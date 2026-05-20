package pipeline

import (
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

// UpdateDocument performs a crash-safe incremental update of a single document.
//
// The flow mirrors GraphRAG-SDK's update() pattern:
//  1. Ingest new content into a __pending__ document node
//  2. Mark ready_to_commit atomically
//  3. Swap: delete old document's entities/chunks, rename pending to live
//
// Crash safety: if the process dies between steps 1 and 3, the next call to
// UpdateDocument or Finalize detects the pending node and retries the cutover.
func (p *Pipeline) UpdateDocument(content, source string) error {
	// First, recover any stuck pending documents from a prior crash.
	p.recoverPendingDocuments()

	docID := source
	if docID == "" {
		return fmt.Errorf("source path required for update")
	}

	// Check if content actually changed.
	doc := &models.Document{Content: content, Source: source}
	newHash := documentContentHash(doc)
	oldHash := fetchDocumentHash(p.store, docID)
	if oldHash != "" && oldHash == newHash {
		log.Printf("Document %s unchanged, skipping update", docID)
		return nil
	}

	pendingID := "__pending__:" + docID

	// Step 1: Ingest new content under a pending document ID.
	pendingDoc := &models.Document{
		Content: content,
		Source:  pendingID,
		Metadata: map[string]string{
			"original_doc_id": docID,
			"content_hash":    newHash,
		},
	}
	if err := p.Ingest([]*models.Document{pendingDoc}); err != nil {
		return fmt.Errorf("pending ingest failed: %w", err)
	}

	// Step 2: Mark ready_to_commit atomically.
	markQ := `MATCH (d:Document {id: $id}) SET d.ready_to_commit = true, d.original_doc_id = $orig`
	if _, err := p.store.QueryWithParams(markQ, map[string]interface{}{
		"id":   pendingID,
		"orig": docID,
	}); err != nil {
		return fmt.Errorf("failed to mark pending ready: %w", err)
	}

	// Step 3: Cutover — delete old, rename pending.
	return p.cutoverDocument(pendingID, docID, newHash)
}

// DeleteDocument removes a document and all its exclusive entities/chunks.
// Entities shared with other documents are preserved (only the MENTIONED_IN
// edges from this document's chunks are removed).
func (p *Pipeline) DeleteDocument(docID string) error {
	if docID == "" {
		return fmt.Errorf("document ID required")
	}

	// Find chunks belonging to this document.
	chunkQ := `MATCH (c:Chunk)-[:PART_OF]->(d:Document {id: $id}) RETURN c.id`
	res, err := p.store.ROQueryWithParams(chunkQ, map[string]interface{}{"id": docID})
	if err != nil {
		return fmt.Errorf("failed to find document chunks: %w", err)
	}
	chunkIDs := parseStringColumn(res)
	if len(chunkIDs) == 0 {
		log.Printf("No chunks found for document %s, cleaning up Document node", docID)
		p.store.QueryWithParams(`MATCH (d:Document {id: $id}) DELETE d`,
			map[string]interface{}{"id": docID})
		return nil
	}

	// Find entities mentioned ONLY in this document's chunks.
	// Entities also mentioned in other documents are shared and must survive.
	exclusiveQ := `
		MATCH (e:Concept)-[:MENTIONED_IN]->(c:Chunk)-[:PART_OF]->(d:Document {id: $id})
		WITH e
		OPTIONAL MATCH (e)-[:MENTIONED_IN]->(c2:Chunk)-[:PART_OF]->(d2:Document)
		WHERE d2.id <> $id
		WITH e, count(d2) AS otherDocs
		WHERE otherDocs = 0
		RETURN e.name`
	exclusiveRes, err := p.store.ROQueryWithParams(exclusiveQ, map[string]interface{}{"id": docID})
	if err != nil {
		log.Printf("Warning: failed to find exclusive entities: %v", err)
	}
	exclusiveNames := parseStringColumn(exclusiveRes)

	// Delete exclusive entities and their edges.
	if len(exclusiveNames) > 0 {
		for _, name := range exclusiveNames {
			delQ := `MATCH (e:Concept {name: $name}) DETACH DELETE e`
			if _, err := p.store.QueryWithParams(delQ, map[string]interface{}{"name": name}); err != nil {
				log.Printf("Warning: failed to delete entity %s: %v", name, err)
			}
		}
		log.Printf("  Deleted %d exclusive entities", len(exclusiveNames))
	}

	// Remove MENTIONED_IN edges from shared entities to this document's chunks.
	mentionQ := `
		MATCH (e:Concept)-[m:MENTIONED_IN]->(c:Chunk)-[:PART_OF]->(d:Document {id: $id})
		DELETE m`
	p.store.QueryWithParams(mentionQ, map[string]interface{}{"id": docID})

	// Delete chunks and their structural edges.
	chunkDelQ := `MATCH (c:Chunk)-[:PART_OF]->(d:Document {id: $id}) DETACH DELETE c`
	p.store.QueryWithParams(chunkDelQ, map[string]interface{}{"id": docID})

	// Delete the document node.
	p.store.QueryWithParams(`MATCH (d:Document {id: $id}) DELETE d`,
		map[string]interface{}{"id": docID})

	log.Printf("Deleted document %s (%d chunks, %d exclusive entities)", docID, len(chunkIDs), len(exclusiveNames))
	return nil
}

// cutoverDocument swaps a pending document into the live position.
func (p *Pipeline) cutoverDocument(pendingID, liveID, newHash string) error {
	// Delete old document content.
	if err := p.DeleteDocument(liveID); err != nil {
		log.Printf("Warning: failed to clean old document %s: %v", liveID, err)
	}

	// Rename pending document: update id, remove pending markers.
	renameQ := `
		MATCH (d:Document {id: $pending})
		SET d.id = $live, d.content_hash = $hash
		REMOVE d.ready_to_commit, d.original_doc_id`
	if _, err := p.store.QueryWithParams(renameQ, map[string]interface{}{
		"pending": pendingID,
		"live":    liveID,
		"hash":    newHash,
	}); err != nil {
		return fmt.Errorf("failed to rename pending document: %w", err)
	}

	// Update chunk source references.
	chunkQ := `MATCH (c:Chunk {source: $pending}) SET c.source = $live`
	p.store.QueryWithParams(chunkQ, map[string]interface{}{
		"pending": pendingID,
		"live":    liveID,
	})

	// Update PART_OF edges (chunks now point to renamed document).
	partOfQ := `
		MATCH (c:Chunk)-[:PART_OF]->(old:Document {id: $pending})
		MATCH (new:Document {id: $live})
		MERGE (c)-[:PART_OF]->(new)
		DELETE old`
	p.store.QueryWithParams(partOfQ, map[string]interface{}{
		"pending": pendingID,
		"live":    liveID,
	})

	log.Printf("Updated document %s (cutover from %s)", liveID, pendingID)
	return nil
}

// recoverPendingDocuments finds any __pending__ documents left from a prior
// crash and either completes or rolls back the cutover.
func (p *Pipeline) recoverPendingDocuments() {
	q := `MATCH (d:Document) WHERE d.id STARTS WITH '__pending__:' AND d.ready_to_commit = true RETURN d.id, d.original_doc_id, d.content_hash`
	res, err := p.store.ROQuery(q)
	if err != nil {
		return
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return
	}
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 3 {
			continue
		}
		pendingID, _ := cols[0].(string)
		origID, _ := cols[1].(string)
		hash, _ := cols[2].(string)
		if pendingID == "" || origID == "" {
			continue
		}
		log.Printf("Recovering pending document: %s -> %s", pendingID, origID)
		if err := p.cutoverDocument(pendingID, origID, hash); err != nil {
			log.Printf("Warning: failed to recover pending %s: %v", pendingID, err)
		}
	}
}

// Finalize runs post-ingest operations: cross-document dedup + embedding
// refresh. Call this after a batch of Update/Delete operations.
func (p *Pipeline) Finalize() error {
	// Recover any crashed pending documents.
	p.recoverPendingDocuments()

	// Cross-document deduplication (implemented in dedup.go).
	p.deduplicateGlobal()

	// Refresh embeddings.
	log.Println("Refreshing embeddings...")
	return p.generateEmbeddings()
}

// parseStringColumn extracts the first string column from a query result.
func parseStringColumn(result interface{}) []string {
	if result == nil {
		return nil
	}
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 1 {
			continue
		}
		if s, ok := cols[0].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sanitizePropertyKey re-exports the ingest helper for use by update.go.
// The canonical definition lives in batchwriter.go or ingest.go.
func sanitizePropertyKeyUpdate(key string) string {
	return sanitizePropertyKey(key)
}

// ApplyChanges processes a batch of document add/update/delete operations.
// Uses serial processing for updates (correctness) and bounded concurrency
// for adds. Call Finalize() once after ApplyChanges to run global dedup
// and embedding refresh.
func (p *Pipeline) ApplyChanges(added, modified []struct{ Content, Source string }, deleted []string) error {
	var errs []string

	// Deletes first.
	for _, docID := range deleted {
		if err := p.DeleteDocument(docID); err != nil {
			errs = append(errs, fmt.Sprintf("delete %s: %v", docID, err))
		}
	}

	// Adds.
	for _, doc := range added {
		if err := p.IngestRawText(doc.Content, doc.Source); err != nil {
			errs = append(errs, fmt.Sprintf("add %s: %v", doc.Source, err))
		}
	}

	// Modifications (serial for correctness).
	for _, doc := range modified {
		if err := p.UpdateDocument(doc.Content, doc.Source); err != nil {
			errs = append(errs, fmt.Sprintf("update %s: %v", doc.Source, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("apply_changes errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
