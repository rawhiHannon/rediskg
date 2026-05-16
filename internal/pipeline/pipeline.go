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

func (p *Pipeline) ingestDocuments(docs []*models.Document) error {
	// Phase 0: Initialize schema with base types + load persisted schema
	log.Println("Initializing schema...")
	p.schema.InitWithBaseTypes()
	p.loadSchema()
	log.Printf("Schema loaded: %d entity types, %d relation types",
		len(p.schema.EntityTypeNames()), len(p.schema.RelationTypeNames()))

	// Phase 1: Chunk documents
	log.Println("Chunking documents...")
	chunks := chunker.ChunkDocuments(docs, p.cfg.ChunkSize, p.cfg.ChunkOverlap)
	log.Printf("Created %d chunks", len(chunks))

	// Phase 2: Extract entities and relations (concurrent, evidence-based)
	log.Println("Extracting entities and relations...")
	allEntities, allTriples := p.extractAll(chunks)
	log.Printf("Extracted %d entities, %d triples", len(allEntities), len(allTriples))

	// Phase 3: Filter stopword/noise
	allEntities = graph.FilterEntities(allEntities)
	allTriples = graph.FilterTriples(allTriples)
	log.Printf("After noise filter: %d entities, %d triples", len(allEntities), len(allTriples))

	// Phase 4: Standardize entity names (deduplicate variants)
	log.Println("Standardizing entity names...")
	allTriples, allEntities, err := p.standardizeEntities(allTriples, allEntities)
	if err != nil {
		log.Printf("Warning: standardization failed, continuing with raw names: %v", err)
	}

	// Phase 5: Build entity profiles (global entity registry)
	log.Println("Building entity profiles...")
	profiles := p.buildEntityProfiles(allEntities, allTriples)
	log.Printf("Built %d entity profiles", len(profiles))

	// Phase 6: Global schema normalization (the critical governance pass)
	// Collects ALL candidate types + relations, sends to LLM as a batch for canonicalization
	log.Println("Running global schema normalization...")
	p.globalSchemaNormalization(allEntities, allTriples)

	// Phase 7: Schema rewrite — deterministically apply normalization to entities
	log.Println("Rewriting entities with normalized schema...")
	allEntities = p.schema.RewriteEntities(allEntities)

	// Build the authoritative entity type map from rewritten entities
	entityMap := buildEntityMap(allEntities)

	// Phase 8: Schema rewrite — deterministically apply normalization to triples
	// Resolves aliases, flips inverses, validates base-type constraints, rejects bad triples
	log.Println("Rewriting triples with normalized schema...")
	allTriples = p.schema.RewriteTriples(allTriples, entityMap)

	// Phase 9: Additional schema-based validation (dedup symmetric, etc.)
	log.Println("Validating triples against schema...")
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap, p.schema)

	// Phase 9b: Propagate types from validated relations
	graph.PropagateTypesFromTriples(allTriples, entityMap, p.schema)

	// Phase 10: Rich-context verification (LLM verifies with profiles + evidence)
	log.Println("Verifying triples with evidence...")
	allTriples = p.verifyTriplesRich(allTriples, profiles)

	// Phase 10b: Conflict resolution — remove weaker facts when stronger exist
	log.Println("Resolving conflicting/redundant triples...")
	allTriples = graph.ResolveConflicts(allTriples)

	// Phase 11: Merge entities into final form
	mergedEntities := mergeEntities(allEntities, entityMap)
	log.Printf("Merged to %d unique entities", len(mergedEntities))

	// Phase 12: Merge duplicate edges
	log.Println("Merging edges...")
	mergedEdges := graph.MergeEdges(allTriples, nil, p.cfg.SemanticWeight)
	log.Printf("Total merged edges: %d", len(mergedEdges))

	// Phase 13: Store entities
	log.Println("Storing entities...")
	if err := p.storeEntities(mergedEntities); err != nil {
		log.Printf("Warning: entity storage failed: %v", err)
	}

	// Phase 14: Store edges
	log.Println("Storing edges...")
	if err := p.storeGraph(mergedEdges); err != nil {
		return fmt.Errorf("failed to store graph: %w", err)
	}

	// Phase 15: Persist schema
	log.Println("Persisting schema...")
	if err := p.saveSchema(); err != nil {
		log.Printf("Warning: schema persistence failed: %v", err)
	}

	// Phase 16: Generate embeddings
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

// extractAll runs extraction on all chunks concurrently.
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
	baseSummary := p.schema.BaseTypeSummary()
	relationSummary := p.schema.RelationTypeSummary()

	for _, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}

		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			entities, err := llm.ExtractEntitiesFromChunk(p.llmClient, c.Text, typeSummary, baseSummary)
			if err != nil {
				log.Printf("Warning: entity extraction failed for chunk %s: %v", c.ID, err)
				return
			}
			if len(entities) == 0 {
				return
			}

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

// buildEntityProfiles collects all mentions, candidate types, and evidence for each entity.
func (p *Pipeline) buildEntityProfiles(entities []models.Entity, triples []models.Triple) map[string]*models.EntityProfile {
	profiles := map[string]*models.EntityProfile{}

	for _, e := range entities {
		if e.Name == "" {
			continue
		}
		prof, ok := profiles[e.Name]
		if !ok {
			prof = &models.EntityProfile{Name: e.Name}
			profiles[e.Name] = prof
		}

		// Collect candidate types
		if e.Type != "" {
			found := false
			for _, ct := range prof.CandidateTypes {
				if ct == e.Type {
					found = true
					break
				}
			}
			if !found {
				prof.CandidateTypes = append(prof.CandidateTypes, e.Type)
			}
		}
		if e.BaseType != "" && e.BaseType != e.Type {
			found := false
			for _, ct := range prof.CandidateTypes {
				if ct == e.BaseType {
					found = true
					break
				}
			}
			if !found {
				prof.CandidateTypes = append(prof.CandidateTypes, e.BaseType)
			}
		}

		// Collect evidence as mentions
		if evidence, ok := e.Properties["evidence"].(string); ok && evidence != "" {
			prof.Mentions = append(prof.Mentions, evidence)
		}

		// Merge description
		if desc, ok := e.Properties["description"].(string); ok && desc != "" {
			if prof.Description == "" {
				prof.Description = desc
			} else if !strings.Contains(prof.Description, desc) {
				prof.Description += " " + desc
			}
		}
	}

	// Also collect evidence from triples
	for _, t := range triples {
		if t.Evidence == "" {
			continue
		}
		if prof, ok := profiles[t.Node1]; ok {
			prof.Mentions = append(prof.Mentions, t.Evidence)
		}
		if prof, ok := profiles[t.Node2]; ok {
			if t.Node2 != t.Node1 { // avoid double-adding
				prof.Mentions = append(prof.Mentions, t.Evidence)
			}
		}
	}

	// Deduplicate mentions
	for _, prof := range profiles {
		prof.Mentions = deduplicateStrings(prof.Mentions)
		// Cap at 10 to keep profiles reasonable
		if len(prof.Mentions) > 10 {
			prof.Mentions = prof.Mentions[:10]
		}
	}

	return profiles
}

// globalSchemaNormalization collects all candidate types and relations from extracted data,
// sends them to the LLM as a batch for canonicalization, then applies the result to the schema.
// This is the key governance step: the LLM acts as a schema compiler, not an extractor.
func (p *Pipeline) globalSchemaNormalization(entities []models.Entity, triples []models.Triple) {
	// Collect candidates with counts and examples
	typeCandidates := schema.CollectTypeCandidates(entities, 3)
	relationCandidates := schema.CollectRelationCandidates(triples, 3)

	if len(typeCandidates) == 0 && len(relationCandidates) == 0 {
		return
	}

	log.Printf("Schema normalization input: %d type candidates, %d relation candidates",
		len(typeCandidates), len(relationCandidates))

	baseSummary := p.schema.BaseTypeSummary()

	normResult, err := llm.NormalizeSchema(p.llmClient, baseSummary, typeCandidates, relationCandidates)
	if err != nil {
		log.Printf("Warning: global schema normalization failed: %v", err)
		// Fallback: run heuristic-only governance
		p.fallbackHeuristicGovernance(entities, triples)
		return
	}

	// Apply the normalization result to the schema (registers types, aliases, relation rules)
	p.schema.ApplyNormalization(normResult)
}

// fallbackHeuristicGovernance applies basic heuristic governance when LLM normalization fails.
func (p *Pipeline) fallbackHeuristicGovernance(entities []models.Entity, triples []models.Triple) {
	accepted, _ := p.schema.EvolveFromEntities(entities)
	relAccepted, _ := p.schema.EvolveFromTriples(triples)
	log.Printf("Fallback governance: %d type aliases, %d relation aliases auto-accepted", accepted, relAccepted)
}

// buildEntityMap creates a name→type lookup from all extracted entities (majority vote).
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

// verifyTriplesRich uses evidence + entity profiles for LLM verification.
// Processes triples in batches to handle large graphs without timeout.
func (p *Pipeline) verifyTriplesRich(triples []models.Triple, profiles map[string]*models.EntityProfile) []models.Triple {
	if len(triples) == 0 {
		return triples
	}

	// Build relation schema summary
	relationSchema := map[string]string{}
	for _, name := range p.schema.RelationTypeNames() {
		rt := p.schema.GetRelationType(name)
		desc := rt.Description
		if desc == "" {
			desc = fmt.Sprintf("src=[%s] → tgt=[%s]",
				strings.Join(rt.SourceTypes, ","), strings.Join(rt.TargetTypes, ","))
		}
		relationSchema[name] = desc
	}

	// Process in batches of 50
	const batchSize = 50
	var allRejectKeys []string
	allModifications := map[string]models.Triple{}

	for i := 0; i < len(triples); i += batchSize {
		end := i + batchSize
		if end > len(triples) {
			end = len(triples)
		}
		batch := triples[i:end]

		_, rejectKeys, modifications, err := llm.VerifyTriplesRich(p.llmClient, batch, profiles, relationSchema)
		if err != nil {
			log.Printf("Warning: rich verification batch %d-%d failed: %v", i, end, err)
			continue
		}

		allRejectKeys = append(allRejectKeys, rejectKeys...)
		for k, v := range modifications {
			allModifications[k] = v
		}
	}

	if len(allRejectKeys) == 0 && len(allModifications) == 0 {
		log.Println("Rich verification: all triples accepted")
		return triples
	}

	log.Printf("Rich verification: rejecting %d, modifying %d triples", len(allRejectKeys), len(allModifications))
	return graph.ApplyVerification(triples, allRejectKeys, allModifications)
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
				if k != "evidence" { // don't store raw evidence in entity properties
					props[k] = v
				}
			}
			merged[e.Name] = &models.Entity{
				Name:       e.Name,
				Type:       typ,
				BaseType:   e.BaseType,
				DomainType: e.DomainType,
				Properties: props,
			}
			continue
		}
		for k, v := range e.Properties {
			if k == "evidence" {
				continue
			}
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

// loadSchema loads any previously-persisted schema from FalkorDB.
func (p *Pipeline) loadSchema() {
	etRows, err := p.store.LoadSchemaEntityTypes()
	if err != nil {
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
