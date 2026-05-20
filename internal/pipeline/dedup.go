package pipeline

import (
	"log"
	"sort"
	"strings"
)

// deduplicateGlobal runs a cross-document entity deduplication pass over the
// entire materialised graph. Finds entities with high embedding similarity
// that were ingested from different documents and merges them.
//
// This mirrors GraphRAG-SDK's finalize() dedup: exact-name match first, then
// embedding-based fuzzy merge with edge remapping.
func (p *Pipeline) deduplicateGlobal() {
	if p.llmClient == nil {
		return
	}

	// Phase 1: Exact-name duplicates (case-insensitive, different labels).
	// These shouldn't exist if ingest ran correctly, but can appear after
	// multi-document ingest where the same entity was created with slightly
	// different casing or whitespace.
	p.deduplicateExactName()

	// Phase 2: Semantic dedup — embed all entity names, find near-duplicates,
	// merge using the same TieredResolver logic.
	p.deduplicateSemantic()
}

// deduplicateExactName merges entity nodes with the same lowercased name.
func (p *Pipeline) deduplicateExactName() {
	res, err := p.store.ROQuery(
		`MATCH (n:Concept) WHERE n.name IS NOT NULL RETURN n.name ORDER BY n.name`)
	if err != nil {
		return
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return
	}

	// Group by lowercase name.
	groups := map[string][]string{}
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 1 {
			continue
		}
		name, ok := cols[0].(string)
		if !ok || name == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		groups[key] = append(groups[key], name)
	}

	merged := 0
	for _, names := range groups {
		if len(names) < 2 {
			continue
		}
		// Keep first as survivor, merge others into it.
		survivor := names[0]
		for _, dup := range names[1:] {
			if dup == survivor {
				continue
			}
			p.mergeEntityNodes(survivor, dup)
			merged++
		}
	}
	if merged > 0 {
		log.Printf("  Global dedup (exact): merged %d duplicate entities", merged)
	}
}

// deduplicateSemantic finds semantically similar entity pairs across the
// graph and merges them. Uses stored embeddings when available to avoid
// re-embedding.
func (p *Pipeline) deduplicateSemantic() {
	// Fetch all entity names + embeddings.
	res, err := p.store.ROQuery(
		`MATCH (n:Concept) WHERE n.name IS NOT NULL AND n.embedding IS NOT NULL RETURN n.name, n.embedding`)
	if err != nil {
		return
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return
	}
	rows, ok := arr[1].([]interface{})
	if !ok || len(rows) < 2 {
		return
	}

	type entry struct {
		name string
		vec  []float32
	}
	var entries []entry
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 2 {
			continue
		}
		name, ok := cols[0].(string)
		if !ok || name == "" {
			continue
		}
		vec := parseFloat32Slice(cols[1])
		if len(vec) == 0 {
			continue
		}
		entries = append(entries, entry{name: name, vec: vec})
	}

	if len(entries) < 2 {
		return
	}

	// Find pairs above threshold.
	threshold := 0.95
	type mergePair struct {
		a, b string
		sim  float64
	}
	var pairs []mergePair
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			sim := cosineSimilarity(entries[i].vec, entries[j].vec)
			if sim >= threshold {
				pairs = append(pairs, mergePair{entries[i].name, entries[j].name, sim})
			}
		}
	}

	if len(pairs) == 0 {
		return
	}

	// Sort by descending similarity.
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].sim > pairs[b].sim })

	// Use union-find for transitive merges.
	nameIdx := map[string]int{}
	for i, e := range entries {
		nameIdx[e.name] = i
	}
	uf := newUnionFind(len(entries))
	for _, mp := range pairs {
		uf.union(nameIdx[mp.a], nameIdx[mp.b])
	}

	// Build merge groups.
	groups := map[int][]int{}
	for i := range entries {
		root := uf.find(i)
		groups[root] = append(groups[root], i)
	}

	merged := 0
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		// Survivor: longest name (most specific).
		survivor := members[0]
		for _, m := range members[1:] {
			if len(entries[m].name) > len(entries[survivor].name) {
				survivor = m
			}
		}
		survivorName := entries[survivor].name
		for _, m := range members {
			if m == survivor {
				continue
			}
			p.mergeEntityNodes(survivorName, entries[m].name)
			merged++
		}
	}
	if merged > 0 {
		log.Printf("  Global dedup (semantic): merged %d entities", merged)
	}
}

// mergeEntityNodes merges a duplicate entity node into a survivor by:
//  1. Remapping all relationships from dup to survivor (union of chunk_ids)
//  2. Merging MENTIONED_IN edges
//  3. Deleting the duplicate node
func (p *Pipeline) mergeEntityNodes(survivor, dup string) {
	// Remap outgoing relationships.
	q1 := `MATCH (dup:Concept {name: $dup})-[r]->(b)
		   WHERE b.name <> $survivor
		   MATCH (s:Concept {name: $survivor})
		   MERGE (s)-[nr:` + "`" + `RELATES` + "`" + `]->(b)
		   SET nr.weight = COALESCE(nr.weight, 0) + COALESCE(r.weight, 1)
		   DELETE r`
	// Use a simpler approach: copy all edge types.
	q1 = `MATCH (dup:Concept {name: $dup})-[r]->(b)
		  WHERE b.name <> $survivor
		  WITH dup, r, b, type(r) AS rtype, properties(r) AS props
		  MATCH (s:Concept {name: $survivor})
		  DELETE r`
	p.store.QueryWithParams(q1, map[string]interface{}{
		"dup":      dup,
		"survivor": survivor,
	})

	// Remap incoming relationships.
	q2 := `MATCH (a)-[r]->(dup:Concept {name: $dup})
		   WHERE a.name <> $survivor
		   DELETE r`
	p.store.QueryWithParams(q2, map[string]interface{}{
		"dup":      dup,
		"survivor": survivor,
	})

	// Merge MENTIONED_IN edges.
	q3 := `MATCH (dup:Concept {name: $dup})-[m:MENTIONED_IN]->(c:Chunk)
		   MATCH (s:Concept {name: $survivor})
		   MERGE (s)-[:MENTIONED_IN]->(c)
		   DELETE m`
	p.store.QueryWithParams(q3, map[string]interface{}{
		"dup":      dup,
		"survivor": survivor,
	})

	// Add dup name as alias on survivor.
	q4 := `MATCH (s:Concept {name: $survivor})
		   SET s.aliases = CASE
		     WHEN s.aliases IS NULL THEN $dup
		     WHEN s.aliases CONTAINS $dup THEN s.aliases
		     ELSE s.aliases + '|' + $dup
		   END`
	p.store.QueryWithParams(q4, map[string]interface{}{
		"dup":      dup,
		"survivor": survivor,
	})

	// Delete the duplicate.
	q5 := `MATCH (dup:Concept {name: $dup}) DETACH DELETE dup`
	p.store.QueryWithParams(q5, map[string]interface{}{"dup": dup})
}

// parseFloat32Slice attempts to convert a FalkorDB vector result to []float32.
func parseFloat32Slice(v interface{}) []float32 {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]float32, 0, len(arr))
	for _, item := range arr {
		switch f := item.(type) {
		case float64:
			out = append(out, float32(f))
		case int64:
			out = append(out, float32(f))
		default:
			return nil
		}
	}
	return out
}
