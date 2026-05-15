package pipeline

import (
	"fmt"
	"log"
	"sync"

	"rediskg/internal/chunker"
	"rediskg/internal/graph"
	"rediskg/internal/llm"
	"rediskg/internal/loader"
	"rediskg/internal/store"
	"rediskg/pkg/config"
	"rediskg/pkg/models"
)

// Pipeline orchestrates the full knowledge graph extraction process.
type Pipeline struct {
	cfg       *config.Config
	store     *store.FalkorStore
	llmClient *llm.Client
}

// New creates a new Pipeline.
func New(cfg *config.Config, store *store.FalkorStore, llmClient *llm.Client) *Pipeline {
	return &Pipeline{
		cfg:       cfg,
		store:     store,
		llmClient: llmClient,
	}
}

// IngestDirectory loads all documents from a directory and builds the knowledge graph.
func (p *Pipeline) IngestDirectory(dirPath string) error {
	log.Printf("Loading documents from %s...", dirPath)
	docs, err := loader.LoadDirectory(dirPath)
	if err != nil {
		return fmt.Errorf("failed to load directory: %w", err)
	}
	if len(docs) == 0 {
		return fmt.Errorf("no supported documents found in %s", dirPath)
	}
	log.Printf("Loaded %d documents", len(docs))

	return p.ingestDocuments(docs)
}

// IngestText ingests a raw text string into the knowledge graph.
func (p *Pipeline) IngestText(text, source string) error {
	doc := loader.LoadText(text, source)
	return p.ingestDocuments([]*models.Document{doc})
}

// chunkResult holds the extraction result for a single chunk.
type chunkResult struct {
	entities []models.Entity
	triples  []models.Triple
}

func (p *Pipeline) ingestDocuments(docs []*models.Document) error {
	// Phase 1: Chunk
	log.Println("Chunking documents...")
	chunks := chunker.ChunkDocuments(docs, p.cfg.ChunkSize, p.cfg.ChunkOverlap)
	log.Printf("Created %d chunks", len(chunks))

	// Phase 2: Two-phase extraction per chunk (concurrent)
	log.Println("Extracting entities and relations (2-phase)...")
	allEntities, allTriples := p.extractAll(chunks)
	log.Printf("Extracted %d entities, %d triples", len(allEntities), len(allTriples))

	// Phase 3: Filter stopword/generic nodes
	before := len(allTriples)
	allTriples = graph.FilterTriples(allTriples)
	log.Printf("Filtered %d noisy triples (%d remaining)", before-len(allTriples), len(allTriples))

	// Phase 4: Build entity type map and validate relations + fix directions
	log.Println("Validating relations and directions...")
	entityMap := buildEntityMap(allEntities)
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap)

	// Phase 5: Standardize entity names (merge aliases)
	log.Println("Standardizing entities...")
	allTriples, err := p.standardizeEntities(allTriples)
	if err != nil {
		log.Printf("Warning: standardization failed, continuing with raw names: %v", err)
	}

	// Phase 6: Merge duplicate edges (no proximity — only LLM-extracted edges)
	log.Println("Merging edges...")
	mergedEdges := graph.MergeEdges(allTriples, nil, p.cfg.SemanticWeight)
	log.Printf("Total merged edges: %d", len(mergedEdges))

	// Phase 7: Store in FalkorDB
	log.Println("Storing graph in FalkorDB...")
	if err := p.storeGraph(mergedEdges); err != nil {
		return fmt.Errorf("failed to store graph: %w", err)
	}

	// Phase 8: Generate embeddings
	log.Println("Generating entity embeddings...")
	if err := p.generateEmbeddings(); err != nil {
		log.Printf("Warning: embedding generation failed: %v", err)
	}

	stats, _ := p.store.GetGraphStats()
	log.Printf("Done! Graph has %d nodes and %d edges", stats["nodes"], stats["edges"])

	return nil
}

// extractAll runs 2-phase extraction (entities then relations) on all chunks concurrently.
func (p *Pipeline) extractAll(chunks []*models.Chunk) ([]models.Entity, []models.Triple) {
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		allEntities []models.Entity
		allTriples  []models.Triple
		sem         = make(chan struct{}, p.cfg.Workers)
	)

	for _, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}

		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			// Phase 1: Extract entities
			entities, err := llm.ExtractEntitiesFromChunk(p.llmClient, c.Text)
			if err != nil {
				log.Printf("Warning: entity extraction failed for chunk %s: %v", c.ID, err)
				return
			}

			if len(entities) == 0 {
				return
			}

			// Phase 2: Extract relations between known entities
			triples, err := llm.ExtractRelationsFromChunk(p.llmClient, c.Text, entities, c.ID)
			if err != nil {
				log.Printf("Warning: relation extraction failed for chunk %s: %v", c.ID, err)
			}

			mu.Lock()
			allEntities = append(allEntities, entities...)
			if triples != nil {
				allTriples = append(allTriples, triples...)
			}
			mu.Unlock()
		}(chunk)
	}

	wg.Wait()
	return allEntities, allTriples
}

// buildEntityMap creates a name→type lookup from all extracted entities.
// When the same entity appears with different types across chunks,
// the most common type wins.
func buildEntityMap(entities []models.Entity) map[string]string {
	// Count type votes per entity
	votes := map[string]map[string]int{} // name -> type -> count
	for _, e := range entities {
		if e.Name == "" || e.Type == "" {
			continue
		}
		if votes[e.Name] == nil {
			votes[e.Name] = map[string]int{}
		}
		votes[e.Name][e.Type]++
	}

	// Pick the most common type for each entity
	result := map[string]string{}
	for name, typeCounts := range votes {
		bestType := ""
		bestCount := 0
		for t, count := range typeCounts {
			if count > bestCount {
				bestType = t
				bestCount = count
			}
		}
		result[name] = bestType
	}

	return result
}

// standardizeEntities collects all unique node names and asks the LLM to deduplicate them.
func (p *Pipeline) standardizeEntities(triples []models.Triple) ([]models.Triple, error) {
	nameSet := map[string]bool{}
	for _, t := range triples {
		nameSet[t.Node1] = true
		nameSet[t.Node2] = true
	}

	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}

	if len(names) < 3 {
		return triples, nil
	}

	mappings, err := llm.StandardizeEntities(p.llmClient, names)
	if err != nil {
		return triples, err
	}

	if len(mappings) > 0 {
		log.Printf("Standardized %d entity name variants", len(mappings))
		triples = graph.ApplyStandardization(triples, mappings)
	}

	return triples, nil
}

// storeGraph writes all merged edges to FalkorDB.
func (p *Pipeline) storeGraph(edges []models.EdgeRecord) error {
	for i, edge := range edges {
		if err := p.store.CreateEdge(edge); err != nil {
			log.Printf("Warning: failed to store edge %d (%s -> %s): %v", i, edge.Node1, edge.Node2, err)
		}
	}
	return nil
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
