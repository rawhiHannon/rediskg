package graph

import (
	"log"

	"rediskg/pkg/models"
)

// directedEdgeKey creates a key that preserves edge direction.
func directedEdgeKey(node1, edge, node2 string) string {
	return node1 + "|||" + edge + "|||" + node2
}

// MergeEdges combines LLM-extracted triples into a single deduplicated edge list.
// Edges with the same (node1, relation, node2) are merged by summing weights.
// Direction is preserved — no alphabetical sorting.
func MergeEdges(triples []models.Triple, proximityEdges []models.EdgeRecord, semanticWeight float64) []models.EdgeRecord {
	merged := map[string]*models.EdgeRecord{}

	// Add LLM-extracted triples with semantic weight (directed)
	for _, t := range triples {
		if t.Node1 == t.Node2 {
			continue
		}

		key := directedEdgeKey(t.Node1, t.Edge, t.Node2)

		if existing, ok := merged[key]; ok {
			existing.Weight += semanticWeight
			if len(t.Edge) < len(existing.Edge) {
				existing.Edge = t.Edge
			}
			if t.ChunkID != "" {
				existing.ChunkIDs = appendUnique(existing.ChunkIDs, t.ChunkID)
			}
		} else {
			chunkIDs := []string{}
			if t.ChunkID != "" {
				chunkIDs = []string{t.ChunkID}
			}
			merged[key] = &models.EdgeRecord{
				Node1:     t.Node1,
				Node1Type: t.Node1Type,
				Node2:     t.Node2,
				Node2Type: t.Node2Type,
				Edge:      t.Edge,
				ChunkIDs:  chunkIDs,
				Weight:    semanticWeight,
				Inferred:  false,
			}
		}
	}

	// Add contextual proximity edges (undirected, use sorted key)
	for _, pe := range proximityEdges {
		n1, n2 := nodePairKey(pe.Node1, pe.Node2)
		key := "prox|||" + n1 + "|||" + n2

		if existing, ok := merged[key]; ok {
			existing.Weight += pe.Weight
			for _, cid := range pe.ChunkIDs {
				existing.ChunkIDs = appendUnique(existing.ChunkIDs, cid)
			}
		} else {
			merged[key] = &models.EdgeRecord{
				Node1:    n1,
				Node2:    n2,
				Edge:     pe.Edge,
				ChunkIDs: pe.ChunkIDs,
				Weight:   pe.Weight,
				Inferred: true,
			}
		}
	}

	result := make([]models.EdgeRecord, 0, len(merged))
	for _, edge := range merged {
		result = append(result, *edge)
	}

	// Post-merge cleanups
	result = resolveServiceConflicts(result)
	result = deduplicateSymmetricEdges(result)

	return result
}

// resolveServiceConflicts removes OFFERS_SERVICE edges when the same
// (org, service) pair also has a DOES_NOT_OFFER edge.
// The negative fact (DOES_NOT_OFFER) wins because it's more specific.
func resolveServiceConflicts(edges []models.EdgeRecord) []models.EdgeRecord {
	// Build set of (org, service) pairs with DOES_NOT_OFFER
	denied := map[string]bool{}
	for _, e := range edges {
		if e.Edge == "DOES_NOT_OFFER" {
			denied[e.Node1+"|||"+e.Node2] = true
		}
	}

	if len(denied) == 0 {
		return edges
	}

	removed := 0
	result := make([]models.EdgeRecord, 0, len(edges))
	for _, e := range edges {
		if e.Edge == "OFFERS_SERVICE" {
			key := e.Node1 + "|||" + e.Node2
			if denied[key] {
				removed++
				continue // conflict: DOES_NOT_OFFER wins
			}
		}
		result = append(result, e)
	}

	if removed > 0 {
		log.Printf("Conflict resolution: removed %d OFFERS_SERVICE edges that conflict with DOES_NOT_OFFER", removed)
	}

	return result
}

// ApplyStandardization applies entity name mappings to all triples.
func ApplyStandardization(triples []models.Triple, mappings map[string]string) []models.Triple {
	if len(mappings) == 0 {
		return triples
	}

	result := make([]models.Triple, len(triples))
	for i, t := range triples {
		result[i] = t
		if canonical, ok := mappings[t.Node1]; ok {
			result[i].Node1 = canonical
		}
		if canonical, ok := mappings[t.Node2]; ok {
			result[i].Node2 = canonical
		}
	}
	return result
}

// ApplyStandardizationToEntities applies name mappings to entities too.
func ApplyStandardizationToEntities(entities []models.Entity, mappings map[string]string) []models.Entity {
	if len(mappings) == 0 {
		return entities
	}

	result := make([]models.Entity, len(entities))
	for i, e := range entities {
		result[i] = e
		if canonical, ok := mappings[e.Name]; ok {
			result[i].Name = canonical
		}
	}
	return result
}

// deduplicateSymmetricEdges removes inverse duplicates for symmetric relations
// like HAS_PARTNER. If both A->HAS_PARTNER->B and B->HAS_PARTNER->A exist,
// keep only the one where Node1 sorts first alphabetically.
func deduplicateSymmetricEdges(edges []models.EdgeRecord) []models.EdgeRecord {
	symmetricRelations := map[string]bool{
		"HAS_PARTNER":     true,
		"CONTRACTED_WITH": true,
		"NO_CONTRACT":     true,
	}

	// Build set of (sorted pair, relation) already seen
	seen := map[string]bool{}
	removed := 0
	result := make([]models.EdgeRecord, 0, len(edges))

	for _, e := range edges {
		if symmetricRelations[e.Edge] {
			// Create a canonical key with sorted node pair
			a, b := e.Node1, e.Node2
			if a > b {
				a, b = b, a
			}
			key := a + "|||" + e.Edge + "|||" + b
			if seen[key] {
				removed++
				continue
			}
			seen[key] = true
		}
		result = append(result, e)
	}

	if removed > 0 {
		log.Printf("Dedup: removed %d inverse-duplicate symmetric edges", removed)
	}

	return result
}

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
