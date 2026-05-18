package pipeline

import (
	"fmt"
	"log"
	"regexp"
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

	// Phase 2b: Filter raw value entities (dates, times, quantities — type + regex)
	preFilter := len(candidateGraph.Entities)
	candidateGraph.Entities = filterRawValueEntities(candidateGraph.Entities)
	if preFilter != len(candidateGraph.Entities) {
		log.Printf("  Filtered %d raw value entities (%d remaining)",
			preFilter-len(candidateGraph.Entities), len(candidateGraph.Entities))
	}

	// Phase 2c: Remove edges whose endpoints were filtered
	preOrphan := len(candidateGraph.Edges)
	candidateGraph.Edges = filterOrphanEdges(candidateGraph.Edges, candidateGraph.Entities)
	if preOrphan != len(candidateGraph.Edges) {
		log.Printf("  Filtered %d orphan edges (%d remaining)",
			preOrphan-len(candidateGraph.Edges), len(candidateGraph.Edges))
	}

	// Phase 3: Group aliases and deduplicate entity mentions
	log.Println("[3/14] Grouping aliases...")
	aliasMap := buildAliasMap(candidateGraph.Entities)
	log.Printf("  Found %d alias mappings", len(aliasMap))

	// Phase 4: Select canonical entities (merge duplicates, pick best types)
	log.Println("[4/14] Selecting canonical entities...")
	canonicalEntities := selectCanonicalEntities(candidateGraph.Entities, aliasMap)
	log.Printf("  %d canonical entities", len(canonicalEntities))

	// Phase 4b: Fix event entity statuses (incidents != planned)
	fixEntityStatuses(canonicalEntities)

	// Phase 5: Rewrite all edges to canonical entity names
	log.Println("[5/14] Rewriting edges to canonical entities...")
	candidateGraph.Edges = rewriteEdgesToCanonical(candidateGraph.Edges, aliasMap)

	// Phase 6: Normalize relation names to stable internal IDs
	log.Println("[6/14] Normalizing relations...")
	candidateGraph.Edges = normalizeRelations(candidateGraph.Edges)
	log.Printf("  %d edges after normalization", len(candidateGraph.Edges))

	// Phase 7: Deterministic negation fix (evidence-based relation correction)
	log.Println("[7/14] Fixing negated relations from evidence...")
	candidateGraph.Edges = fixNegatedRelations(candidateGraph.Edges)

	// Phase 8: Deterministic conditional annotation (evidence-based status/condition)
	log.Println("[8/14] Annotating conditional edges from evidence...")
	candidateGraph.Edges = annotateConditionalEdges(candidateGraph.Edges)

	// Phase 9: Status-aware edge rewriting
	log.Println("[9/14] Rewriting status-aware edges...")
	preRewrite := len(candidateGraph.Edges)
	candidateGraph.Edges = rewriteStatusAwareEdges(candidateGraph.Edges, canonicalEntities)
	log.Printf("  Status rewriting: %d -> %d edges", preRewrite, len(candidateGraph.Edges))

	// Phase 10: Build alternative/conflict groups
	log.Println("[10/14] Building alternative groups...")
	candidateGraph.Edges = solver.BuildAlternativeGroups(candidateGraph.Edges)

	// Phase 11: Apply hard constraints
	log.Println("[11/14] Applying hard constraints...")
	preCount := len(candidateGraph.Edges)
	candidateGraph.Edges = solver.ApplyHardConstraints(
		candidateGraph.Edges, canonicalEntities, aliasMap,
	)
	log.Printf("  Hard constraints: %d -> %d edges", preCount, len(candidateGraph.Edges))

	// Phase 12: Global graph selection
	log.Println("[12/14] Running global graph selector...")
	finalGraph := solver.SelectFinalGraph(candidateGraph.Edges, canonicalEntities)
	log.Printf("  Final graph: %d entities, %d edges",
		len(finalGraph.Entities), len(finalGraph.Edges))

	// Phase 13: Post-solver validation
	log.Println("[13/14] Post-solver validation...")
	finalGraph = postSolverValidation(finalGraph, aliasMap)
	log.Printf("  After validation: %d entities, %d edges",
		len(finalGraph.Entities), len(finalGraph.Edges))

	// Phase 14: Negative-fact conflict resolution
	log.Println("[14/14] Resolving negative-fact conflicts...")
	preConflict := len(finalGraph.Edges)
	finalGraph.Edges = resolveNegativeConflicts(finalGraph.Edges)
	log.Printf("  Conflict resolution: %d -> %d edges", preConflict, len(finalGraph.Edges))

	// Materialize final KG
	log.Println("Materializing to FalkorDB...")
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

	// First pass: score all canonical candidates
	canonicalScores := map[string]float64{}
	evidenceCounts := map[string]int{}
	for _, e := range entities {
		name := e.CanonicalName
		if name == "" {
			name = e.Mention
		}
		evidenceCounts[name] += len(e.Evidence)
	}

	for _, e := range entities {
		name := e.CanonicalName
		if name == "" {
			name = e.Mention
		}
		score := scoreCanonicalCandidate(name, e, evidenceCounts[name])
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
		canonical = strings.ToLower(strings.TrimSpace(canonical))

		// Map mention to canonical if different
		mention := strings.ToLower(strings.TrimSpace(e.Mention))
		if mention != canonical && mention != "" {
			aliasMap[mention] = canonical
		}

		// Map declared aliases
		for _, alias := range e.Aliases {
			aliasName := strings.ToLower(strings.TrimSpace(alias.Text))
			if aliasName != canonical && aliasName != "" {
				aliasMap[aliasName] = canonical
			}
		}
	}

	return aliasMap
}

// scoreCanonicalCandidate computes a quality score for a canonical name candidate.
func scoreCanonicalCandidate(name string, e models.CandidateEntity, evidenceCount int) float64 {
	score := 0.0

	// Full-name boost: longer names are more specific and better canonicals
	words := strings.Fields(name)
	score += float64(len(words)) * 3.0

	// Base type confidence boost
	if len(e.BaseTypes) > 0 {
		score += e.BaseTypes[0].Score * 10
	}

	// Evidence count boost: more mentions = more reliable
	score += float64(evidenceCount) * 2.0

	// Functional role boost: entities with roles are more semantically grounded
	score += float64(len(e.FunctionalRoles)) * 3.0

	// Status boost: known status is better than unknown
	if e.Status != "" && e.Status != "unknown" {
		score += 2.0
	}

	// Document-title penalty
	docPatterns := []string{"knowledge base", "internal operations", "last reviewed",
		"document owner", "version", "report", "manual", "policy document",
		"user guide", "reference guide"}
	lower := strings.ToLower(name)
	for _, pattern := range docPatterns {
		if strings.Contains(lower, pattern) {
			score -= 20.0
			break
		}
	}

	// Generic phrase penalty: single common words are poor canonicals
	if len(words) == 1 {
		genericWords := map[string]bool{
			"service": true, "branch": true, "unit": true, "office": true,
			"center": true, "site": true, "department": true, "team": true,
			"manager": true, "director": true, "system": true, "portal": true,
		}
		if genericWords[lower] {
			score -= 15.0
		}
	}

	// Alias penalty: if this entity was declared as an alias of something else, it's not the best canonical
	if e.CanonicalName != "" && e.Mention != e.CanonicalName {
		// The mention is an alias variant
		score -= 5.0
	}

	return score
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
		from := strings.ToLower(strings.TrimSpace(edges[i].FromMention))
		to := strings.ToLower(strings.TrimSpace(edges[i].ToMention))
		edges[i].FromMention = from
		edges[i].ToMention = to
		if canon, ok := aliasMap[from]; ok {
			edges[i].FromMention = canon
		}
		if canon, ok := aliasMap[to]; ok {
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
			Name:       ent.CanonicalName,
			Properties: map[string]interface{}{},
		}
		if len(ent.BaseTypes) > 0 {
			entity.BaseType = ent.BaseTypes[0]
			entity.Type = ent.BaseTypes[0]
		}
		if len(ent.DomainTypes) > 0 {
			entity.DomainType = ent.DomainTypes[0]
			entity.Properties["domain_type"] = ent.DomainTypes[0]
		}
		if ent.Status != "" {
			entity.Properties["status"] = ent.Status
		}
		if len(ent.FunctionalRoles) > 0 {
			entity.Properties["functional_roles"] = strings.Join(ent.FunctionalRoles, ",")
		}
		if err := p.store.CreateEntity(entity); err != nil {
			log.Printf("Warning: failed to store entity '%s': %v", ent.CanonicalName, err)
		}
	}

	// Store edges
	for _, edge := range fg.Edges {
		evidence := ""
		if len(edge.Evidence) > 0 {
			evidence = edge.Evidence[0].Text
		}
		edgeRecord := models.EdgeRecord{
			Node1:     edge.From,
			Node2:     edge.To,
			Edge:      edge.RelationID,
			Weight:    edge.Weight,
			ChunkIDs:  edge.ChunkIDs,
			Evidence:  evidence,
			Status:    edge.Status,
			Condition: edge.Condition,
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
func postSolverValidation(fg *models.FinalGraph, aliasMap map[string]string) *models.FinalGraph {
	// Build entity lookup
	entityByName := map[string]*models.KGEntity{}
	for i := range fg.Entities {
		entityByName[fg.Entities[i].CanonicalName] = &fg.Entities[i]
	}

	// Filter edges: both endpoints must exist, no self-loops, no stale alias endpoints
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
		// Reject non-ALIAS_OF edges where either endpoint is still an alias
		if edge.RelationID != "ALIAS_OF" {
			if _, isAlias := aliasMap[edge.From]; isAlias {
				continue
			}
			if _, isAlias := aliasMap[edge.To]; isAlias {
				continue
			}
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

// rewriteStatusAwareEdges converts edges based on entity status.
// - Planned entity + OFFERS → PLANNED_SERVICE
// - Planned entity + HAS_BRANCH → HAS_PLANNED_BRANCH
// - Any edge touching a planned entity gets status="planned"
func rewriteStatusAwareEdges(edges []models.CandidateEdge, entities map[string]*models.CanonicalEntity) []models.CandidateEdge {
	for i := range edges {
		e := &edges[i]
		fromEnt := entities[e.FromMention]
		toEnt := entities[e.ToMention]

		fromPlanned := fromEnt != nil && fromEnt.IsPlanned()
		toPlanned := toEnt != nil && toEnt.IsPlanned()

		// Rewrite specific relation IDs for planned entities
		if fromPlanned || toPlanned {
			switch e.RelationID {
			case "OFFERS":
				if fromPlanned {
					e.RelationID = "PLANNED_SERVICE"
				}
			case "HAS_BRANCH":
				if toPlanned {
					e.RelationID = "HAS_PLANNED_BRANCH"
				}
			}
			// Mark all edges touching planned entities with planned status
			if e.Status == "" {
				e.Status = "planned"
			}
		}
	}
	return edges
}

// resolveNegativeConflicts removes positive edges when a matching negative edge exists.
// E.g., OFFERS is removed if DOES_NOT_OFFER exists for the same (from, to) pair.
func resolveNegativeConflicts(edges []models.KGEdge) []models.KGEdge {
	// Map of negative relation -> positive relation it overrides
	negativeOverrides := map[string]string{
		"DOES_NOT_OFFER":                   "OFFERS",
		"DOES_NOT_WORK_AT":                 "BASED_AT",
		"NO_CONTRACT_WITH":                 "CONTRACTED_WITH",
		"DOES_NOT_HANDLE_BILLING_FOR":      "HANDLES_BILLING_FOR",
		"DOES_NOT_HANDLE_CLAIMS_FOR":       "HANDLES_BILLING_FOR",
		"DOES_NOT_HANDLE_REIMBURSEMENT_FOR":"HANDLES_REIMBURSEMENT_FOR",
		"DOES_NOT_PROCESS_TESTS_FOR":       "PROCESSES_TESTS_FOR",
	}

	// Build set of (from, to) pairs with negative facts
	type pairKey struct{ from, to string }
	denied := map[string]map[pairKey]bool{} // positive_rel -> set of denied pairs

	for _, e := range edges {
		if positiveRel, isNeg := negativeOverrides[e.RelationID]; isNeg {
			if denied[positiveRel] == nil {
				denied[positiveRel] = map[pairKey]bool{}
			}
			denied[positiveRel][pairKey{e.From, e.To}] = true
		}
	}

	if len(denied) == 0 {
		return edges
	}

	var result []models.KGEdge
	for _, e := range edges {
		if pairs, ok := denied[e.RelationID]; ok {
			if pairs[pairKey{e.From, e.To}] {
				continue // overridden by negative fact
			}
		}
		result = append(result, e)
	}
	return result
}

// filterRawValueEntities removes entities that are raw values (dates, times, etc.)
// and should be stored as edge properties instead.
func filterRawValueEntities(entities []models.CandidateEntity) []models.CandidateEntity {
	var result []models.CandidateEntity
	for _, e := range entities {
		if isRawValueEntity(e) {
			continue
		}
		result = append(result, e)
	}
	return result
}

// isRawValueEntity checks if an entity is a raw date/time/quantity value.
// Uses both type-based and regex-based detection.
func isRawValueEntity(e models.CandidateEntity) bool {
	rawTypes := map[string]bool{
		"date_time": true, "quantity": true, "money": true, "identifier": true,
	}
	// Check ALL base types, not just the first — if any scored type is raw with high confidence, filter
	for _, bt := range e.BaseTypes {
		if rawTypes[bt.Type] && bt.Score >= 0.5 {
			return true
		}
	}

	// Regex fallback: catch temporal values typed as "concept" or other non-raw types
	name := strings.ToLower(strings.TrimSpace(e.Mention))
	if looksLikeRawTemporalValue(name) {
		return true
	}

	return false
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// containsAny checks if s contains any of the substrings.
func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// fixNegatedRelations detects evidence negation and flips positive relations to negative.
// Runs before hard constraints so the solver sees correct relation IDs.
func fixNegatedRelations(edges []models.CandidateEdge) []models.CandidateEdge {
	negationPhrases := []string{
		"does not handle", "doesn't handle", "do not handle",
		"not responsible for",
		"does not process", "doesn't process",
		"does not offer", "doesn't offer",
		"not available",
		"no contract with",
	}

	for i := range edges {
		e := &edges[i]
		ev := strings.ToLower(e.EvidenceText)

		if !containsAny(ev, negationPhrases) {
			continue
		}

		switch e.RelationID {
		case "HANDLES_BILLING_FOR":
			e.RelationID = "DOES_NOT_HANDLE_BILLING_FOR"
		case "HANDLES_REIMBURSEMENT_FOR":
			e.RelationID = "DOES_NOT_HANDLE_REIMBURSEMENT_FOR"
		case "OFFERS":
			e.RelationID = "DOES_NOT_OFFER"
		case "PROCESSES_TESTS_FOR":
			e.RelationID = "DOES_NOT_PROCESS_TESTS_FOR"
		case "CONTRACTED_WITH":
			e.RelationID = "NO_CONTRACT_WITH"
		}
	}
	return edges
}

// annotateConditionalEdges detects conditional/backup evidence and sets edge status/condition.
// Runs after fixNegatedRelations so negated edges are already corrected.
func annotateConditionalEdges(edges []models.CandidateEdge) []models.CandidateEdge {
	conditionalTriggers := []string{
		"if ", "when ", "during ", "unless ", "in case",
	}
	backupTriggers := []string{
		"downtime", "redirected", "backup", "fallback",
		"urgent samples", "unavailable", "emergency",
	}

	for i := range edges {
		e := &edges[i]
		ev := strings.ToLower(e.EvidenceText)

		if !containsAny(ev, append(conditionalTriggers, backupTriggers...)) {
			continue
		}

		// Determine status — only mark backup for eligible partner/service relations
		if containsAny(ev, backupTriggers) {
			if isBackupEligibleRelation(e.RelationID) {
				if e.Status == "" || e.Status == "active" {
					e.Status = "backup"
				}
			} else if e.Status == "" || e.Status == "active" {
				e.Status = "conditional"
			}
		} else if e.Status == "" || e.Status == "active" {
			e.Status = "conditional"
		}

		// Extract condition phrase if not already set
		if e.Condition == "" {
			e.Condition = extractConditionPhrase(e.EvidenceText)
		}
	}
	return edges
}

// isBackupEligibleRelation returns true for partner/service relations where "backup" status makes sense.
// Event relations (INVOLVES, CAUSED_BY, OCCURRED_ON) should not be marked as backup.
func isBackupEligibleRelation(rel string) bool {
	switch rel {
	case "PROCESSES_TESTS_FOR",
		"TRANSPORTS_SAMPLES_FOR",
		"FULFILLS_PRESCRIPTIONS_FOR",
		"HANDLES_BILLING_FOR",
		"HANDLES_REIMBURSEMENT_FOR",
		"PROVIDES_SERVICE_FOR",
		"PROVIDES_REMOTE_SERVICE_FOR":
		return true
	default:
		return false
	}
}

// extractConditionPhrase extracts the conditional clause from evidence text.
func extractConditionPhrase(evidence string) string {
	lower := strings.ToLower(evidence)

	// Try to find "if ...", "when ...", "during ...", "unless ..." clauses
	prefixes := []string{"if ", "when ", "during ", "unless ", "in case "}
	for _, prefix := range prefixes {
		idx := strings.Index(lower, prefix)
		if idx < 0 {
			continue
		}
		// Extract from the prefix to the next sentence boundary
		rest := evidence[idx:]
		// Find end: comma, period, semicolon, or end of string
		endIdx := len(rest)
		for _, delim := range []string{",", ".", ";", " – ", " — "} {
			if pos := strings.Index(rest[len(prefix):], delim); pos >= 0 && pos+len(prefix) < endIdx {
				endIdx = pos + len(prefix)
			}
		}
		phrase := strings.TrimSpace(rest[:endIdx])
		if len(phrase) > 10 { // must be meaningful
			return phrase
		}
	}
	return ""
}

// filterOrphanEdges removes edges whose endpoints were filtered out (e.g., raw value entities).
func filterOrphanEdges(edges []models.CandidateEdge, entities []models.CandidateEntity) []models.CandidateEdge {
	entitySet := make(map[string]bool, len(entities))
	for _, e := range entities {
		mention := strings.ToLower(strings.TrimSpace(e.Mention))
		canonical := strings.ToLower(strings.TrimSpace(e.CanonicalName))
		if mention != "" {
			entitySet[mention] = true
		}
		if canonical != "" {
			entitySet[canonical] = true
		}
	}

	var result []models.CandidateEdge
	for _, e := range edges {
		if entitySet[e.FromMention] && entitySet[e.ToMention] {
			result = append(result, e)
		}
	}
	return result
}

// looksLikeRawTemporalValue checks if a mention looks like a date, time, day-of-week,
// or schedule fragment that should not be a standalone entity.
func looksLikeRawTemporalValue(s string) bool {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^\d{1,2}:\d{2}$`),                                  // 10:00
		regexp.MustCompile(`^\d{1,2}:\d{2}\s`),                                 // 13:00 every business day
		regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`),                              // 2024-11-06
		regexp.MustCompile(`^q[1-4]\s+\d{4}$`),                                 // q4 2026
		regexp.MustCompile(`^(daily|weekly|monthly|yearly|biweekly)$`),          // monthly
		regexp.MustCompile(`^(monday|tuesday|wednesday|thursday|friday|saturday|sunday)s?\b`), // tuesday ...
		regexp.MustCompile(`^(twice|once|three times)\s+per\s+(day|week|month|year)$`),
		regexp.MustCompile(`\b\d+\s+(business\s+)?days?\b`),                     // 3 business days
		regexp.MustCompile(`at least .* days? in advance`),
		regexp.MustCompile(`^\d+\s*(am|pm)$`),                                   // 8am
		regexp.MustCompile(`^every\s+`),                                          // every monday
	}

	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// fixEntityStatuses corrects entity statuses based on evidence.
// Events with past-tense evidence should not be "planned".
func fixEntityStatuses(entities map[string]*models.CanonicalEntity) {
	for _, ent := range entities {
		if !containsStr(ent.BaseTypes, "event") {
			continue
		}

		ev := joinEvidence(ent.Evidence)
		lower := strings.ToLower(ev)

		if ent.Status == "planned" || ent.Status == "unknown" {
			if containsAny(lower, []string{
				"occurred on", "occurred at", "incident",
				"was reported", "happened on", "took place",
				"was resolved", "was completed",
			}) {
				ent.Status = "historical"
			}
		}
	}
}

// joinEvidence concatenates all evidence text for an entity.
func joinEvidence(refs []models.EvidenceRef) string {
	var parts []string
	for _, r := range refs {
		if r.Text != "" {
			parts = append(parts, r.Text)
		}
	}
	return strings.Join(parts, " ")
}
