package pipeline

import (
	"log"
	"strings"

	"rediskg/internal/llm"
	"rediskg/internal/schema"
	"rediskg/internal/store"
	"rediskg/pkg/config"
)

// Pipeline orchestrates the full knowledge graph extraction process.
type Pipeline struct {
	cfg       *config.Config
	store     *store.FalkorStore
	llmClient *llm.Client
	schema    *schema.Schema
}

// New creates a new Pipeline.
func New(cfg *config.Config, store *store.FalkorStore, llmClient *llm.Client) *Pipeline {
	return &Pipeline{
		cfg:       cfg,
		store:     store,
		llmClient: llmClient,
		schema:    schema.New(),
	}
}

// generateEmbeddings creates a vector index and stores embeddings for entity dedup.
func (p *Pipeline) generateEmbeddings() error {
	err := p.store.CreateVectorIndex("Concept", "embedding", p.cfg.EmbeddingDimension)
	if err != nil {
		log.Printf("Vector index may already exist: %v", err)
	}

	nodes, err := p.store.GetAllNodes()
	if err != nil {
		return err
	}

	for _, node := range nodes {
		name, ok := node["col_0"].(string)
		if !ok || name == "" {
			continue
		}

		embedding, err := p.llmClient.Embed(name)
		if err != nil {
			log.Printf("Warning: failed to embed '%s': %v", name, err)
			continue
		}

		if err := p.store.SetEntityEmbedding(name, embedding); err != nil {
			log.Printf("Warning: failed to store embedding for '%s': %v", name, err)
		}
	}

	return nil
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
