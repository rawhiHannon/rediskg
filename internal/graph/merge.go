package graph

import (
	"rediskg/pkg/models"
)

// MergeEdges combines LLM-extracted triples (with semantic weight) and
// contextual proximity edges into a single deduplicated edge list.
// Edges between the same node pair are merged by summing weights and
// concatenating descriptions.
func MergeEdges(triples []models.Triple, proximityEdges []models.EdgeRecord, semanticWeight float64) []models.EdgeRecord {
	merged := map[string]*models.EdgeRecord{}

	// Add LLM-extracted triples with semantic weight
	for _, t := range triples {
		// Skip self-referencing edges
		if t.Node1 == t.Node2 {
			continue
		}

		n1, n2 := nodePairKey(t.Node1, t.Node2)
		key := n1 + "|||" + n2

		if existing, ok := merged[key]; ok {
			existing.Weight += semanticWeight
			// Keep the shorter (more specific) edge label rather than concatenating
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
				Node1:     n1,
				Node1Type: t.Node1Type,
				Node2:     n2,
				Node2Type: t.Node2Type,
				Edge:      t.Edge,
				ChunkIDs:  chunkIDs,
				Weight:    semanticWeight,
				Inferred:  false,
			}
		}
	}

	// Add contextual proximity edges
	for _, pe := range proximityEdges {
		n1, n2 := nodePairKey(pe.Node1, pe.Node2)
		key := n1 + "|||" + n2

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

	// Convert map to slice
	result := make([]models.EdgeRecord, 0, len(merged))
	for _, edge := range merged {
		result = append(result, *edge)
	}

	return result
}

// ApplyStandardization applies entity name mappings to all triples.
// For example, if mappings says "ai" -> "artificial intelligence",
// all occurrences of "ai" in node_1 or node_2 get replaced.
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

func appendUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}
