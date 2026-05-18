package solver

import (
	"fmt"
	"log"
	"sort"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// BuildAlternativeGroups assigns edges to alternative groups.
// Edges with the same (from, to) pair but different relations compete.
// Also handles person-branch relation conflicts (BASED_AT vs VISITS vs PROVIDES_SERVICE_FOR).
func BuildAlternativeGroups(edges []models.CandidateEdge) []models.CandidateEdge {
	// Group by entity pair (order-independent for grouping purposes)
	type pairKey struct{ a, b string }
	pairs := map[pairKey][]int{}

	for i, e := range edges {
		a := strings.ToLower(e.FromMention)
		b := strings.ToLower(e.ToMention)
		if a > b {
			a, b = b, a
		}
		pairs[pairKey{a, b}] = append(pairs[pairKey{a, b}], i)
	}

	// For pairs with multiple different relations, create alternative groups
	for pk, indices := range pairs {
		if len(indices) <= 1 {
			continue
		}

		relations := map[string]bool{}
		for _, idx := range indices {
			relations[edges[idx].RelationID] = true
		}
		if len(relations) <= 1 {
			continue // same relation repeated = support, not conflict
		}

		// Check if these are competing relations
		if areCompetingRelations(relations) {
			groupID := fmt.Sprintf("alt_%s_%s", pk.a, pk.b)
			for _, idx := range indices {
				if edges[idx].AlternativeGroup == "" {
					edges[idx].AlternativeGroup = groupID
				}
			}
		}
	}

	return edges
}

// areCompetingRelations checks if a set of relations for the same entity pair
// should be treated as alternatives (pick one) vs complementary (keep all).
func areCompetingRelations(relations map[string]bool) bool {
	// Resolve aliases so e.g. WORKS_AT and BASED_AT compete correctly
	resolved := make(map[string]bool, len(relations))
	for r := range relations {
		canonical, _ := schema.ResolveRelation(r)
		if canonical == "" {
			canonical = r
		}
		resolved[canonical] = true
	}

	// Person-location relations compete with each other
	personLocationRels := map[string]bool{
		"BASED_AT": true, "VISITS": true, "PROVIDES_SERVICE_FOR": true,
		"PROVIDES_REMOTE_SERVICE_FOR": true, "MANAGES": true,
		"DOES_NOT_WORK_AT": true,
	}

	// Structure relations compete
	structureRels := map[string]bool{
		"HAS_BRANCH": true, "PART_OF": true, "PARTNERS_WITH": true,
	}

	// Manager relations compete
	managerRels := map[string]bool{
		"MANAGES": true, "HAS_DEPUTY_MANAGER": true,
	}

	personCount := 0
	structCount := 0
	managerCount := 0
	for r := range resolved {
		if personLocationRels[r] {
			personCount++
		}
		if structureRels[r] {
			structCount++
		}
		if managerRels[r] {
			managerCount++
		}
	}

	return personCount > 1 || structCount > 1 || managerCount > 1
}

// SelectFinalGraph picks the globally best-consistent set of edges.
//
// Algorithm:
//  1. For each alternative group, pick the highest-scoring candidate.
//  2. For ungrouped edges, keep all above minimum threshold.
//  3. Deduplicate: same (from, relation, to) consolidated.
//  4. Person-branch uniqueness: at most one primary relation per person-branch pair.
func SelectFinalGraph(
	edges []models.CandidateEdge,
	entities map[string]*models.CanonicalEntity,
) *models.FinalGraph {
	const minConfidence = 0.3

	// Step 1: Pick best from each alternative group
	groups := map[string][]models.CandidateEdge{}
	var ungrouped []models.CandidateEdge

	for _, e := range edges {
		if e.AlternativeGroup != "" {
			groups[e.AlternativeGroup] = append(groups[e.AlternativeGroup], e)
		} else {
			ungrouped = append(ungrouped, e)
		}
	}

	var selected []models.CandidateEdge

	for _, groupEdges := range groups {
		sort.Slice(groupEdges, func(i, j int) bool {
			return finalScore(groupEdges[i]) > finalScore(groupEdges[j])
		})
		if finalScore(groupEdges[0]) >= minConfidence {
			selected = append(selected, groupEdges[0])
		}
	}

	// Step 2: Keep ungrouped edges above threshold
	for _, e := range ungrouped {
		if finalScore(e) >= minConfidence {
			selected = append(selected, e)
		}
	}

	log.Printf("Graph selector: %d edges selected from %d candidates (%d groups resolved)",
		len(selected), len(edges), len(groups))

	// Step 3: Deduplicate same (from, relation, to)
	selected = deduplicateEdges(selected)

	// Step 4: Build final graph
	return buildFinal(selected, entities)
}

// finalScore computes the combined score for selection.
func finalScore(e models.CandidateEdge) float64 {
	score := e.EvidenceScore*0.4 + e.SchemaFitScore*0.3 + e.Confidence*0.3
	if score == 0 {
		score = e.Confidence // fallback if components not set
	}
	return score
}

// deduplicateEdges merges edges with the same from/relation/to.
func deduplicateEdges(edges []models.CandidateEdge) []models.CandidateEdge {
	type key struct{ from, rel, to string }
	best := map[key]*models.CandidateEdge{}

	for i := range edges {
		e := &edges[i]
		k := key{
			strings.ToLower(e.FromMention),
			e.RelationID,
			strings.ToLower(e.ToMention),
		}
		if existing, ok := best[k]; ok {
			// Keep higher scoring one, accumulate confidence
			if finalScore(*e) > finalScore(*existing) {
				*existing = *e
			}
			existing.Confidence += e.Confidence * 0.1 // slight boost for repeated support
		} else {
			copy := *e
			best[k] = &copy
		}
	}

	result := make([]models.CandidateEdge, 0, len(best))
	for _, e := range best {
		result = append(result, *e)
	}
	return result
}

// buildFinal converts selected candidates into the final materialized graph.
func buildFinal(edges []models.CandidateEdge, entities map[string]*models.CanonicalEntity) *models.FinalGraph {
	fg := &models.FinalGraph{}

	// Collect used entities
	usedEntities := map[string]bool{}

	for _, e := range edges {
		usedEntities[e.FromMention] = true
		usedEntities[e.ToMention] = true

		kgEdge := models.KGEdge{
			From:       e.FromMention,
			RelationID: e.RelationID,
			To:         e.ToMention,
			Weight:     finalScore(e),
		}
		if e.EvidenceText != "" {
			kgEdge.Evidence = []models.EvidenceRef{{
				Text:     e.EvidenceText,
				Language: e.EvidenceLang,
				ChunkID:  e.ChunkID,
			}}
		}
		if e.ChunkID != "" {
			kgEdge.ChunkIDs = []string{e.ChunkID}
		}
		kgEdge.Status = e.Status
		kgEdge.Condition = e.Condition
		fg.Edges = append(fg.Edges, kgEdge)
	}

	// Build final entities
	for name := range usedEntities {
		ent := entities[name]
		kgEnt := models.KGEntity{
			CanonicalName: name,
		}
		if ent != nil {
			kgEnt.ID = ent.ID
			kgEnt.BaseTypes = ent.BaseTypes
			kgEnt.DomainTypes = ent.DomainTypes
			kgEnt.FunctionalRoles = ent.FunctionalRoles
			kgEnt.Status = ent.Status
			kgEnt.Labels = ent.Labels
			kgEnt.Aliases = ent.Aliases
		}
		fg.Entities = append(fg.Entities, kgEnt)
	}

	// Sort for deterministic output
	sort.Slice(fg.Entities, func(i, j int) bool {
		return fg.Entities[i].CanonicalName < fg.Entities[j].CanonicalName
	})
	sort.Slice(fg.Edges, func(i, j int) bool {
		ki := fg.Edges[i].From + "|" + fg.Edges[i].RelationID + "|" + fg.Edges[i].To
		kj := fg.Edges[j].From + "|" + fg.Edges[j].RelationID + "|" + fg.Edges[j].To
		return ki < kj
	})

	return fg
}
