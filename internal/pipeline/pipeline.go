package pipeline

import (
	"fmt"
	"log"
	"strings"
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

	// Phase 3: Filter stopword/generic/noise nodes from both entities and triples
	beforeE := len(allEntities)
	allEntities = graph.FilterEntities(allEntities)
	before := len(allTriples)
	allTriples = graph.FilterTriples(allTriples)
	log.Printf("Filtered %d noisy entities, %d noisy triples (%d entities, %d triples remaining)",
		beforeE-len(allEntities), before-len(allTriples), len(allEntities), len(allTriples))

	// Phase 4: Standardize entity names FIRST (before validation)
	log.Println("Standardizing entities...")
	allTriples, allEntities, err := p.standardizeEntities(allTriples, allEntities)
	if err != nil {
		log.Printf("Warning: standardization failed, continuing with raw names: %v", err)
	}

	// Phase 5: Build entity type map, correct types deterministically, then LLM-classify ambiguous ones
	log.Println("Correcting entity types...")
	entityMap := buildEntityMap(allEntities)
	locked := graph.CorrectEntityTypes(entityMap)

	// Collect entities not locked by deterministic rules — ask the LLM to classify them
	ambiguous := map[string]string{}
	for name, typ := range entityMap {
		if !locked[name] {
			ambiguous[name] = typ
		}
	}
	if len(ambiguous) > 0 {
		log.Printf("Classifying %d ambiguous entities via LLM...", len(ambiguous))
		classifications, err := llm.ClassifyEntityTypes(p.llmClient, ambiguous)
		if err != nil {
			log.Printf("Warning: LLM classification failed, continuing with existing types: %v", err)
		} else if classifications != nil {
			classified := 0
			for name, newType := range classifications {
				if _, ok := entityMap[name]; ok && newType != "" {
					entityMap[name] = newType
					locked[name] = true
					classified++
				}
			}
			log.Printf("LLM classified %d entities", classified)
		}
	}

	// Phase 5b: Validate relations using the corrected entity types
	log.Println("Validating relations and directions...")
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap)

	// Phase 5c: Propagate types from VALIDATED edges for any remaining untyped entities
	graph.PropagateTypesFromTriples(allTriples, entityMap, locked)

	// Phase 6: Merge duplicate edges (directed — preserves edge direction)
	log.Println("Merging edges...")
	mergedEdges := graph.MergeEdges(allTriples, nil, p.cfg.SemanticWeight)
	log.Printf("Total merged edges: %d", len(mergedEdges))

	// Phase 7: Merge and store entities with properties
	log.Println("Storing entities with properties...")
	mergedEntities := mergeEntities(allEntities, entityMap)
	if err := p.storeEntities(mergedEntities); err != nil {
		log.Printf("Warning: entity storage failed: %v", err)
	}
	log.Printf("Stored %d unique entities with properties", len(mergedEntities))

	// Phase 8: Store edges in FalkorDB
	log.Println("Storing edges in FalkorDB...")
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
// Returns updated triples AND entities with canonical names applied.
func (p *Pipeline) standardizeEntities(triples []models.Triple, entities []models.Entity) ([]models.Triple, []models.Entity, error) {
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
		return triples, entities, nil
	}

	mappings, err := llm.StandardizeEntities(p.llmClient, names)
	if err != nil {
		return triples, entities, err
	}

	if len(mappings) > 0 {
		log.Printf("Standardized %d entity name variants", len(mappings))
		triples = graph.ApplyStandardization(triples, mappings)
		entities = graph.ApplyStandardizationToEntities(entities, mappings)
	}

	return triples, entities, nil
}

// mergeEntities deduplicates entities by name, merging properties and using the validated type.
func mergeEntities(entities []models.Entity, entityMap map[string]string) []models.Entity {
	merged := map[string]*models.Entity{}

	for _, e := range entities {
		if e.Name == "" {
			continue
		}
		existing, ok := merged[e.Name]
		if !ok {
			// Use the validated type from entityMap
			typ := entityMap[e.Name]
			if typ == "" {
				typ = e.Type
			}
			props := map[string]interface{}{}
			for k, v := range e.Properties {
				props[k] = v
			}
			merged[e.Name] = &models.Entity{
				Name:       e.Name,
				Type:       typ,
				Properties: props,
			}
			continue
		}
		// Merge properties: later values overwrite, but descriptions get concatenated
		for k, v := range e.Properties {
			if k == "description" {
				if existDesc, ok := existing.Properties["description"].(string); ok {
					newDesc, _ := v.(string)
					if newDesc != "" && !strings.Contains(existDesc, newDesc) {
						existing.Properties["description"] = existDesc + " " + newDesc
					}
				} else {
					existing.Properties[k] = v
				}
			} else {
				existing.Properties[k] = v
			}
		}
	}

	result := make([]models.Entity, 0, len(merged))
	for _, e := range merged {
		result = append(result, *e)
	}
	return result
}

// storeEntities writes all merged entities to FalkorDB with their properties.
func (p *Pipeline) storeEntities(entities []models.Entity) error {
	for _, entity := range entities {
		if err := p.store.CreateEntity(entity); err != nil {
			log.Printf("Warning: failed to store entity '%s': %v", entity.Name, err)
		}
	}
	return nil
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
