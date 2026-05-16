package graph

import (
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// ValidateAndNormalizeTriples normalizes relation names, applies schema-based direction
// checks, and removes invalid triples. Uses the dynamic schema instead of hardcoded rules.
func ValidateAndNormalizeTriples(triples []models.Triple, entityMap map[string]string, s *schema.Schema) []models.Triple {
	var result []models.Triple
	flipped := 0
	normalized := 0
	rejected := 0

	for _, t := range triples {
		// Apply entity types from the validated entityMap
		if mapType, ok := entityMap[t.Node1]; ok && mapType != "" {
			t.Node1Type = mapType
		}
		if mapType, ok := entityMap[t.Node2]; ok && mapType != "" {
			t.Node2Type = mapType
		}

		// Normalize relation name (basic: uppercase, spaces to underscores)
		edge := NormalizeRelation(t.Edge)
		if edge != t.Edge {
			normalized++
		}
		t.Edge = edge

		// Normalize entity types
		t.Node1Type = strings.ToLower(strings.TrimSpace(t.Node1Type))
		t.Node2Type = strings.ToLower(strings.TrimSpace(t.Node2Type))

		// Update entity map with types from triples
		if t.Node1Type != "" {
			entityMap[t.Node1] = t.Node1Type
		}
		if t.Node2Type != "" {
			entityMap[t.Node2] = t.Node2Type
		}

		// Skip self-referencing edges
		if t.Node1 == t.Node2 {
			rejected++
			continue
		}

		// Schema-based direction validation
		direction := s.ValidateTripleDirection(t.Edge, t.Node1Type, t.Node2Type)
		switch direction {
		case "flip":
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			flipped++
		case "invalid":
			rejected++
			continue
		}

		result = append(result, t)
	}

	// Post-pass: deduplicate symmetric edges based on schema
	result = deduplicateSymmetricFromSchema(result, s)

	log.Printf("Validation: normalized %d, flipped %d, rejected %d (of %d total)",
		normalized, flipped, rejected, len(triples))

	return result
}

// ApplyLLMValidation applies the results of LLM-based triple validation to the triple list.
// This handles flips, normalizations, and removals suggested by the LLM.
func ApplyLLMValidation(triples []models.Triple, flips, removes map[string]bool, normalizations map[string]string) []models.Triple {
	result := make([]models.Triple, 0, len(triples))
	flipped := 0
	removed := 0
	normalized := 0

	for _, t := range triples {
		key := tripleKey(t)

		if removes[key] {
			removed++
			continue
		}

		if flips[key] {
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			flipped++
		}

		if newEdge, ok := normalizations[key]; ok {
			t.Edge = newEdge
			normalized++
		}

		result = append(result, t)
	}

	if removed > 0 || flipped > 0 || normalized > 0 {
		log.Printf("LLM validation applied: removed %d, flipped %d, normalized %d", removed, flipped, normalized)
	}
	return result
}

// ApplyVerification applies LLM verification results (removals and modifications) to the triple list.
func ApplyVerification(triples []models.Triple, removals []string, modifications map[string]models.Triple) []models.Triple {
	result := make([]models.Triple, 0, len(triples))
	removeSet := map[string]bool{}
	for _, key := range removals {
		removeSet[key] = true
	}

	removed := 0
	modified := 0
	for _, t := range triples {
		key := t.Node1 + "|" + t.Edge + "|" + t.Node2
		if removeSet[key] {
			removed++
			continue
		}
		if mod, ok := modifications[key]; ok {
			result = append(result, mod)
			modified++
			continue
		}
		result = append(result, t)
	}

	if removed > 0 || modified > 0 {
		log.Printf("LLM verification: removed %d, modified %d edges", removed, modified)
	}
	return result
}

// PropagateTypesFromTriples infers entity types from how entities are used in relations,
// using the schema's relation type definitions as the source of truth.
func PropagateTypesFromTriples(triples []models.Triple, entityMap map[string]string, s *schema.Schema) {
	votes := map[string]map[string]int{}
	addVote := func(entity, typ string) {
		if entity == "" || typ == "" {
			return
		}
		if votes[entity] == nil {
			votes[entity] = map[string]int{}
		}
		votes[entity][typ]++
	}

	for _, t := range triples {
		rt := s.GetRelationType(t.Edge)
		if rt == nil {
			continue
		}
		// If a relation has exactly one expected source type, vote for it
		if len(rt.SourceTypes) == 1 {
			addVote(t.Node1, rt.SourceTypes[0])
		}
		if len(rt.TargetTypes) == 1 {
			addVote(t.Node2, rt.TargetTypes[0])
		}
	}

	propagated := 0
	for entity, typeCounts := range votes {
		currentType := entityMap[entity]

		bestType := ""
		bestCount := 0
		for t, count := range typeCounts {
			if count > bestCount {
				bestType = t
				bestCount = count
			}
		}
		if bestType == "" {
			continue
		}

		// Fill missing types unconditionally
		if currentType == "" {
			entityMap[entity] = bestType
			propagated++
			continue
		}

		// Override only with strong evidence (2+ votes)
		if currentType != bestType && bestCount >= 2 {
			entityMap[entity] = bestType
			propagated++
		}
	}

	if propagated > 0 {
		log.Printf("Type propagation: inferred %d entity types from graph structure", propagated)
	}
}

// NormalizeRelation converts a raw relation string to a canonical form.
// Truncates overly verbose names (4+ words) to keep relations generic.
func NormalizeRelation(edge string) string {
	trimmed := strings.TrimSpace(edge)
	if trimmed == "" {
		return ""
	}
	underscored := strings.ReplaceAll(trimmed, " ", "_")
	underscored = strings.ReplaceAll(underscored, "-", "_")
	upper := strings.ToUpper(underscored)

	// Truncate overly verbose relation names to first 3 words
	parts := strings.Split(upper, "_")
	if len(parts) > 3 {
		upper = strings.Join(parts[:3], "_")
	}
	return upper
}

// deduplicateSymmetricFromSchema removes inverse duplicates for symmetric relations
// as defined in the schema.
func deduplicateSymmetricFromSchema(triples []models.Triple, s *schema.Schema) []models.Triple {
	seen := map[string]bool{}
	removed := 0
	result := make([]models.Triple, 0, len(triples))

	for _, t := range triples {
		rt := s.GetRelationType(t.Edge)
		if rt != nil && rt.Symmetric {
			a, b := t.Node1, t.Node2
			if a > b {
				a, b = b, a
			}
			key := a + "|||" + t.Edge + "|||" + b
			if seen[key] {
				removed++
				continue
			}
			seen[key] = true
		}
		result = append(result, t)
	}

	if removed > 0 {
		log.Printf("Dedup: removed %d inverse-duplicate symmetric edges", removed)
	}
	return result
}

func tripleKey(t models.Triple) string {
	return strings.ToLower(t.Node1) + "|" + strings.ToUpper(t.Edge) + "|" + strings.ToLower(t.Node2)
}
