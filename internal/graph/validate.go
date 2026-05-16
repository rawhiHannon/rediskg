package graph

import (
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// ValidateAndNormalizeTriples normalizes relation names, applies schema-based direction
// checks, and removes invalid triples. Uses the dynamic schema instead of hardcoded rules.
// IMPORTANT: entityMap is READ-ONLY. Triples cannot mutate entity types.
func ValidateAndNormalizeTriples(triples []models.Triple, entityMap map[string]string, s *schema.Schema) []models.Triple {
	var result []models.Triple
	flipped := 0
	normalized := 0
	rejected := 0

	for _, t := range triples {
		// Apply entity types from the authoritative entityMap (read-only)
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

		// Skip self-referencing edges
		if t.Node1 == t.Node2 {
			rejected++
			continue
		}

		// Schema-based direction validation (strict)
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
			// Start from original, overlay only non-empty fields from modification
			newT := t
			if mod.Node1 != "" {
				newT.Node1 = mod.Node1
			}
			if mod.Node2 != "" {
				newT.Node2 = mod.Node2
			}
			if mod.Edge != "" {
				newT.Edge = mod.Edge
			}
			if mod.Node1Type != "" {
				newT.Node1Type = mod.Node1Type
			}
			if mod.Node2Type != "" {
				newT.Node2Type = mod.Node2Type
			}
			// Always keep original metadata
			newT.Evidence = t.Evidence
			newT.ChunkID = t.ChunkID
			newT.Weight = t.Weight
			result = append(result, newT)
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


// NormalizeRelation converts a raw relation string to a canonical form.
// Does NOT truncate — schema normalization decides aliases/rejects for verbose names.
func NormalizeRelation(edge string) string {
	trimmed := strings.TrimSpace(edge)
	if trimmed == "" {
		return ""
	}
	underscored := strings.ReplaceAll(trimmed, " ", "_")
	underscored = strings.ReplaceAll(underscored, "-", "_")
	return strings.ToUpper(underscored)
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

// DeduplicateEndpointVariants merges entity name variants that differ only by
// trivial suffixes (trailing 's', 'es', 'ing') when they appear in the same
// relation context. This is domain-agnostic pluralization dedup.
func DeduplicateEndpointVariants(triples []models.Triple) []models.Triple {
	// Collect all unique endpoint names
	names := map[string]int{} // name → occurrence count
	for _, t := range triples {
		names[strings.ToLower(t.Node1)]++
		names[strings.ToLower(t.Node2)]++
	}

	// Build canonical mapping: shorter/more-common form wins
	canonMap := map[string]string{}
	for name := range names {
		// Check if plural form exists
		variants := []string{
			name + "s",    // service → services
			name + "es",   // match → matches
			name + "ies",  // but skip complex suffix rules
		}
		for _, v := range variants {
			if _, ok := names[v]; ok {
				// Both exist — keep the more frequent one
				if names[name] >= names[v] {
					canonMap[v] = name
				} else {
					canonMap[name] = v
				}
			}
		}
		// Check if this IS a plural and the singular exists
		if strings.HasSuffix(name, "s") && !strings.HasSuffix(name, "ss") {
			singular := name[:len(name)-1]
			if _, ok := names[singular]; ok {
				if names[singular] >= names[name] {
					canonMap[name] = singular
				} else {
					canonMap[singular] = name
				}
			}
		}
	}

	if len(canonMap) == 0 {
		return triples
	}

	normalized := 0
	result := make([]models.Triple, len(triples))
	for i, t := range triples {
		result[i] = t
		if canon, ok := canonMap[strings.ToLower(t.Node1)]; ok {
			result[i].Node1 = canon
			normalized++
		}
		if canon, ok := canonMap[strings.ToLower(t.Node2)]; ok {
			result[i].Node2 = canon
			normalized++
		}
	}

	if normalized > 0 {
		log.Printf("Endpoint variant dedup: normalized %d endpoint references", normalized)
	}
	return result
}

// ResolveConflicts removes weaker/redundant triples when stronger alternatives exist.
// Rules:
// - If entity has BASED_AT X, remove WORKS_AT X (BASED_AT is more specific)
// - If entity MANAGES X, remove WORKS_AT X for same entity (implied)
// - Deduplicate exact same fact with different evidence (keep first)
// - Remove generic relations when specific ones exist for same pair
func ResolveConflicts(triples []models.Triple) []models.Triple {
	// Index: entity → set of (relation, target) pairs
	type relTarget struct {
		edge   string
		target string
	}
	entityRels := map[string]map[relTarget]bool{}

	for _, t := range triples {
		src := strings.ToLower(t.Node1)
		if entityRels[src] == nil {
			entityRels[src] = map[relTarget]bool{}
		}
		entityRels[src][relTarget{strings.ToUpper(t.Edge), strings.ToLower(t.Node2)}] = true
	}

	// Define subsumption rules: if (stronger) exists, (weaker) is redundant
	type subsumption struct {
		strongerEdge string
		weakerEdge   string
	}
	rules := []subsumption{
		{"BASED_AT", "WORKS_AT"}, // BASED_AT implies WORKS_AT
		{"MANAGES", "WORKS_AT"},  // Manager at X implies works at X
		{"MANAGES", "BASED_AT"},  // Manager at X implies based at X
		{"OWNS", "PART_OF"},      // Ownership is stronger than part-of
	}

	// Build removal set
	removeKeys := map[string]bool{}
	for _, t := range triples {
		src := strings.ToLower(t.Node1)
		edge := strings.ToUpper(t.Edge)
		tgt := strings.ToLower(t.Node2)

		for _, rule := range rules {
			if edge == rule.weakerEdge {
				// Check if stronger exists for same src→tgt
				if entityRels[src][relTarget{rule.strongerEdge, tgt}] {
					key := src + "|" + edge + "|" + tgt
					removeKeys[key] = true
				}
			}
		}
	}

	// Network rollup detection: if a branch OFFERS service X and the parent network
	// also OFFERS service X, the network-level edge is redundant (keep branch-level)
	// Build: service → set of organizations that offer it
	serviceOfferers := map[string][]string{} // service → list of orgs offering it
	for _, t := range triples {
		if strings.ToUpper(t.Edge) == "OFFERS" || strings.ToUpper(t.Edge) == "PROVIDES" {
			svc := strings.ToLower(t.Node2)
			serviceOfferers[svc] = append(serviceOfferers[svc], strings.ToLower(t.Node1))
		}
	}
	// Build: child → parent from OPERATES/PART_OF
	childParent := map[string]string{}
	for _, t := range triples {
		edge := strings.ToUpper(t.Edge)
		if edge == "OPERATES" || edge == "HAS_BRANCH" {
			// parent → child
			childParent[strings.ToLower(t.Node2)] = strings.ToLower(t.Node1)
		} else if edge == "PART_OF" || edge == "BELONGS_TO" {
			// child → parent
			childParent[strings.ToLower(t.Node1)] = strings.ToLower(t.Node2)
		}
	}
	// Mark network-level OFFERS as redundant if any child also OFFERS same service
	for _, t := range triples {
		edge := strings.ToUpper(t.Edge)
		if edge != "OFFERS" && edge != "PROVIDES" {
			continue
		}
		src := strings.ToLower(t.Node1)
		svc := strings.ToLower(t.Node2)
		// Check if src is a parent of any other offerer of the same service
		for _, otherOrg := range serviceOfferers[svc] {
			if otherOrg == src {
				continue
			}
			if childParent[otherOrg] == src {
				// src is parent, otherOrg (child) also offers this service → parent is redundant
				key := src + "|" + edge + "|" + svc
				removeKeys[key] = true
				break
			}
		}
	}

	// Also deduplicate exact same (src, edge, tgt) — keep first occurrence
	seen := map[string]bool{}
	result := make([]models.Triple, 0, len(triples))
	removed := 0

	for _, t := range triples {
		key := strings.ToLower(t.Node1) + "|" + strings.ToUpper(t.Edge) + "|" + strings.ToLower(t.Node2)

		if removeKeys[key] {
			removed++
			continue
		}
		if seen[key] {
			removed++
			continue
		}
		seen[key] = true
		result = append(result, t)
	}

	if removed > 0 {
		log.Printf("Conflict resolution: removed %d weaker/duplicate triples", removed)
	}
	return result
}
