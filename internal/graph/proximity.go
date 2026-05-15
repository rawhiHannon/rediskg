package graph

import (
	"rediskg/pkg/models"
)

// nodePairKey creates a consistent key for a node pair (alphabetically ordered).
func nodePairKey(a, b string) (string, string) {
	if a > b {
		return b, a
	}
	return a, b
}

// CalculateContextualProximity computes co-occurrence edges between concepts
// that appear in the same text chunk. This is the dual-weighting trick from
// rahulnyk/knowledge_graph: concepts that co-occur in the same chunk are
// likely related even if the LLM didn't explicitly extract a relationship.
//
// minCount: minimum number of co-occurrences to keep an edge (default 2).
func CalculateContextualProximity(triples []models.Triple, minCount int) []models.EdgeRecord {
	// Step 1: Build chunk -> nodes mapping
	chunkNodes := map[string]map[string]bool{}
	for _, t := range triples {
		if t.ChunkID == "" {
			continue
		}
		if chunkNodes[t.ChunkID] == nil {
			chunkNodes[t.ChunkID] = map[string]bool{}
		}
		chunkNodes[t.ChunkID][t.Node1] = true
		chunkNodes[t.ChunkID][t.Node2] = true
	}

	// Step 2: Self-join on chunk_id — create edges between ALL nodes in same chunk
	type pairData struct {
		chunkIDs map[string]bool
		count    int
	}
	pairs := map[string]*pairData{}

	for chunkID, nodes := range chunkNodes {
		// Skip chunks with too many entities — they produce O(n²) noisy pairs
		if len(nodes) > 20 {
			continue
		}

		nodeList := make([]string, 0, len(nodes))
		for n := range nodes {
			nodeList = append(nodeList, n)
		}

		for i := 0; i < len(nodeList); i++ {
			for j := i + 1; j < len(nodeList); j++ {
				n1, n2 := nodePairKey(nodeList[i], nodeList[j])
				key := n1 + "|||" + n2

				if pairs[key] == nil {
					pairs[key] = &pairData{chunkIDs: map[string]bool{}}
				}
				pairs[key].chunkIDs[chunkID] = true
				pairs[key].count++
			}
		}
	}

	// Step 3: Filter by minCount and create EdgeRecords
	var edges []models.EdgeRecord
	for key, data := range pairs {
		if data.count < minCount {
			continue
		}

		// Split the key back into node names
		n1, n2 := splitPairKey(key)
		chunkIDs := make([]string, 0, len(data.chunkIDs))
		for id := range data.chunkIDs {
			chunkIDs = append(chunkIDs, id)
		}

		edges = append(edges, models.EdgeRecord{
			Node1:    n1,
			Node2:    n2,
			Edge:     "contextual proximity",
			ChunkIDs: chunkIDs,
			Weight:   float64(data.count),
			Inferred: true,
		})
	}

	return edges
}

func splitPairKey(key string) (string, string) {
	for i := 0; i < len(key)-2; i++ {
		if key[i] == '|' && key[i+1] == '|' && key[i+2] == '|' {
			return key[:i], key[i+3:]
		}
	}
	return key, ""
}
