package pipeline

import (
	"sort"

	"rediskg/internal/chunker"
	"rediskg/pkg/models"
)

// Chunker splits documents into chunks ready for extraction. Implementations
// should be deterministic — same input must yield the same chunk IDs so
// content-hash short-circuit + MENTIONED_IN edges stay stable across runs.
type Chunker interface {
	ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk
}

// Resolver builds the alias map and the canonical-entity set from the raw
// extraction output. Replace it to A/B test canonicalisation strategies
// (e.g. embedding-based dedup, LLM-judged merge) without rerunning extraction.
type Resolver interface {
	Resolve(entities []models.CandidateEntity) (canonicals map[string]*models.CanonicalEntity, aliasMap map[string]string)
}

// Canonicalizer applies domain-aware post-processing to canonical entities
// (functional role cleanup, status fixing, service-name collapse, alias
// property propagation). Decoupled from Resolver so the cheap, generic
// resolution pass can be reused with different cleanup rules.
type Canonicalizer interface {
	Canonicalize(entities map[string]*models.CanonicalEntity, aliasMap map[string]string)
}

// Extractor takes a set of chunks and returns extracted entities + edges.
// Replace it to swap extraction strategies (e.g., local NER + LLM verification,
// schema-constrained extraction, zero-shot).
type Extractor interface {
	Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph
}

// Reranker scores and selects the top-K candidate chunks given a query vector.
// Replace it to swap reranking strategies (e.g., cross-encoder, reciprocal
// rank fusion, MMR diversity reranking).
type Reranker interface {
	Rerank(queryVec []float32, candidates []*RankedChunk, topK int) []*RankedChunk
}

// RankedChunk is a candidate chunk with its text, embedding, and score.
type RankedChunk struct {
	ID        string
	Text      string
	Embedding []float32
	Score     float64
	Sources   []string // retrieval paths that found this chunk
}

// ── Default implementations ──────────────────────────────────────
//
// These wrap the existing free functions so the public API stays the same
// while opening the door to plug-in replacements. Pipeline.New uses them
// by default; callers can swap any of the three by setting the matching
// field on *Pipeline before calling Ingest.

// defaultChunker delegates to the existing fixed-size chunker.
type defaultChunker struct{}

func (defaultChunker) ChunkDocuments(docs []*models.Document, size, overlap int) []*models.Chunk {
	return chunker.ChunkDocuments(docs, size, overlap)
}

// defaultResolver wraps buildAliasMap + addServiceCanonRules + selectCanonicalEntities,
// which is the exact pipeline shape we run today.
type defaultResolver struct{}

func (defaultResolver) Resolve(entities []models.CandidateEntity) (map[string]*models.CanonicalEntity, map[string]string) {
	aliasMap := buildAliasMap(entities)
	addServiceCanonRules(entities, aliasMap)
	canonicals := selectCanonicalEntities(entities, aliasMap)
	return canonicals, aliasMap
}

// defaultCanonicalizer wraps the role/status/service-cleanup helpers that
// run right after canonical selection.
type defaultCanonicalizer struct{}

func (defaultCanonicalizer) Canonicalize(entities map[string]*models.CanonicalEntity, aliasMap map[string]string) {
	cleanConflictingFunctionalRoles(entities)
	fixEntityStatuses(entities)
	canonicalizeServiceEntities(entities)
	applyAliasProperties(entities, aliasMap)
}

// defaultExtractor wraps extractSchemaConstrained — the LLM-based extraction
// that uses schema context to guide entity/relation extraction.
type defaultExtractor struct {
	pipeline *Pipeline
}

func (de defaultExtractor) Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph {
	return de.pipeline.extractSchemaConstrained(chunks)
}

// defaultReranker uses cosine similarity with stored embeddings (fast path)
// or re-embeds at query time (slow path). Matches the existing rerankChunks.
type defaultReranker struct {
	pipeline *Pipeline
}

func (dr defaultReranker) Rerank(queryVec []float32, candidates []*RankedChunk, topK int) []*RankedChunk {
	if len(candidates) == 0 {
		return nil
	}

	// Count candidates with stored embeddings
	withEmb := 0
	for _, c := range candidates {
		if len(c.Embedding) > 0 {
			withEmb++
		}
	}

	type scored struct {
		chunk *RankedChunk
		score float64
	}
	items := make([]scored, len(candidates))

	if float64(withEmb)/float64(len(candidates)) >= 0.9 {
		// Fast path: use stored embeddings
		for i, c := range candidates {
			if len(c.Embedding) == 0 {
				items[i] = scored{c, -1.0}
				continue
			}
			items[i] = scored{c, cosineSim(queryVec, c.Embedding)}
		}
	} else {
		// Slow path: re-embed
		for i, c := range candidates {
			if len(c.Embedding) > 0 {
				items[i] = scored{c, cosineSim(queryVec, c.Embedding)}
				continue
			}
			vec, err := dr.pipeline.llmClient.Embed(c.Text)
			if err != nil {
				items[i] = scored{c, -1.0}
				continue
			}
			items[i] = scored{c, cosineSim(queryVec, vec)}
		}
	}

	// Sort descending by score
	sort.SliceStable(items, func(i, j int) bool { return items[i].score > items[j].score })

	out := make([]*RankedChunk, 0, topK)
	for i := 0; i < len(items) && i < topK; i++ {
		items[i].chunk.Score = items[i].score
		out = append(out, items[i].chunk)
	}
	return out
}
