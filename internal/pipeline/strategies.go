package pipeline

import (
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
