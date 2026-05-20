package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"strings"

	"rediskg/internal/store"
	"rediskg/pkg/models"
)

// lexicalBackboneEnabled controls whether the ingest emits a Document/Chunk
// provenance graph. Kept as a constant so it can be flipped easily; the
// SDK-style backbone is the only mode we ship right now.
const lexicalBackboneEnabled = true

// documentID derives a stable id for a Document. Falls back to a hash of
// the content when no source path is available (raw text ingest).
func documentID(doc *models.Document) string {
	if doc == nil {
		return ""
	}
	s := strings.TrimSpace(doc.Source)
	if s != "" {
		return s
	}
	sum := sha256.Sum256([]byte(doc.Content))
	return "doc-" + hex.EncodeToString(sum[:])[:16]
}

// documentContentHash returns the SHA-256 of the document's text. Stored on
// the :Document node so a re-ingest of the same content becomes a no-op.
func documentContentHash(doc *models.Document) string {
	if doc == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(doc.Content))
	return hex.EncodeToString(sum[:])
}

// filterUnchangedDocs removes documents whose stored content_hash matches
// the SHA-256 of the new content. Returns the docs that actually need to be
// re-ingested. When everything is unchanged the caller can early-return.
//
// Idempotent re-ingest is one of the explicit wins from GraphRAG-SDK's
// design — a single Cypher lookup avoids re-running extraction + writes for
// documents we've already processed.
func filterUnchangedDocs(s *store.FalkorStore, docs []*models.Document) []*models.Document {
	if !lexicalBackboneEnabled || len(docs) == 0 {
		return docs
	}
	kept := make([]*models.Document, 0, len(docs))
	skipped := 0
	for _, d := range docs {
		id := documentID(d)
		hash := documentContentHash(d)
		if id == "" || hash == "" {
			kept = append(kept, d)
			continue
		}
		existing := fetchDocumentHash(s, id)
		if existing != "" && existing == hash {
			skipped++
			log.Printf("  Skipping unchanged document: %s", id)
			continue
		}
		kept = append(kept, d)
	}
	if skipped > 0 {
		log.Printf("  Content-hash short-circuit: skipped %d unchanged document(s)", skipped)
	}
	return kept
}

// fetchDocumentHash reads :Document.content_hash for the given id, or ""
// if no such Document exists yet.
func fetchDocumentHash(s *store.FalkorStore, id string) string {
	res, err := s.ROQueryWithParams(
		`MATCH (d:Document {id: $id}) RETURN d.content_hash LIMIT 1`,
		map[string]interface{}{"id": id},
	)
	if err != nil {
		return ""
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return ""
	}
	rows, ok := arr[1].([]interface{})
	if !ok || len(rows) == 0 {
		return ""
	}
	row, ok := rows[0].([]interface{})
	if !ok || len(row) == 0 {
		return ""
	}
	if hash, ok := row[0].(string); ok {
		return hash
	}
	return ""
}

// writeLexicalBackbone emits the Document + Chunk provenance graph:
//
//	(:Document {id, content_hash, path})
//	(:Chunk {id, text, index, source})
//	(:Chunk)-[:PART_OF]->(:Document)
//	(:Chunk)-[:NEXT_CHUNK]->(:Chunk)   // sequential within the document
//
// This runs early in the pipeline so that MENTIONED_IN edges (written later)
// can MATCH the Chunk nodes by id.
func writeLexicalBackbone(s *store.FalkorStore, docs []*models.Document, chunks []*models.Chunk) {
	if !lexicalBackboneEnabled || len(docs) == 0 {
		return
	}
	// --- Documents ---
	docBatch := make([]interface{}, 0, len(docs))
	for _, d := range docs {
		id := documentID(d)
		if id == "" {
			continue
		}
		docBatch = append(docBatch, map[string]interface{}{
			"id":           id,
			"content_hash": documentContentHash(d),
			"path":         d.Source,
		})
	}
	if len(docBatch) > 0 {
		q := "UNWIND $batch AS item " +
			"MERGE (d:Document {id: item.id}) " +
			"SET d.content_hash = item.content_hash, d.path = item.path"
		if _, err := s.QueryWithParams(q, map[string]interface{}{"batch": docBatch}); err != nil {
			log.Printf("Warning: failed to upsert Document nodes: %v", err)
		}
	}

	// --- Chunks + PART_OF + NEXT_CHUNK ---
	// Group chunks by source (= document id) so NEXT_CHUNK only links
	// chunks within the same document.
	chunksByDoc := map[string][]*models.Chunk{}
	docOrder := []string{}
	for _, c := range chunks {
		src := c.Source
		if _, ok := chunksByDoc[src]; !ok {
			docOrder = append(docOrder, src)
		}
		chunksByDoc[src] = append(chunksByDoc[src], c)
	}

	chunkBatch := make([]interface{}, 0, len(chunks))
	partOf := make([]interface{}, 0, len(chunks))
	nextChunk := make([]interface{}, 0, len(chunks))
	for _, src := range docOrder {
		group := chunksByDoc[src]
		for i, c := range group {
			cleanText := store.SanitizeControl(c.Text)
			chunkBatch = append(chunkBatch, map[string]interface{}{
				"id":     c.ID,
				"text":   cleanText,
				"index":  c.Index,
				"source": src,
			})
			partOf = append(partOf, map[string]interface{}{
				"chunk_id": c.ID,
				"doc_id":   src,
				"index":    c.Index,
			})
			if i > 0 {
				nextChunk = append(nextChunk, map[string]interface{}{
					"prev_id": group[i-1].ID,
					"next_id": c.ID,
				})
			}
		}
	}

	if len(chunkBatch) > 0 {
		q := "UNWIND $batch AS item " +
			"MERGE (c:Chunk {id: item.id}) " +
			"SET c.text = item.text, c.index = item.index, c.source = item.source"
		if _, err := s.QueryWithParams(q, map[string]interface{}{"batch": chunkBatch}); err != nil {
			log.Printf("Warning: failed to upsert Chunk nodes: %v", err)
		}
	}
	if len(partOf) > 0 {
		q := "UNWIND $batch AS item " +
			"MATCH (c:Chunk {id: item.chunk_id}), (d:Document {id: item.doc_id}) " +
			"MERGE (c)-[r:PART_OF]->(d) " +
			"SET r.index = item.index"
		if _, err := s.QueryWithParams(q, map[string]interface{}{"batch": partOf}); err != nil {
			log.Printf("Warning: failed to write PART_OF edges: %v", err)
		}
	}
	if len(nextChunk) > 0 {
		q := "UNWIND $batch AS item " +
			"MATCH (a:Chunk {id: item.prev_id}), (b:Chunk {id: item.next_id}) " +
			"MERGE (a)-[:NEXT_CHUNK]->(b)"
		if _, err := s.QueryWithParams(q, map[string]interface{}{"batch": nextChunk}); err != nil {
			log.Printf("Warning: failed to write NEXT_CHUNK edges: %v", err)
		}
	}
	log.Printf("  Lexical backbone: %d documents, %d chunks, %d PART_OF, %d NEXT_CHUNK",
		len(docBatch), len(chunkBatch), len(partOf), len(nextChunk))
}

// writeMentionedInEdges links each materialised :Concept entity to the
// chunk(s) it was extracted from. Uses canonical entity name → set of chunk
// ids built from the canonical entities' evidence trail.
//
// Only emitted when the lexical backbone is enabled (otherwise the target
// :Chunk nodes don't exist).
func writeMentionedInEdges(s *store.FalkorStore, canonicalEntities map[string]*models.CanonicalEntity, materialised map[string]bool) {
	if !lexicalBackboneEnabled || len(canonicalEntities) == 0 {
		return
	}
	type pair struct{ entity, chunk string }
	seen := map[pair]bool{}
	batch := make([]interface{}, 0)
	for name, ent := range canonicalEntities {
		if ent == nil {
			continue
		}
		lname := strings.ToLower(strings.TrimSpace(name))
		if lname == "" || !materialised[lname] {
			continue
		}
		for _, ev := range ent.Evidence {
			cid := strings.TrimSpace(ev.ChunkID)
			if cid == "" {
				continue
			}
			key := pair{lname, cid}
			if seen[key] {
				continue
			}
			seen[key] = true
			batch = append(batch, map[string]interface{}{
				"entity": lname,
				"chunk":  cid,
			})
		}
	}
	if len(batch) == 0 {
		return
	}
	q := "UNWIND $batch AS item " +
		"MATCH (e:Concept {name: item.entity}), (c:Chunk {id: item.chunk}) " +
		"MERGE (e)-[:MENTIONED_IN]->(c)"
	if _, err := s.QueryWithParams(q, map[string]interface{}{"batch": batch}); err != nil {
		log.Printf("Warning: failed to write MENTIONED_IN edges: %v", err)
		return
	}
	log.Printf("  Lexical backbone: %d MENTIONED_IN edges", len(batch))
}
