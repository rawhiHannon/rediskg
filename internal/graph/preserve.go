// Deprecated: Old entity preservation from pre-schema pipeline. Active pipeline uses pipeline/ingest.go postSolverValidation.
package graph

import (
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// PreserveCoreFacts re-adds high-confidence structural facts that were removed during
// verification/filtering but are schema-valid and evidence-backed.
// This prevents over-aggressive LLM verification from removing backbone facts.
//
// A fact is "core" if:
// 1. It appeared in the raw extraction (rawTriples)
// 2. It has non-empty evidence
// 3. It passes schema validation (relation exists, types match direction)
// 4. Its relation is a structural relation (OPERATES, MANAGES, OFFERS, LOCATED_IN, PART_OF, etc.)
// 5. It was not present in the compiled output (was removed by verification/filtering)
func PreserveCoreFacts(rawTriples, compiledTriples []models.Triple, entityMap map[string]string, s *schema.Schema) []models.Triple {
	// Structural relations that form the backbone of any knowledge graph
	structuralRelations := map[string]bool{
		"OPERATES":    true,
		"MANAGES":     true,
		"OFFERS":      true,
		"PROVIDES":    true,
		"LOCATED_IN":  true,
		"LOCATED_AT":  true,
		"PART_OF":     true,
		"BELONGS_TO":  true,
		"HAS_BRANCH":  true,
		"FOUNDED":     true,
		"FOUNDED_BY":  true,
		"OWNS":        true,
		"EMPLOYS":     true,
		"WORKS_AT":    true,
		"BASED_AT":    true,
		"HAS_ROLE":    true,
	}

	// Index compiled triples for fast lookup
	compiledKeys := map[string]bool{}
	for _, t := range compiledTriples {
		key := strings.ToLower(t.Node1) + "|" + strings.ToUpper(t.Edge) + "|" + strings.ToLower(t.Node2)
		compiledKeys[key] = true
	}

	preserved := 0
	result := make([]models.Triple, len(compiledTriples))
	copy(result, compiledTriples)

	for _, t := range rawTriples {
		edge := strings.ToUpper(t.Edge)

		// Only consider structural relations
		if !structuralRelations[edge] {
			continue
		}

		// Must have evidence
		if t.Evidence == "" {
			continue
		}

		// Must not already be in compiled output
		key := strings.ToLower(t.Node1) + "|" + edge + "|" + strings.ToLower(t.Node2)
		if compiledKeys[key] {
			continue
		}

		// Apply entity types from the authoritative map
		srcType := entityMap[t.Node1]
		tgtType := entityMap[t.Node2]
		if srcType == "" {
			srcType = t.Node1Type
		}
		if tgtType == "" {
			tgtType = t.Node2Type
		}

		// Must pass schema validation
		direction := s.ValidateTripleDirection(edge, srcType, tgtType)
		if direction == "invalid" {
			continue
		}

		// Apply flip if needed
		restored := t
		restored.Edge = edge
		restored.Node1Type = srcType
		restored.Node2Type = tgtType
		if direction == "flip" {
			restored.Node1, restored.Node2 = restored.Node2, restored.Node1
			restored.Node1Type, restored.Node2Type = restored.Node2Type, restored.Node1Type
		}

		// Check for duplicate after flip
		flippedKey := strings.ToLower(restored.Node1) + "|" + strings.ToUpper(restored.Edge) + "|" + strings.ToLower(restored.Node2)
		if compiledKeys[flippedKey] {
			continue
		}

		compiledKeys[flippedKey] = true
		result = append(result, restored)
		preserved++
	}

	if preserved > 0 {
		log.Printf("Core fact preservation: restored %d high-confidence structural facts", preserved)
	}
	return result
}
