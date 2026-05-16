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
	// Phase 0: Initialize schema with base types + optionally load persisted schema
	log.Println("Initializing schema...")
	p.schema.InitWithBaseTypes()
	if p.cfg.PersistSchema && !p.cfg.ResetSchemaOnIngest {
		p.loadSchema()
	}
	log.Printf("Schema initialized: %d entity types, %d relation types",
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

	// Phase 6b: Collect and apply alias endpoint mappings from ALIAS_OF triples
	// ALIAS_OF edges contain alias→canonical info that must be applied before removal
	aliasMappings := collectAliasEndpoints(allTriples, allEntities)
	if len(aliasMappings) > 0 {
		log.Printf("Applying %d alias endpoint rewrites from ALIAS_OF triples", len(aliasMappings))
		allTriples = graph.ApplyStandardization(allTriples, aliasMappings)
		allEntities = graph.ApplyStandardizationToEntities(allEntities, aliasMappings)
	}

	// Phase 7: Schema rewrite — deterministically apply normalization to entities
	log.Println("Rewriting entities with normalized schema...")
	allEntities = p.schema.RewriteEntities(allEntities)

	// Build the AUTHORITATIVE entity type map from rewritten entities.
	// Once built, this map is frozen — triples cannot mutate it.
	entityTypeMap := buildEntityTypeMap(allEntities)
	entityMap := entityTypeMapToFlat(entityTypeMap)

	// Phase 8: Schema rewrite — deterministically apply normalization to triples
	// Resolves aliases, flips inverses, validates base-type constraints, rejects bad triples
	log.Println("Rewriting triples with normalized schema...")
	allTriples = p.schema.RewriteTriples(allTriples, entityMap)

	// Phase 9: Additional schema-based validation (dedup symmetric, etc.)
	log.Println("Validating triples against schema...")
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap, p.schema)

	// Phase 10: Rich-context verification (LLM verifies with profiles + evidence)
	log.Println("Verifying triples with evidence...")
	allTriples = p.verifyTriplesRich(allTriples, profiles)

	// Phase 10b: Re-run schema compiler after LLM verification
	// Verification can modify triples — revalidate against schema
	log.Println("Re-validating triples after verification...")
	allTriples = p.schema.RewriteTriples(allTriples, entityMap)
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap, p.schema)

	// Phase 10c: Contextual enforcement — status-aware and role-aware checks
	log.Println("Applying contextual enforcement (planned entities, deputy roles)...")
	allTriples = applyContextualEnforcement(allTriples, profiles, entityTypeMap)

	// Phase 10d: Endpoint variant dedup (domain-agnostic pluralization merging)
	allTriples = graph.DeduplicateEndpointVariants(allTriples)

	// Phase 10e: Re-run schema validation after dedup (dedup can create new type mismatches)
	allTriples = graph.ValidateAndNormalizeTriples(allTriples, entityMap, p.schema)

	// Phase 10f: Conflict resolution — remove weaker facts when stronger exist
	log.Println("Resolving conflicting/redundant triples...")
	allTriples = graph.ResolveConflicts(allTriples)

	// Phase 10g: Final canonical endpoint rewrite — guarantee all endpoints use canonical names
	if len(aliasMappings) > 0 {
		allTriples = graph.ApplyStandardization(allTriples, aliasMappings)
	}

	// Phase 11: Build final entity list from compiled graph endpoints only
	mergedEntities := buildEntitiesFromGraph(allTriples, entityTypeMap)
	log.Printf("Final entity set: %d entities (from graph endpoints)", len(mergedEntities))

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

	// Phase 15: Persist schema (if enabled)
	if p.cfg.PersistSchema {
		log.Println("Persisting schema...")
		if err := p.saveSchema(); err != nil {
			log.Printf("Warning: schema persistence failed: %v", err)
		}
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

// EntityTypeInfo holds the resolved type information for an entity after schema normalization.
type EntityTypeInfo struct {
	Type       string // resolved type (may be base or domain type)
	BaseType   string // universal scaffold type (person, organization, etc.)
	DomainType string // domain-specific subtype (clinic_branch, etc.)
}

// buildEntityTypeMap creates a name→EntityTypeInfo lookup from rewritten entities (majority vote).
// This is the AUTHORITATIVE type source — once built, it should not be mutated by triples.
func buildEntityTypeMap(entities []models.Entity) map[string]*EntityTypeInfo {
	type voteEntry struct {
		typ        string
		baseType   string
		domainType string
	}
	votes := map[string][]voteEntry{}
	for _, e := range entities {
		if e.Name == "" || e.Type == "" {
			continue
		}
		votes[e.Name] = append(votes[e.Name], voteEntry{e.Type, e.BaseType, e.DomainType})
	}

	result := map[string]*EntityTypeInfo{}
	for name, entries := range votes {
		// Majority vote on type
		typeCounts := map[string]int{}
		for _, v := range entries {
			typeCounts[v.typ]++
		}
		bestType := ""
		bestCount := 0
		for t, count := range typeCounts {
			if count > bestCount {
				bestType = t
				bestCount = count
			}
		}

		// Use the first entry with that type for base/domain info
		info := &EntityTypeInfo{Type: bestType}
		for _, v := range entries {
			if v.typ == bestType {
				if v.baseType != "" {
					info.BaseType = v.baseType
				}
				if v.domainType != "" {
					info.DomainType = v.domainType
				}
				break
			}
		}
		// If base type is still empty, fall back to the type itself if it's a base type
		if info.BaseType == "" {
			info.BaseType = info.Type
		}
		result[name] = info
	}
	return result
}

// entityTypeMapToFlat converts rich EntityTypeInfo map to flat map[string]string for
// backward-compatible functions that only need the type name.
func entityTypeMapToFlat(infoMap map[string]*EntityTypeInfo) map[string]string {
	result := make(map[string]string, len(infoMap))
	for name, info := range infoMap {
		result[name] = info.Type
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


// buildEntitiesFromGraph creates the final entity list from compiled graph endpoints only.
// Only entities that actually appear as triple endpoints make it into the final graph.
func buildEntitiesFromGraph(triples []models.Triple, entityTypeMap map[string]*EntityTypeInfo) []models.Entity {
	// Collect all unique endpoints from the compiled triples
	endpoints := map[string]bool{}
	for _, t := range triples {
		if t.Node1 != "" {
			endpoints[t.Node1] = true
		}
		if t.Node2 != "" {
			endpoints[t.Node2] = true
		}
	}

	// Build entities with authoritative type info
	result := make([]models.Entity, 0, len(endpoints))
	for name := range endpoints {
		e := models.Entity{Name: name}
		if info, ok := entityTypeMap[name]; ok {
			e.Type = info.Type
			e.BaseType = info.BaseType
			e.DomainType = info.DomainType
		}
		result = append(result, e)
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

// applyContextualEnforcement applies status-aware and role-aware checks using entity profiles.
// - Planned/future entities cannot have active relations (OPERATES, OFFERS, HAS_CLINICIAN, WORKS_AT)
// - Entities with deputy manager role cannot have MANAGES relation (convert or reject)
func applyContextualEnforcement(triples []models.Triple, profiles map[string]*models.EntityProfile, entityTypeMap map[string]*EntityTypeInfo) []models.Triple {
	// Detect planned entities from profiles (mentions containing "planned", "future", "upcoming", "expected")
	plannedEntities := map[string]bool{}
	for name, prof := range profiles {
		if isPlannedEntity(prof) {
			plannedEntities[name] = true
		}
	}

	// Detect deputy entities: entities that have "deputy" in their HAS_ROLE targets
	// We detect these from the triples themselves
	deputyPersons := map[string]bool{}
	for _, t := range triples {
		if strings.ToUpper(t.Edge) == "HAS_ROLE" {
			targetLower := strings.ToLower(t.Node2)
			if strings.Contains(targetLower, "deputy") || strings.Contains(targetLower, "acting") || strings.Contains(targetLower, "interim") {
				deputyPersons[t.Node1] = true
			}
		}
	}

	// Active relations that planned entities cannot have
	activeRelations := map[string]bool{
		"OPERATES": true, "OFFERS": true, "PROVIDES": true,
		"WORKS_AT": true, "BASED_AT": true,
		"MANAGES": true, "MANAGED_BY": true,
	}

	// Allowed relations for planned entities
	// (everything else is rejected)

	result := make([]models.Triple, 0, len(triples))
	plannedRejected := 0
	deputyFixed := 0

	for _, t := range triples {
		edge := strings.ToUpper(t.Edge)

		// Check if either endpoint is a planned entity with an active relation
		if activeRelations[edge] {
			if plannedEntities[t.Node1] || plannedEntities[t.Node2] {
				plannedRejected++
				continue
			}
		}

		// Check if source is a deputy person trying to MANAGES
		if edge == "MANAGES" && deputyPersons[t.Node1] {
			// Convert MANAGES to HAS_DEPUTY_MANAGER (flipped direction: org -> person)
			t.Edge = "HAS_DEPUTY_MANAGER"
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			deputyFixed++
		}

		result = append(result, t)
	}

	if plannedRejected > 0 || deputyFixed > 0 {
		log.Printf("Contextual enforcement: rejected %d planned-entity triples, fixed %d deputy->MANAGES", plannedRejected, deputyFixed)
	}

	// BASED_AT limiter: if a person has multiple BASED_AT, keep only the first (most evidenced)
	// and convert others to VISITS (since BASED_AT should mean primary location)
	result = limitBasedAt(result, entityTypeMap)

	return result
}

// limitBasedAt ensures each person has at most one BASED_AT relation.
// Keeps the best-evidenced BASED_AT (by weight, then evidence length) and converts others to VISITS.
func limitBasedAt(triples []models.Triple, entityTypeMap map[string]*EntityTypeInfo) []models.Triple {
	// Collect BASED_AT indices per person
	type basedAtEntry struct {
		index    int
		weight   float64
		evidence int // length of evidence string as quality proxy
	}
	personBasedAt := map[string][]basedAtEntry{}
	for i, t := range triples {
		if strings.ToUpper(t.Edge) == "BASED_AT" {
			info := entityTypeMap[t.Node1]
			if info != nil && info.BaseType == "person" {
				personBasedAt[t.Node1] = append(personBasedAt[t.Node1], basedAtEntry{
					index:    i,
					weight:   t.Weight,
					evidence: len(t.Evidence),
				})
			}
		}
	}

	// For persons with multiple BASED_AT, find the best one and convert the rest
	convertIndices := map[int]bool{}
	for _, entries := range personBasedAt {
		if len(entries) <= 1 {
			continue
		}
		// Pick best: highest weight, tie-break by evidence length
		bestIdx := 0
		for i := 1; i < len(entries); i++ {
			if entries[i].weight > entries[bestIdx].weight ||
				(entries[i].weight == entries[bestIdx].weight && entries[i].evidence > entries[bestIdx].evidence) {
				bestIdx = i
			}
		}
		for i, e := range entries {
			if i != bestIdx {
				convertIndices[e.index] = true
			}
		}
	}

	if len(convertIndices) == 0 {
		return triples
	}

	result := make([]models.Triple, len(triples))
	for i, t := range triples {
		result[i] = t
		if convertIndices[i] {
			result[i].Edge = "VISITS"
		}
	}

	log.Printf("BASED_AT limiter: converted %d extra BASED_AT to VISITS (evidence-ranked)", len(convertIndices))
	return result
}

// isPlannedEntity detects whether an entity is planned/future from its profile evidence.
func isPlannedEntity(prof *models.EntityProfile) bool {
	if prof == nil {
		return false
	}
	plannedKeywords := []string{"planned", "future", "upcoming", "expected opening", "under construction", "not yet open", "will open", "scheduled to open"}
	// Check description
	lower := strings.ToLower(prof.Description)
	for _, kw := range plannedKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	// Check mentions
	for _, m := range prof.Mentions {
		ml := strings.ToLower(m)
		for _, kw := range plannedKeywords {
			if strings.Contains(ml, kw) {
				return true
			}
		}
	}
	return false
}

// collectAliasEndpoints extracts alias groups from ALIAS_OF triples and picks the
// best canonical name using a scoring function. Returns alias→canonical mappings.
func collectAliasEndpoints(triples []models.Triple, entities []models.Entity) map[string]string {
	// Collect alias pairs (direction-agnostic — we'll score to pick canonical)
	type pair struct{ a, b string }
	pairs := []pair{}
	for _, t := range triples {
		if strings.ToUpper(t.Edge) == "ALIAS_OF" && t.Node1 != "" && t.Node2 != "" && t.Node1 != t.Node2 {
			pairs = append(pairs, pair{t.Node1, t.Node2})
		}
	}

	if len(pairs) == 0 {
		return nil
	}

	// Build alias groups using union-find style grouping
	canonical := map[string]string{} // name → group representative
	for _, p := range pairs {
		// Find existing groups
		groupA := resolveCanonical(canonical, p.a)
		groupB := resolveCanonical(canonical, p.b)
		if groupA == groupB {
			continue
		}
		// Score both to pick the better canonical
		if canonicalScore(groupA, entities) >= canonicalScore(groupB, entities) {
			canonical[groupB] = groupA
		} else {
			canonical[groupA] = groupB
		}
	}

	// Flatten: build alias→canonical map
	mappings := map[string]string{}
	for name := range canonical {
		canon := resolveCanonical(canonical, name)
		if canon != name {
			mappings[name] = canon
		}
	}

	return mappings
}

// resolveCanonical follows the chain to find the ultimate canonical name.
func resolveCanonical(m map[string]string, name string) string {
	visited := map[string]bool{}
	current := name
	for {
		next, ok := m[current]
		if !ok || next == current || visited[current] {
			return current
		}
		visited[current] = true
		current = next
	}
}

// canonicalScore scores a name for canonical-ness. Higher = better canonical.
func canonicalScore(name string, entities []models.Entity) int {
	score := 0
	lower := strings.ToLower(name)

	// Prefer longer names (more specific)
	score += len(name)

	// Prefer names with brand/network prefix (e.g. "cedargate haifa central" > "haifa central")
	words := strings.Fields(name)
	if len(words) >= 3 {
		score += 20
	}

	// Prefer names that appear as entities with a non-alias type
	for _, e := range entities {
		if strings.ToLower(e.Name) == lower {
			if e.Type != "" && e.Type != "alias" {
				score += 30
			}
			break
		}
	}

	// Penalize very short names (likely abbreviations)
	if len(name) < 10 {
		score -= 10
	}

	// Penalize names that are just a generic location-like word
	if len(words) <= 2 {
		score -= 5
	}

	return score
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
