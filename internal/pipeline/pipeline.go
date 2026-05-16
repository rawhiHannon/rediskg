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
	"rediskg/internal/schema"
	"rediskg/internal/store"
	"rediskg/pkg/config"
	"rediskg/pkg/models"
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
	// Phase 0: Load existing schema from FalkorDB (if any)
	log.Println("Loading schema...")
	p.loadSchema()
	log.Printf("Schema loaded: %d entity types, %d relation types",
		len(p.schema.EntityTypeNames()), len(p.schema.RelationTypeNames()))

	// Phase 1: Chunk
	log.Println("Chunking documents...")
	chunks := chunker.ChunkDocuments(docs, p.cfg.ChunkSize, p.cfg.ChunkOverlap)
	log.Printf("Created %d chunks", len(chunks))

	// Phase 2: Extract entities and relations (concurrent, schema-aware)
	log.Println("Extracting entities and relations...")
	allEntities, allTriples := p.extractAll(chunks)
	log.Printf("Extracted %d entities, %d triples", len(allEntities), len(allTriples))

	// Phase 3: Filter stopword/noise nodes
	beforeE := len(allEntities)
	allEntities = graph.FilterEntities(allEntities)
	before := len(allTriples)
	allTriples = graph.FilterTriples(allTriples)
	log.Printf("Filtered %d noisy entities, %d noisy triples (%d entities, %d triples remaining)",
		beforeE-len(allEntities), before-len(allTriples), len(allEntities), len(allTriples))

	// Phase 4: Schema evolution — learn new types from extracted data
	log.Println("Evolving schema from extracted data...")
	p.schema.EvolveFromEntities(allEntities)
	p.schema.EvolveFromTriples(allTriples)

	// Phase 4b: Ask LLM to refine schema (add descriptions, constraints, merge duplicates)
	log.Println("Refining schema with LLM...")
	p.refineSchema()

	// Phase 5: Standardize entity names
	log.Println("Standardizing entities...")
	allTriples, allEntities, err := p.standardizeEntities(allTriples, allEntities)
	if err != nil {
		log.Printf("Warning: standardization failed, continuing with raw names: %v", err)
	}

	// Phase 6: Classify entity types using schema
	log.Println("Classifying entity types...")
	entityMap := buildEntityMap(allEntities)
	p.classifyEntityTypes(entityMap, allTriples)

	// Phase 7: Validate triples using schema
	log.Println("Validating relations against schema...")
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap, p.schema)

	// Phase 7b: LLM-based triple validation for complex cases
	if len(allTriples) > 0 && len(allTriples) <= 200 {
		log.Println("LLM triple validation...")
		allTriples = p.llmValidateTriples(allTriples)
	}

	// Phase 7c: Propagate types from validated edges
	graph.PropagateTypesFromTriples(allTriples, entityMap, p.schema)

	// Phase 8: Merge entities
	mergedEntities := mergeEntities(allEntities, entityMap)

	// Phase 9: LLM verification — review the full graph
	log.Println("Verifying graph with LLM...")
	allTriples = p.verifyGraph(allTriples, mergedEntities)

	// Phase 10: Merge duplicate edges
	log.Println("Merging edges...")
	mergedEdges := graph.MergeEdges(allTriples, nil, p.cfg.SemanticWeight)
	log.Printf("Total merged edges: %d", len(mergedEdges))

	// Phase 11: Store entities
	log.Println("Storing entities with properties...")
	if err := p.storeEntities(mergedEntities); err != nil {
		log.Printf("Warning: entity storage failed: %v", err)
	}
	log.Printf("Stored %d unique entities with properties", len(mergedEntities))

	// Phase 12: Store edges
	log.Println("Storing edges in FalkorDB...")
	if err := p.storeGraph(mergedEdges); err != nil {
		return fmt.Errorf("failed to store graph: %w", err)
	}

	// Phase 13: Persist schema
	log.Println("Persisting schema...")
	if err := p.saveSchema(); err != nil {
		log.Printf("Warning: schema persistence failed: %v", err)
	}

	// Phase 14: Generate embeddings
	log.Println("Generating entity embeddings...")
	if err := p.generateEmbeddings(); err != nil {
		log.Printf("Warning: embedding generation failed: %v", err)
	}

	stats, _ := p.store.GetGraphStats()
	log.Printf("Done! Graph has %d nodes and %d edges", stats["nodes"], stats["edges"])
	log.Printf("Schema: %d entity types, %d relation types",
		len(p.schema.EntityTypeNames()), len(p.schema.RelationTypeNames()))

	return nil
}

// loadSchema loads any previously-persisted schema from FalkorDB.
func (p *Pipeline) loadSchema() {
	etRows, err := p.store.LoadSchemaEntityTypes()
	if err != nil {
		// "empty key" means graph doesn't exist yet — expected on first run
		if !strings.Contains(err.Error(), "empty key") {
			log.Printf("Warning: failed to load entity type schema: %v", err)
		}
		return
	}
	for _, row := range etRows {
		p.schema.AddEntityType(schema.EntityType{
			Name:        row["name"],
			Description: row["description"],
			ParentType:  row["parent_type"],
		})
	}

	rtRows, err := p.store.LoadSchemaRelationTypes()
	if err != nil {
		log.Printf("Warning: failed to load relation type schema: %v", err)
		return
	}
	for _, row := range rtRows {
		srcTypes := splitCSV(row["source_types"])
		tgtTypes := splitCSV(row["target_types"])
		symmetric := row["symmetric"] == "true" || row["symmetric"] == "1"
		p.schema.AddRelationType(schema.RelationType{
			Name:        row["name"],
			Description: row["description"],
			SourceTypes: srcTypes,
			TargetTypes: tgtTypes,
			Symmetric:   symmetric,
		})
	}
}

// saveSchema persists the current schema to FalkorDB.
func (p *Pipeline) saveSchema() error {
	entityTypes := map[string]struct{ Desc, Parent string }{}
	for _, name := range p.schema.EntityTypeNames() {
		et := p.schema.GetEntityType(name)
		entityTypes[name] = struct{ Desc, Parent string }{et.Description, et.ParentType}
	}

	relationTypes := map[string]struct {
		Desc        string
		SourceTypes []string
		TargetTypes []string
		Symmetric   bool
	}{}
	for _, name := range p.schema.RelationTypeNames() {
		rt := p.schema.GetRelationType(name)
		relationTypes[name] = struct {
			Desc        string
			SourceTypes []string
			TargetTypes []string
			Symmetric   bool
		}{rt.Description, rt.SourceTypes, rt.TargetTypes, rt.Symmetric}
	}

	return p.store.SaveSchema(entityTypes, relationTypes)
}

// refineSchema asks the LLM to improve the discovered schema.
func (p *Pipeline) refineSchema() {
	entityTypes, relationTypes, mergeMap, err := llm.RefineSchema(p.llmClient, p.schema)
	if err != nil {
		log.Printf("Warning: schema refinement failed: %v", err)
		return
	}

	if mergeMap != nil && len(mergeMap) > 0 {
		log.Printf("Schema refinement: merging %d duplicate types", len(mergeMap))
		// Apply type merges — update entity types in schema
		for dup, canonical := range mergeMap {
			// Remove the duplicate, keep the canonical
			p.schema.AddEntityType(schema.EntityType{
				Name:       canonical,
				ParentType: "", // will be set by the refined definitions
			})
			_ = dup // the duplicate simply won't be re-added
		}
	}

	p.schema.MergeSchemaDefinitions(entityTypes, relationTypes)
	log.Printf("Schema refined: %d entity types, %d relation types",
		len(p.schema.EntityTypeNames()), len(p.schema.RelationTypeNames()))
}

// classifyEntityTypes uses the LLM + schema to classify all entities.
func (p *Pipeline) classifyEntityTypes(entityMap map[string]string, triples []models.Triple) {
	// Find entities with missing or empty types
	untyped := map[string]string{}
	for name, typ := range entityMap {
		if typ == "" {
			untyped[name] = ""
		}
	}

	if len(untyped) == 0 {
		return
	}

	log.Printf("Classifying %d untyped entities via LLM...", len(untyped))
	classifications, newTypes, err := llm.ClassifyEntitiesWithSchema(p.llmClient, p.schema, untyped, triples)
	if err != nil {
		log.Printf("Warning: entity classification failed: %v", err)
		return
	}

	// Register any new types discovered during classification
	if newTypes != nil {
		for _, nt := range newTypes {
			p.schema.AddEntityType(nt)
		}
		log.Printf("Classification discovered %d new entity types", len(newTypes))
	}

	// Apply classifications
	classified := 0
	if classifications != nil {
		for name, newType := range classifications {
			if _, ok := entityMap[name]; ok && newType != "" {
				entityMap[name] = strings.ToLower(newType)
				classified++
			}
		}
	}
	log.Printf("LLM classified %d entities", classified)
}

// llmValidateTriples sends triples to LLM for schema-based validation.
func (p *Pipeline) llmValidateTriples(triples []models.Triple) []models.Triple {
	result, err := llm.ValidateTriplesWithSchema(p.llmClient, p.schema, triples)
	if err != nil || result == nil {
		log.Printf("Warning: LLM triple validation failed: %v", err)
		return triples
	}

	// Register any new relation types
	for _, nr := range result.NewRelations {
		p.schema.AddRelationType(nr)
	}

	// Build maps for applying changes
	flips := map[string]bool{}
	removes := map[string]bool{}
	normalizations := map[string]string{}

	for _, f := range result.Flip {
		key := strings.ToLower(f.Node1) + "|" + strings.ToUpper(f.Edge) + "|" + strings.ToLower(f.Node2)
		flips[key] = true
	}
	for _, r := range result.Remove {
		key := strings.ToLower(r.Node1) + "|" + strings.ToUpper(r.Edge) + "|" + strings.ToLower(r.Node2)
		removes[key] = true
	}
	for _, n := range result.Normalize {
		key := strings.ToLower(n.Node1) + "|" + strings.ToUpper(n.OldEdge) + "|" + strings.ToLower(n.Node2)
		normalizations[key] = strings.ToUpper(n.NewEdge)
	}

	return graph.ApplyLLMValidation(triples, flips, removes, normalizations)
}

// extractAll runs extraction on all chunks concurrently, using schema context.
func (p *Pipeline) extractAll(chunks []*models.Chunk) ([]models.Entity, []models.Triple) {
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		allEntities []models.Entity
		allTriples  []models.Triple
		sem         = make(chan struct{}, p.cfg.Workers)
	)

	// Get current schema summaries for prompts
	typeSummary := p.schema.EntityTypeSummary()
	relationSummary := p.schema.RelationTypeSummary()

	for _, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}

		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			// Phase 1: Extract entities (schema-aware)
			entities, err := llm.ExtractEntitiesFromChunk(p.llmClient, c.Text, typeSummary)
			if err != nil {
				log.Printf("Warning: entity extraction failed for chunk %s: %v", c.ID, err)
				return
			}

			if len(entities) == 0 {
				return
			}

			// Phase 2: Extract relations (schema-aware)
			triples, err := llm.ExtractRelationsFromChunk(p.llmClient, c.Text, entities, c.ID, relationSummary)
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
func buildEntityMap(entities []models.Entity) map[string]string {
	votes := map[string]map[string]int{}
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

// standardizeEntities collects all unique entities and asks the LLM to deduplicate them.
func (p *Pipeline) standardizeEntities(triples []models.Triple, entities []models.Entity) ([]models.Triple, []models.Entity, error) {
	uniqueEntities := map[string]models.Entity{}
	for _, e := range entities {
		if e.Name == "" {
			continue
		}
		existing, ok := uniqueEntities[e.Name]
		if !ok {
			uniqueEntities[e.Name] = e
			continue
		}
		existDesc, _ := existing.Properties["description"].(string)
		newDesc, _ := e.Properties["description"].(string)
		if len(newDesc) > len(existDesc) {
			uniqueEntities[e.Name] = e
		}
	}

	dedupedEntities := make([]models.Entity, 0, len(uniqueEntities))
	for _, e := range uniqueEntities {
		dedupedEntities = append(dedupedEntities, e)
	}

	if len(dedupedEntities) < 3 {
		return triples, entities, nil
	}

	mappings, err := llm.StandardizeEntities(p.llmClient, dedupedEntities)
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

// verifyGraph sends the full triple list to the LLM for verification and applies corrections.
func (p *Pipeline) verifyGraph(triples []models.Triple, entities []models.Entity) []models.Triple {
	removals, modifications, err := llm.VerifyGraph(p.llmClient, triples, entities)
	if err != nil {
		log.Printf("Warning: graph verification failed, continuing unverified: %v", err)
		return triples
	}

	if len(removals) == 0 && len(modifications) == 0 {
		log.Println("LLM verification: graph looks clean")
		return triples
	}

	removeKeys := make([]string, 0, len(removals))
	for _, r := range removals {
		key := strings.ToLower(r.Node1) + "|" + strings.ToUpper(r.Edge) + "|" + strings.ToLower(r.Node2)
		removeKeys = append(removeKeys, key)
		log.Printf("  LLM remove: %s -[%s]-> %s (%s)", r.Node1, r.Edge, r.Node2, r.Reason)
	}

	modMap := map[string]models.Triple{}
	for _, m := range modifications {
		key := strings.ToLower(m.Node1) + "|" + strings.ToUpper(m.Edge) + "|" + strings.ToLower(m.Node2)
		newTriple := models.Triple{
			Node1: strings.ToLower(m.Node1),
			Node2: strings.ToLower(m.Node2),
			Edge:  strings.ToUpper(m.Edge),
		}
		if m.NewNode1 != "" {
			newTriple.Node1 = strings.ToLower(m.NewNode1)
		}
		if m.NewNode2 != "" {
			newTriple.Node2 = strings.ToLower(m.NewNode2)
		}
		if m.NewEdge != "" {
			newTriple.Edge = strings.ToUpper(m.NewEdge)
		}
		modMap[key] = newTriple
		log.Printf("  LLM modify: %s -[%s]-> %s → %s -[%s]-> %s (%s)",
			m.Node1, m.Edge, m.Node2, newTriple.Node1, newTriple.Edge, newTriple.Node2, m.Reason)
	}

	return graph.ApplyVerification(triples, removeKeys, modMap)
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
