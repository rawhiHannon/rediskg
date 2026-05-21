package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"rediskg/internal/chunker"
	"rediskg/internal/llm"
	"rediskg/internal/schema"
	"rediskg/internal/store"
	"rediskg/pkg/config"
)

// Pipeline orchestrates the full knowledge graph extraction process.
//
// Five strategy slots are pluggable — set Chunker / Resolver / Canonicalizer /
// Extractor / Reranker to a custom implementation before calling Ingest to A/B
// test alternative strategies without touching the rest of the pipeline.
// Default implementations preserve current behaviour.
type Pipeline struct {
	cfg       *config.Config
	store     *store.FalkorStore
	llmClient *llm.Client
	schema    *schema.Schema

	// Strategy hooks — replace via direct assignment after New().
	Chunker       Chunker
	Resolver      Resolver
	Canonicalizer Canonicalizer
	Extractor     Extractor
	Reranker      Reranker
	Coref         *CorefResolver // nil = disabled; set to enable pronoun resolution

	// Telemetry — set during Ingest, nil when idle. Read via Stats/SubscribeStats.
	stats *PipelineStats
	statsMu sync.RWMutex
}

// New creates a new Pipeline with the default strategy implementations.
func New(cfg *config.Config, store *store.FalkorStore, llmClient *llm.Client) *Pipeline {
	p := &Pipeline{
		cfg:           cfg,
		store:         store,
		llmClient:     llmClient,
		schema:        schema.New(),
		Chunker:       selectChunker(cfg, llmClient),
		Resolver:      NewTieredResolver(llmClient),
		Canonicalizer: defaultCanonicalizer{},
		Coref:         &CorefResolver{LLM: llmClient, Workers: cfg.Workers},
	}
	p.Extractor = selectExtractor(cfg, p)
	p.Reranker = defaultReranker{pipeline: p}
	return p
}

// SetExtractor switches the extraction strategy at runtime (e.g. per-request).
func (p *Pipeline) SetExtractor(strategy, nerURL string) {
	switch strategy {
	case "hybrid":
		p.Extractor = NewHybridExtractor(p, nerURL)
	default:
		p.Extractor = defaultExtractor{pipeline: p}
	}
}

// Stats returns the current pipeline telemetry (nil when idle).
func (p *Pipeline) Stats() *PipelineStats {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	return p.stats
}

// SubscribeStats returns a channel that receives JSON-encoded SSE events
// for pipeline progress. If no pipeline is running, returns nil.
func (p *Pipeline) SubscribeStats() chan []byte {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	if p.stats == nil {
		return nil
	}
	return p.stats.Subscribe()
}

// UnsubscribeStats removes a subscriber channel.
func (p *Pipeline) UnsubscribeStats(ch chan []byte) {
	p.statsMu.RLock()
	defer p.statsMu.RUnlock()
	if p.stats != nil {
		p.stats.Unsubscribe(ch)
	}
}

// selectChunker returns the appropriate Chunker based on cfg.ChunkStrategy.
func selectChunker(cfg *config.Config, llmClient *llm.Client) Chunker {
	switch cfg.ChunkStrategy {
	case "sentence":
		return chunker.SentenceChunker{}
	case "structural":
		return chunker.StructuralChunker{}
	case "contextual":
		return &chunker.ContextualChunker{
			Workers: cfg.Workers,
			ContextFn: func(docText, chunkText string) string {
				resp, err := llmClient.Complete(
					`You are a document analyst. Given the full document and a chunk from it, write a short 1-2 sentence context that explains where this chunk fits in the overall document. Be concise. Respond as JSON: {"context": "..."}`,
					"DOCUMENT:\n"+docText+"\n\nCHUNK:\n"+chunkText,
				)
				if err != nil {
					return ""
				}
				var result struct {
					Context string `json:"context"`
				}
				if err := json.Unmarshal([]byte(resp), &result); err != nil {
					return ""
				}
				return result.Context
			},
		}
	default:
		return defaultChunker{}
	}
}

// selectExtractor picks the extraction strategy based on config.
func selectExtractor(cfg *config.Config, p *Pipeline) Extractor {
	switch cfg.ExtractionStrategy {
	case "hybrid":
		url := cfg.NERServiceURL
		if url != "" {
			log.Printf("Using hybrid extraction (external NER: %s)", url)
		} else {
			log.Printf("Using hybrid extraction (built-in NER)")
		}
		return NewHybridExtractor(p, url)
	default:
		return defaultExtractor{pipeline: p}
	}
}

// generateEmbeddings creates vector indexes and stores embeddings for the
// three things multi-path retrieval needs:
//
//  1. Entity names on :Concept(embedding)
//  2. Chunk text on :Chunk(embedding)
//  3. Edge fact strings on :<RelType>(embedding), per relation type
//
// All embedding calls run concurrently bounded by cfg.Workers. Per-rel
// vector indexes are created lazily: only for relation types that actually
// have at least one materialised edge with a non-empty fact string.
func (p *Pipeline) generateEmbeddings() error {
	dim := p.cfg.EmbeddingDimension

	// --- Indexes (best-effort: "index exists" is not an error) ---
	if err := p.store.CreateVectorIndex("Concept", "embedding", dim); err != nil {
		log.Printf("Concept vector index: %v", err)
	}
	if err := p.store.CreateVectorIndex("Chunk", "embedding", dim); err != nil {
		log.Printf("Chunk vector index: %v", err)
	}

	// --- Fulltext indexes (best-effort) ---
	if err := p.store.CreateFulltextIndex("Concept", "name"); err != nil {
		log.Printf("Concept fulltext index: %v", err)
	}
	if err := p.store.CreateFulltextIndex("Chunk", "text"); err != nil {
		log.Printf("Chunk fulltext index: %v", err)
	}

	// --- Entity name embeddings ---
	if err := p.embedConceptNames(); err != nil {
		log.Printf("Warning: entity embedding failed: %v", err)
	}

	// --- Chunk text embeddings ---
	if err := p.embedChunks(); err != nil {
		log.Printf("Warning: chunk embedding failed: %v", err)
	}

	// --- Edge fact embeddings (per relation type) ---
	if err := p.embedEdgeFacts(dim); err != nil {
		log.Printf("Warning: edge fact embedding failed: %v", err)
	}

	return nil
}

// embedConceptNames embeds every :Concept.name and stores it as .embedding.
// Concurrent up to cfg.Workers.
func (p *Pipeline) embedConceptNames() error {
	nodes, err := p.store.GetAllNodes()
	if err != nil {
		return err
	}
	workers := p.cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	done := 0
	var mu sync.Mutex
	for _, node := range nodes {
		name, ok := node["col_0"].(string)
		if !ok || name == "" {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			defer func() { <-sem }()
			embedding, err := embedWithRetry(func() ([]float32, error) { return p.llmClient.Embed(name) })
			if err != nil {
				log.Printf("Warning: embed entity %q: %v", name, err)
				return
			}
			if err := p.store.SetEntityEmbedding(name, embedding); err != nil {
				log.Printf("Warning: store entity embedding %q: %v", name, err)
				return
			}
			mu.Lock()
			done++
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	log.Printf("  Embedded %d entities", done)
	return nil
}

// embedChunks fetches every :Chunk and embeds its text. Concurrent up to
// cfg.Workers. Existing chunks with an embedding already are re-embedded
// (cheap, ensures consistency after re-ingest with the same id).
func (p *Pipeline) embedChunks() error {
	res, err := p.store.ROQuery(`MATCH (c:Chunk) RETURN c.id, c.text`)
	if err != nil {
		return err
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return nil
	}
	workers := p.cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	done := 0
	var mu sync.Mutex
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 2 {
			continue
		}
		id, _ := cols[0].(string)
		text, _ := cols[1].(string)
		if id == "" || text == "" {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id, text string) {
			defer wg.Done()
			defer func() { <-sem }()
			embedding, err := embedWithRetry(func() ([]float32, error) { return p.llmClient.Embed(text) })
			if err != nil {
				log.Printf("Warning: embed chunk %s: %v", id, err)
				return
			}
			if err := p.store.SetChunkEmbedding(id, embedding); err != nil {
				log.Printf("Warning: store chunk embedding %s: %v", id, err)
				return
			}
			mu.Lock()
			done++
			mu.Unlock()
		}(id, text)
	}
	wg.Wait()
	log.Printf("  Embedded %d chunks", done)
	return nil
}

// embedEdgeFacts walks every relationship type that has materialised edges
// with a fact string, embeds each fact, and stores it as r.embedding. A
// vector index is created per relation type so multi-path retrieval can
// query each one. Concurrent up to cfg.Workers.
func (p *Pipeline) embedEdgeFacts(dim int) error {
	// Discover which rel types actually appear in the materialised graph.
	res, err := p.store.ROQuery(`CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType`)
	if err != nil {
		return err
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return nil
	}
	var relTypes []string
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 1 {
			continue
		}
		if rt, ok := cols[0].(string); ok && rt != "" {
			relTypes = append(relTypes, rt)
		}
	}

	workers := p.cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	totalEmbedded := 0
	for _, rt := range relTypes {
		// Pull edges and their facts. id(r) is the stable handle we use
		// to write the embedding back without recomputing endpoints.
		q := fmt.Sprintf(`MATCH ()-[r:%s]->() WHERE r.fact IS NOT NULL AND r.fact <> '' RETURN id(r), r.fact`, rt)
		res, err := p.store.ROQuery(q)
		if err != nil {
			log.Printf("Warning: list %s edges: %v", rt, err)
			continue
		}
		arr, ok := res.([]interface{})
		if !ok || len(arr) < 2 {
			continue
		}
		rows, ok := arr[1].([]interface{})
		if !ok || len(rows) == 0 {
			continue
		}
		// Create vector index for this rel type (lazy, ignore "already exists").
		if err := p.store.CreateEdgeVectorIndex(rt, "embedding", dim); err != nil {
			log.Printf("  %s vector index: %v", rt, err)
		}

		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		var mu sync.Mutex
		done := 0
		for _, row := range rows {
			cols, ok := row.([]interface{})
			if !ok || len(cols) < 2 {
				continue
			}
			var relID int64
			switch v := cols[0].(type) {
			case int64:
				relID = v
			case float64:
				relID = int64(v)
			default:
				continue
			}
			fact, _ := cols[1].(string)
			if fact == "" {
				continue
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(relID int64, fact string) {
				defer wg.Done()
				defer func() { <-sem }()
				embedding, err := embedWithRetry(func() ([]float32, error) { return p.llmClient.Embed(fact) })
				if err != nil {
					log.Printf("Warning: embed %s fact id=%d: %v", rt, relID, err)
					return
				}
				vec := float32SliceToVecStr(embedding)
				upd := fmt.Sprintf(`MATCH ()-[r:%s]->() WHERE id(r) = %d SET r.embedding = vecf32(%s)`, rt, relID, vec)
				if _, err := p.store.Query(upd); err != nil {
					log.Printf("Warning: store %s embedding id=%d: %v", rt, relID, err)
					return
				}
				mu.Lock()
				done++
				mu.Unlock()
			}(relID, fact)
		}
		wg.Wait()
		totalEmbedded += done
		log.Printf("  Embedded %d [%s] edge facts", done, rt)
	}
	log.Printf("  Edge facts embedded: %d across %d relation type(s)", totalEmbedded, len(relTypes))
	return nil
}

// embedWithRetry retries an embedding call up to 3 times with exponential
// backoff (1s, 2s, 4s). This prevents a single transient API error from
// silently dropping an entity/chunk embedding.
func embedWithRetry(fn func() ([]float32, error)) ([]float32, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		vec, err := fn()
		if err == nil {
			return vec, nil
		}
		lastErr = err
		time.Sleep(time.Duration(1<<uint(attempt)) * time.Second)
	}
	return nil, lastErr
}

// float32SliceToVecStr formats a []float32 as the Cypher vecf32 argument
// list `[f, f, …]`. Duplicate-named in store/falkor.go for the store layer's
// use; kept here as a private helper for the pipeline's direct Cypher.
func float32SliceToVecStr(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%f", f)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func deduplicateStrings(ss []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
