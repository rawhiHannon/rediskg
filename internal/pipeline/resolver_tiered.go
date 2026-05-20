package pipeline

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"

	"rediskg/internal/llm"
	"rediskg/pkg/models"
)

// TieredResolver implements a 3-tier entity resolution strategy inspired by
// GraphRAG-SDK's multi-phase dedup:
//
//  1. Exact match: case-insensitive name+label grouping (always, fast)
//  2. Semantic similarity: embedding-based cosine similarity (configurable threshold)
//  3. LLM-verified: ambiguous pairs checked by LLM YES/NO (soft threshold range)
//
// Between tiers, Union-Find clustering ensures transitive merges: if A≈B and
// B≈C, all three merge even if A and C were never directly compared.
type TieredResolver struct {
	LLM *llm.Client

	// HardThreshold: pairs above this are merged without LLM check.
	// Default 0.95.
	HardThreshold float64

	// SoftThreshold: pairs between Soft and Hard are sent to LLM for YES/NO.
	// Below Soft they are skipped entirely. Default 0.80.
	SoftThreshold float64

	// MaxLLMVerifications caps LLM calls per ingest to avoid runaway cost.
	// Default 50.
	MaxLLMVerifications int

	// Workers controls concurrent embedding calls. Default 8.
	Workers int
}

// NewTieredResolver creates a TieredResolver with sensible defaults.
func NewTieredResolver(llmClient *llm.Client) *TieredResolver {
	return &TieredResolver{
		LLM:                 llmClient,
		HardThreshold:       0.95,
		SoftThreshold:       0.80,
		MaxLLMVerifications: 50,
		Workers:             8,
	}
}

// Resolve runs the 3-tier resolution pipeline.
func (tr *TieredResolver) Resolve(entities []models.CandidateEntity) (map[string]*models.CanonicalEntity, map[string]string) {
	// Phase 1: exact match — reuse existing alias-map builder.
	aliasMap := buildAliasMap(entities)
	addServiceCanonRules(entities, aliasMap)
	canonicals := selectCanonicalEntities(entities, aliasMap)

	// Collect unique canonical names for embedding.
	names := make([]string, 0, len(canonicals))
	for name := range canonicals {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order

	if len(names) < 2 || tr.LLM == nil {
		return canonicals, aliasMap
	}

	// Phase 2: embed all canonical entity names.
	embeddings := tr.embedNames(names)
	if len(embeddings) < 2 {
		log.Printf("  Tiered resolver: only %d embeddings, skipping semantic phase", len(embeddings))
		return canonicals, aliasMap
	}

	// Build similarity pairs and classify into hard/soft/skip.
	type pair struct {
		i, j int
		sim  float64
	}
	var hardMerges, softPairs []pair

	for i := 0; i < len(names); i++ {
		ei := embeddings[names[i]]
		if ei == nil {
			continue
		}
		for j := i + 1; j < len(names); j++ {
			ej := embeddings[names[j]]
			if ej == nil {
				continue
			}
			// Skip if base types don't overlap (prevent cross-type merges).
			if !typesOverlap(canonicals[names[i]], canonicals[names[j]]) {
				continue
			}
			sim := cosineSimilarity(ei, ej)
			if sim >= tr.HardThreshold {
				hardMerges = append(hardMerges, pair{i, j, sim})
			} else if sim >= tr.SoftThreshold {
				softPairs = append(softPairs, pair{i, j, sim})
			}
		}
	}

	// Union-Find for transitive merges.
	uf := newUnionFind(len(names))

	// Apply hard merges directly.
	for _, p := range hardMerges {
		uf.union(p.i, p.j)
		log.Printf("  Semantic merge (hard, sim=%.3f): %q + %q", p.sim, names[p.i], names[p.j])
	}

	// Phase 3: LLM-verify soft pairs (sorted by descending similarity so
	// the most likely merges are checked first under the cap).
	sort.Slice(softPairs, func(a, b int) bool {
		return softPairs[a].sim > softPairs[b].sim
	})
	llmChecked := 0
	for _, p := range softPairs {
		// Skip if already in the same cluster (transitive from a hard merge).
		if uf.find(p.i) == uf.find(p.j) {
			continue
		}
		if llmChecked >= tr.MaxLLMVerifications {
			break
		}
		llmChecked++
		if tr.llmVerify(names[p.i], names[p.j], canonicals) {
			uf.union(p.i, p.j)
			log.Printf("  LLM merge (sim=%.3f): %q + %q", p.sim, names[p.i], names[p.j])
		}
	}

	// Build merge groups from Union-Find.
	groups := map[int][]int{} // root -> indices
	for i := range names {
		root := uf.find(i)
		groups[root] = append(groups[root], i)
	}

	// Apply merges: pick the survivor (longest canonical name) and remap.
	mergedCount := 0
	for _, members := range groups {
		if len(members) < 2 {
			continue
		}
		// Pick survivor: entity with the most evidence, then longest name.
		survivor := members[0]
		for _, m := range members[1:] {
			survEv := len(canonicals[names[survivor]].Evidence)
			mEv := len(canonicals[names[m]].Evidence)
			if mEv > survEv || (mEv == survEv && len(names[m]) > len(names[survivor])) {
				survivor = m
			}
		}
		survivorName := names[survivor]
		for _, m := range members {
			if m == survivor {
				continue
			}
			mergedName := names[m]
			// Merge the losing entity into the survivor.
			mergeCanonical(canonicals[survivorName], canonicals[mergedName])
			delete(canonicals, mergedName)
			aliasMap[mergedName] = survivorName
			mergedCount++
		}
	}

	if mergedCount > 0 {
		log.Printf("  Tiered resolver: merged %d entities (%d hard, %d LLM-verified)",
			mergedCount, len(hardMerges), llmChecked)
	}

	return canonicals, aliasMap
}

// embedNames embeds all names concurrently, returns name->embedding map.
func (tr *TieredResolver) embedNames(names []string) map[string][]float32 {
	result := make(map[string][]float32, len(names))
	var mu sync.Mutex
	var wg sync.WaitGroup

	workers := tr.Workers
	if workers <= 0 {
		workers = 8
	}
	sem := make(chan struct{}, workers)

	for _, name := range names {
		sem <- struct{}{}
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := tr.LLM.Embed(n)
			if err != nil {
				log.Printf("  Tiered resolver: embed %q failed: %v", n, err)
				return
			}
			mu.Lock()
			result[n] = vec
			mu.Unlock()
		}(name)
	}
	wg.Wait()
	return result
}

// llmVerify asks the LLM whether two entities refer to the same thing.
func (tr *TieredResolver) llmVerify(nameA, nameB string, canonicals map[string]*models.CanonicalEntity) bool {
	entA := canonicals[nameA]
	entB := canonicals[nameB]

	descA := entityDescription(entA)
	descB := entityDescription(entB)

	systemPrompt := `You are an entity resolution expert. Determine if two entity names refer to the same real-world entity.
Respond with ONLY "YES" or "NO" — no explanation.`

	userPrompt := fmt.Sprintf(`Entity A: %s
Types: %s
Context: %s

Entity B: %s
Types: %s
Context: %s

Do these two entities refer to the same real-world entity? Answer YES or NO.`,
		nameA, strings.Join(entA.BaseTypes, ", "), descA,
		nameB, strings.Join(entB.BaseTypes, ", "), descB,
	)

	resp, err := tr.LLM.Complete(systemPrompt, userPrompt)
	if err != nil {
		log.Printf("  LLM verify %q vs %q failed: %v", nameA, nameB, err)
		return false
	}
	resp = strings.TrimSpace(strings.ToUpper(resp))
	// Handle JSON-wrapped response
	if strings.Contains(resp, "YES") {
		return true
	}
	return false
}

// entityDescription builds a short context string for LLM verification.
func entityDescription(ent *models.CanonicalEntity) string {
	var parts []string
	if len(ent.DomainTypes) > 0 {
		parts = append(parts, "domain: "+strings.Join(ent.DomainTypes, ", "))
	}
	if len(ent.FunctionalRoles) > 0 {
		parts = append(parts, "roles: "+strings.Join(ent.FunctionalRoles, ", "))
	}
	if ent.Status != "" && ent.Status != "unknown" {
		parts = append(parts, "status: "+ent.Status)
	}
	// Include first evidence sentence for context.
	if len(ent.Evidence) > 0 {
		ev := ent.Evidence[0].Text
		if len(ev) > 200 {
			ev = ev[:200] + "..."
		}
		parts = append(parts, "evidence: "+ev)
	}
	if len(parts) == 0 {
		return "(no additional context)"
	}
	return strings.Join(parts, "; ")
}

// mergeCanonical folds `src` into `dst`, combining types/roles/aliases/evidence.
func mergeCanonical(dst, src *models.CanonicalEntity) {
	for _, bt := range src.BaseTypes {
		if !containsStr(dst.BaseTypes, bt) {
			dst.BaseTypes = append(dst.BaseTypes, bt)
		}
	}
	for _, dt := range src.DomainTypes {
		if !containsStr(dst.DomainTypes, dt) {
			dst.DomainTypes = append(dst.DomainTypes, dt)
		}
	}
	for _, role := range src.FunctionalRoles {
		if !containsStr(dst.FunctionalRoles, role) {
			dst.FunctionalRoles = append(dst.FunctionalRoles, role)
		}
	}
	if dst.Status == "" || dst.Status == "unknown" {
		if src.Status != "" {
			dst.Status = src.Status
		}
	}
	dst.Aliases = append(dst.Aliases, src.Aliases...)
	// Add the merged entity's name as an alias.
	dst.Aliases = append(dst.Aliases, models.LangText{Text: src.CanonicalName})
	dst.Evidence = append(dst.Evidence, src.Evidence...)
}

// typesOverlap returns true if two canonical entities share at least one base type.
func typesOverlap(a, b *models.CanonicalEntity) bool {
	if a == nil || b == nil {
		return false
	}
	for _, at := range a.BaseTypes {
		for _, bt := range b.BaseTypes {
			if at == bt {
				return true
			}
		}
	}
	return false
}

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// ── Union-Find ──────────────────────────────────────────────────────

type unionFind struct {
	parent []int
	rank   []int
}

func newUnionFind(n int) *unionFind {
	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	return &unionFind{parent: parent, rank: rank}
}

func (uf *unionFind) find(x int) int {
	for uf.parent[x] != x {
		uf.parent[x] = uf.parent[uf.parent[x]] // path halving
		x = uf.parent[x]
	}
	return x
}

func (uf *unionFind) union(x, y int) {
	rx, ry := uf.find(x), uf.find(y)
	if rx == ry {
		return
	}
	if uf.rank[rx] < uf.rank[ry] {
		rx, ry = ry, rx
	}
	uf.parent[ry] = rx
	if uf.rank[rx] == uf.rank[ry] {
		uf.rank[rx]++
	}
}
