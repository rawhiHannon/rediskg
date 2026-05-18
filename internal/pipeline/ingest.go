package pipeline

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"rediskg/internal/chunker"
	"rediskg/internal/llm"
	"rediskg/internal/loader"
	"rediskg/internal/schema"
	"rediskg/internal/solver"
	"rediskg/pkg/models"
)

// Ingest loads documents and builds the knowledge graph using the schema-constrained pipeline.
//
// Pipeline order (from spec):
//  1. Extract candidate entities, mentions, aliases, types, and edges.
//  2. Score entity candidates.
//  3. Group aliases.
//  4. Select canonical entities.
//  5. Rewrite all candidate edges to canonical entities.
//  6. Normalize relation names to internal relation IDs.
//  7. Create alternative/conflict groups.
//  8. Apply hard constraints.
//  9. Run global graph selector.
//  10. Materialize final KG.
func (p *Pipeline) Ingest(docs []*models.Document) error {
	log.Println("Starting ingestion pipeline...")

	// Phase 1: Chunk documents
	log.Println("[1/10] Chunking documents...")
	chunks := chunker.ChunkDocuments(docs, p.cfg.ChunkSize, p.cfg.ChunkOverlap)
	log.Printf("  Created %d chunks", len(chunks))

	// Phase 2: Extract candidates (schema-constrained)
	log.Println("[2/10] Extracting candidates (schema-constrained)...")
	candidateGraph := p.extractSchemaConstrained(chunks)
	log.Printf("  Extracted %d entity candidates, %d edge candidates",
		len(candidateGraph.Entities), len(candidateGraph.Edges))

	if len(candidateGraph.Entities) == 0 {
		return fmt.Errorf("no entities extracted from documents")
	}

	// Phase 3: Group aliases and deduplicate entity mentions
	log.Println("[3/10] Grouping aliases...")
	aliasMap := buildAliasMap(candidateGraph.Entities)
	log.Printf("  Found %d alias mappings", len(aliasMap))

	// Phase 4: Select canonical entities (merge duplicates, pick best types)
	log.Println("[4/10] Selecting canonical entities...")
	canonicalEntities := selectCanonicalEntities(candidateGraph.Entities, aliasMap)
	log.Printf("  %d canonical entities", len(canonicalEntities))

	// Phase 5: Rewrite all edges to canonical entity names
	log.Println("[5/10] Rewriting edges to canonical entities...")
	candidateGraph.Edges = rewriteEdgesToCanonical(candidateGraph.Edges, aliasMap)

	// Phase 6: Normalize relation names to stable internal IDs
	log.Println("[6/10] Normalizing relations...")
	candidateGraph.Edges = normalizeRelations(candidateGraph.Edges)
	log.Printf("  %d edges after normalization", len(candidateGraph.Edges))

	// Phase 7: Build alternative/conflict groups
	log.Println("[7/10] Building alternative groups...")
	candidateGraph.Edges = solver.BuildAlternativeGroups(candidateGraph.Edges)

	// Phase 8: Apply hard constraints
	log.Println("[8/10] Applying hard constraints...")
	preCount := len(candidateGraph.Edges)
	candidateGraph.Edges = solver.ApplyHardConstraints(
		candidateGraph.Edges, canonicalEntities, aliasMap,
	)
	log.Printf("  Hard constraints: %d -> %d edges", preCount, len(candidateGraph.Edges))

	// Phase 9: Global graph selection
	log.Println("[9/11] Running global graph selector...")
	finalGraph := solver.SelectFinalGraph(candidateGraph.Edges, canonicalEntities)
	log.Printf("  Final graph: %d entities, %d edges",
		len(finalGraph.Entities), len(finalGraph.Edges))

	// Phase 10: Post-solver validation
	log.Println("[10/11] Post-solver validation...")
	finalGraph = postSolverValidation(finalGraph)
	log.Printf("  After validation: %d entities, %d edges",
		len(finalGraph.Entities), len(finalGraph.Edges))

	// Phase 11: Materialize final KG
	log.Println("[11/11] Materializing to FalkorDB...")
	if err := p.materializeFinalGraph(finalGraph); err != nil {
		return fmt.Errorf("materialization failed: %w", err)
	}

	// Embeddings
	log.Println("Generating embeddings...")
	if err := p.generateEmbeddings(); err != nil {
		log.Printf("Warning: embedding generation failed: %v", err)
	}

	stats, _ := p.store.GetGraphStats()
	log.Printf("Done! Graph has %d nodes and %d edges", stats["nodes"], stats["edges"])

	return nil
}

// IngestDir loads all documents from a directory.
func (p *Pipeline) IngestDir(dirPath string) error {
	log.Printf("Loading documents from %s...", dirPath)
	docs, err := loader.LoadDirectory(dirPath)
	if err != nil {
		return fmt.Errorf("failed to load directory: %w", err)
	}
	if len(docs) == 0 {
		return fmt.Errorf("no supported documents found in %s", dirPath)
	}
	log.Printf("Loaded %d documents", len(docs))
	return p.Ingest(docs)
}

// IngestRawText ingests raw text.
func (p *Pipeline) IngestRawText(text, source string) error {
	doc := loader.LoadText(text, source)
	return p.Ingest([]*models.Document{doc})
}

// extractSchemaConstrained runs schema-constrained extraction on all chunks concurrently.
func (p *Pipeline) extractSchemaConstrained(chunks []*models.Chunk) *models.CandidateGraph {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		allEnts  []models.CandidateEntity
		allEdges []models.CandidateEdge
		sem      = make(chan struct{}, p.cfg.Workers)
	)

	for _, chunk := range chunks {
		wg.Add(1)
		sem <- struct{}{}

		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			entities, edges, err := llm.ExtractWithSchema(p.llmClient, c.Text, c.ID)
			if err != nil {
				log.Printf("Warning: extraction failed for chunk %s: %v", c.ID, err)
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

// buildAliasMap collects alias -> canonical mappings from candidate entities.
func buildAliasMap(entities []models.CandidateEntity) map[string]string {
	aliasMap := map[string]string{}

	// First pass: collect canonical candidates
	canonicalScores := map[string]float64{}
	for _, e := range entities {
		name := e.CanonicalName
		if name == "" {
			name = e.Mention
		}
		// Score: longer names + higher type confidence = better canonical
		score := float64(len(name))
		if len(e.BaseTypes) > 0 {
			score += e.BaseTypes[0].Score * 10
		}
		if score > canonicalScores[name] {
			canonicalScores[name] = score
		}
	}

	// Second pass: map aliases to canonical
	for _, e := range entities {
		canonical := e.CanonicalName
		if canonical == "" {
			canonical = e.Mention
		}

		// Map mention to canonical if different
		if e.Mention != canonical {
			aliasMap[e.Mention] = canonical
		}

		// Map declared aliases
		for _, alias := range e.Aliases {
			aliasName := strings.ToLower(alias.Text)
			if aliasName != canonical && aliasName != "" {
				aliasMap[aliasName] = canonical
			}
		}
	}

	return aliasMap
}

// selectCanonicalEntities merges duplicate entities and selects best types.
func selectCanonicalEntities(entities []models.CandidateEntity, aliasMap map[string]string) map[string]*models.CanonicalEntity {
	merged := map[string]*models.CanonicalEntity{}

	for _, e := range entities {
		// Resolve to canonical name
		name := e.CanonicalName
		if name == "" {
			name = e.Mention
		}
		if canon, ok := aliasMap[name]; ok {
			name = canon
		}

		existing, ok := merged[name]
		if !ok {
			existing = &models.CanonicalEntity{
				ID:            name,
				CanonicalName: name,
				Labels:        map[string]string{},
			}
			merged[name] = existing
		}

		// Aggregate base types (keep highest score per type)
		for _, bt := range e.BaseTypes {
			found := false
			for i, existingBT := range existing.BaseTypes {
				if existingBT == bt.Type {
					found = true
					_ = i
					break
				}
			}
			if !found && bt.Score >= 0.5 {
				existing.BaseTypes = append(existing.BaseTypes, bt.Type)
			}
		}

		// Aggregate domain types
		for _, dt := range e.DomainTypes {
			found := false
			for _, existingDT := range existing.DomainTypes {
				if existingDT == dt.Type {
					found = true
					break
				}
			}
			if !found && dt.Score >= 0.5 {
				existing.DomainTypes = append(existing.DomainTypes, dt.Type)
			}
		}

		// Aggregate functional roles
		for _, role := range e.FunctionalRoles {
			if !containsStr(existing.FunctionalRoles, role) {
				existing.FunctionalRoles = append(existing.FunctionalRoles, role)
			}
		}

		// Set status (prefer non-unknown, first wins)
		if existing.Status == "" || existing.Status == "unknown" {
			if e.Status != "" {
				existing.Status = e.Status
			}
		}

		// Collect aliases
		existing.Aliases = append(existing.Aliases, e.Aliases...)

		// Collect evidence
		existing.Evidence = append(existing.Evidence, e.Evidence...)
	}

	// Ensure all entities have at least one base type
	for _, ent := range merged {
		if len(ent.BaseTypes) == 0 {
			ent.BaseTypes = []string{"concept"} // fallback
		}
	}

	return merged
}

// rewriteEdgesToCanonical replaces alias mentions with canonical names in all edges.
func rewriteEdgesToCanonical(edges []models.CandidateEdge, aliasMap map[string]string) []models.CandidateEdge {
	for i := range edges {
		if canon, ok := aliasMap[edges[i].FromMention]; ok {
			edges[i].FromMention = canon
		}
		if canon, ok := aliasMap[edges[i].ToMention]; ok {
			edges[i].ToMention = canon
		}
	}
	return edges
}

// normalizeRelations ensures all edges use canonical relation IDs.
// Removes edges with rejected/empty relation IDs.
func normalizeRelations(edges []models.CandidateEdge) []models.CandidateEdge {
	var result []models.CandidateEdge
	for _, e := range edges {
		if e.RelationID == "" {
			// Try to resolve raw relation
			resolved, _ := schema.ResolveRelation(e.RelationRaw)
			if resolved == "" {
				continue // rejected
			}
			e.RelationID = resolved
		}
		// Remove self-loops
		if e.FromMention == e.ToMention {
			continue
		}
		result = append(result, e)
	}
	return result
}

// materializeFinalGraph writes the final graph to FalkorDB.
func (p *Pipeline) materializeFinalGraph(fg *models.FinalGraph) error {
	// Store entities
	for _, ent := range fg.Entities {
		entity := models.Entity{
			Name: ent.CanonicalName,
		}
		if len(ent.BaseTypes) > 0 {
			entity.BaseType = ent.BaseTypes[0]
		}
		if len(ent.DomainTypes) > 0 {
			entity.DomainType = ent.DomainTypes[0]
			entity.Type = ent.DomainTypes[0]
		} else if len(ent.BaseTypes) > 0 {
			entity.Type = ent.BaseTypes[0]
		}
		if err := p.store.CreateEntity(entity); err != nil {
			log.Printf("Warning: failed to store entity '%s': %v", ent.CanonicalName, err)
		}
	}

	// Store edges
	for _, edge := range fg.Edges {
		edgeRecord := models.EdgeRecord{
			Node1:    edge.From,
			Node2:    edge.To,
			Edge:     edge.RelationID,
			Weight:   edge.Weight,
			ChunkIDs: edge.ChunkIDs,
		}
		if err := p.store.CreateEdge(edgeRecord); err != nil {
			log.Printf("Warning: failed to store edge %s -[%s]-> %s: %v",
				edge.From, edge.RelationID, edge.To, err)
		}
	}

	return nil
}

// postSolverValidation performs final cleanup on the solved graph.
// Removes orphan entities, ensures type consistency, and validates edges.
func postSolverValidation(fg *models.FinalGraph) *models.FinalGraph {
	// Build entity lookup
	entityByName := map[string]*models.KGEntity{}
	for i := range fg.Entities {
		entityByName[fg.Entities[i].CanonicalName] = &fg.Entities[i]
	}

	// Filter edges: both endpoints must exist as entities
	var validEdges []models.KGEdge
	usedEntities := map[string]bool{}
	for _, edge := range fg.Edges {
		if _, ok := entityByName[edge.From]; !ok {
			continue
		}
		if _, ok := entityByName[edge.To]; !ok {
			continue
		}
		if edge.From == edge.To {
			continue
		}
		validEdges = append(validEdges, edge)
		usedEntities[edge.From] = true
		usedEntities[edge.To] = true
	}

	// Keep only entities that participate in at least one edge
	var validEntities []models.KGEntity
	for _, ent := range fg.Entities {
		if usedEntities[ent.CanonicalName] {
			if len(ent.BaseTypes) == 0 {
				ent.BaseTypes = []string{"concept"}
			}
			validEntities = append(validEntities, ent)
		}
	}

	return &models.FinalGraph{
		Entities: validEntities,
		Edges:    validEdges,
	}
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
