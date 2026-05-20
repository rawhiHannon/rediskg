package chunker

import (
	"rediskg/pkg/models"

	"github.com/google/uuid"
)

// ContextualChunker wraps a base chunker and prepends a short LLM-generated
// context summary to each chunk. This implements the "contextual retrieval"
// pattern: each chunk carries a brief description of where it fits in the
// overall document, improving retrieval and extraction quality.
//
// The LLM call is provided via the ContextFn callback so this package doesn't
// depend on internal/llm. Wire it in the pipeline layer.
type ContextualChunker struct {
	Base      func(docs []*models.Document, chunkSize, overlap int) []*models.Chunk
	ContextFn func(docText, chunkText string) string // returns a 1-2 sentence context prefix
	Workers   int
}

func (cc *ContextualChunker) ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk {
	if cc.Base == nil {
		cc.Base = ChunkDocuments
	}

	// First, chunk normally
	chunks := cc.Base(docs, chunkSize, overlap)

	if cc.ContextFn == nil {
		return chunks
	}

	// Build a map of source -> full document text for context generation
	docText := make(map[string]string)
	for _, doc := range docs {
		docText[doc.Source] = doc.Content
	}

	workers := cc.Workers
	if workers <= 0 {
		workers = 4
	}

	// Generate context prefixes concurrently
	type result struct {
		idx     int
		context string
	}
	results := make(chan result, len(chunks))
	sem := make(chan struct{}, workers)

	for i, chunk := range chunks {
		sem <- struct{}{}
		go func(idx int, c *models.Chunk) {
			defer func() { <-sem }()
			fullDoc := docText[c.Source]
			if fullDoc == "" {
				results <- result{idx: idx}
				return
			}
			// Truncate document if too long (keep first 3000 chars for context)
			if len(fullDoc) > 3000 {
				fullDoc = fullDoc[:3000] + "\n...[truncated]"
			}
			ctx := cc.ContextFn(fullDoc, c.Text)
			results <- result{idx: idx, context: ctx}
		}(i, chunk)
	}

	// Collect results
	for range chunks {
		r := <-results
		if r.context != "" {
			chunks[r.idx].Text = "Context: " + r.context + "\n\n" + chunks[r.idx].Text
			if chunks[r.idx].Metadata == nil {
				chunks[r.idx].Metadata = map[string]string{}
			}
			chunks[r.idx].Metadata["contextual_prefix"] = r.context
		}
	}

	// Re-assign stable IDs after context prepending
	for i, c := range chunks {
		c.ID = uuid.New().String()[:32]
		c.Index = i
	}

	return chunks
}
