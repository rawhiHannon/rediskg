package pipeline

import (
	"encoding/json"
	"log"
	"strings"
	"sync"

	"rediskg/internal/llm"
	"rediskg/internal/ner"
	"rediskg/pkg/models"
)

// HybridExtractor uses NER for fast entity extraction (no LLM cost), then
// sends those entities + chunk text to the LLM for verification and
// relationship extraction. This cuts LLM calls in half compared to the
// default two-pass LLM approach.
//
// By default it uses the built-in Go rule-based NER (zero setup). If an
// external NER service URL is configured, it uses that instead.
//
// Flow per chunk:
//
//	NER (built-in or external) → entity spans
//	         ↓
//	LLM pass (verify entities + extract relations) → verified entities + edges
type HybridExtractor struct {
	pipeline     *Pipeline
	nerExtractor ner.Extractor
}

// NewHybridExtractor creates a hybrid extractor. If nerURL is non-empty, it
// uses the external HTTP NER service; otherwise it uses the built-in
// Go rule-based NER engine (no setup required).
func NewHybridExtractor(p *Pipeline, nerURL string) *HybridExtractor {
	var ext ner.Extractor
	if nerURL != "" {
		ext = ner.NewClient(nerURL)
	} else {
		ext = ner.NewBuiltinExtractor()
	}
	return &HybridExtractor{
		pipeline:     p,
		nerExtractor: ext,
	}
}

// Extract runs hybrid NER+LLM extraction across all chunks concurrently.
func (he *HybridExtractor) Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		allEnts  []models.CandidateEntity
		allEdges []models.CandidateEdge
		sem      = make(chan struct{}, workers)
	)

	for _, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}

		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			entities, edges := he.extractChunk(c)
			if len(entities) == 0 && len(edges) == 0 {
				return
			}

			mu.Lock()
			allEnts = append(allEnts, entities...)
			allEdges = append(allEdges, edges...)
			mu.Unlock()
		}(chunk)
	}

	wg.Wait()
	return &models.CandidateGraph{
		Entities: allEnts,
		Edges:    allEdges,
	}
}

// extractChunk runs the two-phase hybrid extraction for a single chunk.
func (he *HybridExtractor) extractChunk(c *models.Chunk) ([]models.CandidateEntity, []models.CandidateEdge) {
	// Phase 1: Local NER (free, fast)
	spans, err := he.nerExtractor.Extract(c.Text)
	if err != nil {
		log.Printf("Warning: NER service failed for chunk %s, falling back to LLM: %v", c.ID, err)
		return he.fallbackLLM(c)
	}

	if len(spans) == 0 {
		// NER found nothing — try LLM fallback in case the text has
		// domain-specific entities the NER model doesn't know about.
		return he.fallbackLLM(c)
	}

	// Convert NER spans to the entity summary format the LLM verify pass expects.
	nerEntities := spansToNERSummary(spans, c.ID)

	// Phase 2: LLM verify + extract relations (1 call instead of 2)
	nerJSON := FormatNEREntitiesForPrompt(nerEntities)
	verified, edges, err := llm.VerifyAndExtractFromNER(
		he.pipeline.llmClient, c.Text, c.ID, nerJSON,
	)
	if err != nil {
		log.Printf("Warning: hybrid verify+extract failed for chunk %s, keeping NER entities: %v", c.ID, err)
		return nerEntitiesToCandidates(spans, c.ID), nil
	}
	if len(verified) == 0 {
		log.Printf("Warning: hybrid verify returned 0 entities for chunk %s, keeping NER entities", c.ID)
		return nerEntitiesToCandidates(spans, c.ID), edges
	}
	return verified, edges
}

// fallbackLLM falls back to the standard two-pass LLM extraction when NER
// service is unavailable or returns no results.
func (he *HybridExtractor) fallbackLLM(c *models.Chunk) ([]models.CandidateEntity, []models.CandidateEdge) {
	entities, edges, _ := llm.ExtractWithSchema(he.pipeline.llmClient, c.Text, c.ID)
	return entities, edges
}

// nerEntitiesToCandidates converts raw NER spans into minimal CandidateEntities
// (used as fallback when the LLM verify pass fails).
func nerEntitiesToCandidates(spans []ner.Span, chunkID string) []models.CandidateEntity {
	out := make([]models.CandidateEntity, 0, len(spans))
	seen := map[string]bool{}
	for _, s := range spans {
		name := strings.ToLower(strings.TrimSpace(s.Text))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		ce := models.CandidateEntity{
			Mention:       name,
			CanonicalName: name,
			ChunkID:       chunkID,
		}
		if bt := ner.LabelToBaseType(s.Label); bt != "" {
			ce.BaseTypes = []models.ScoredType{{Type: bt, Score: 0.8}}
		}
		out = append(out, ce)
	}
	return out
}

// NEREntitySummary is the minimal entity info passed from NER to the LLM
// verify pass. It mirrors the nerSummary shape from extract_schema.go but
// is built from NER spans instead of a prior LLM call.
type NEREntitySummary struct {
	Mention   string `json:"mention"`
	BaseType  string `json:"base_type,omitempty"`
	NERLabel  string `json:"ner_label"`
	StartChar int    `json:"start_char"`
	EndChar   int    `json:"end_char"`
}

func spansToNERSummary(spans []ner.Span, chunkID string) []NEREntitySummary {
	seen := map[string]bool{}
	out := make([]NEREntitySummary, 0, len(spans))
	for _, s := range spans {
		name := strings.TrimSpace(s.Text)
		key := strings.ToLower(name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, NEREntitySummary{
			Mention:   name,
			BaseType:  ner.LabelToBaseType(s.Label),
			NERLabel:  s.Label,
			StartChar: s.Start,
			EndChar:   s.End,
		})
	}
	return out
}

// FormatNEREntitiesForPrompt serializes NER entity summaries as JSON for the
// LLM verify prompt.
func FormatNEREntitiesForPrompt(entities []NEREntitySummary) string {
	b, err := json.Marshal(entities)
	if err != nil {
		return "[]"
	}
	return string(b)
}
