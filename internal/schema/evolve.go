package schema

import (
	"log"
	"strings"

	"rediskg/pkg/models"
)

// EvolveFromEntities checks extracted entities against the governance layer.
// Types that pass heuristic checks are accepted directly; ambiguous ones are queued as candidates.
func (s *Schema) EvolveFromEntities(entities []models.Entity) (accepted int, candidates []CandidateType) {
	seen := map[string]bool{}
	for _, e := range entities {
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		if typ == "" || seen[typ] {
			continue
		}
		seen[typ] = true

		// Already known (including via alias)
		if s.HasEntityType(typ) {
			continue
		}

		result := s.CheckProposedType(typ)
		switch result.Decision {
		case "synonym":
			// Auto-register as alias if confidence is high enough
			if result.Confidence >= 0.85 && !result.NeedsLLM {
				s.RegisterTypeAlias(typ, result.CanonicalName)
				accepted++
			} else {
				candidates = append(candidates, CandidateType{
					ProposedName:  typ,
					ProposedBases: []string{s.ResolveBaseType(result.CanonicalName)},
					Decision:      "synonym",
					CanonicalName: result.CanonicalName,
					Confidence:    result.Confidence,
				})
			}
		case "too_vague", "invalid":
			// Skip
		default:
			// Queue as candidate for LLM review
			baseType := ""
			if e.BaseType != "" {
				baseType = e.BaseType
			}
			candidates = append(candidates, CandidateType{
				ProposedName:  typ,
				ProposedBases: filterEmpty([]string{baseType}),
				Evidence:      getEvidence(e),
				Decision:      "", // pending LLM review
				Confidence:    0,
			})
		}
	}

	if accepted > 0 {
		log.Printf("Schema evolution: auto-accepted %d type aliases", accepted)
	}
	return accepted, candidates
}

// EvolveFromTriples checks extracted relations against the governance layer.
// Returns accepted count and candidates needing LLM review.
func (s *Schema) EvolveFromTriples(triples []models.Triple) (accepted int, candidates []CandidateRelation) {
	// Collect observed type pairs for each relation
	relObs := map[string]*relObservation{}
	skipped := 0

	for _, t := range triples {
		rel := strings.ToUpper(strings.TrimSpace(t.Edge))
		if rel == "" {
			continue
		}
		// Skip overly specific relation names (4+ words)
		words := strings.Split(rel, "_")
		if len(words) > 3 {
			skipped++
			continue
		}

		obs, ok := relObs[rel]
		if !ok {
			obs = &relObservation{
				sourceTypes: map[string]int{},
				targetTypes: map[string]int{},
			}
			relObs[rel] = obs
		}
		if t.Node1Type != "" {
			obs.sourceTypes[strings.ToLower(t.Node1Type)]++
		}
		if t.Node2Type != "" {
			obs.targetTypes[strings.ToLower(t.Node2Type)]++
		}
	}

	if skipped > 0 {
		log.Printf("Schema evolution: skipped %d overly-specific relation names (4+ words)", skipped)
	}

	for rel, obs := range relObs {
		// Already known (including via alias)
		if s.HasRelationType(rel) {
			// Update existing with newly observed type pairs
			s.mu.Lock()
			canonical := s.ResolveRelationName(rel)
			existing := s.RelationTypes[canonical]
			if existing != nil {
				for src := range obs.sourceTypes {
					if !containsType(existing.SourceTypes, src) {
						existing.SourceTypes = append(existing.SourceTypes, src)
					}
				}
				for tgt := range obs.targetTypes {
					if !containsType(existing.TargetTypes, tgt) {
						existing.TargetTypes = append(existing.TargetTypes, tgt)
					}
				}
			}
			s.mu.Unlock()
			continue
		}

		result := s.CheckProposedRelation(rel)
		switch result.Decision {
		case "synonym":
			if result.Confidence >= 0.85 && !result.NeedsLLM {
				s.RegisterRelationAlias(rel, result.CanonicalName, false)
				accepted++
			} else {
				candidates = append(candidates, CandidateRelation{
					ProposedName:    rel,
					Decision:        "synonym",
					CanonicalName:   result.CanonicalName,
					SourceBaseTypes: topTypes(obs.sourceTypes, 3),
					TargetBaseTypes: topTypes(obs.targetTypes, 3),
					Confidence:      result.Confidence,
				})
			}
		case "inverse":
			candidates = append(candidates, CandidateRelation{
				ProposedName:    rel,
				Decision:        "inverse",
				CanonicalName:   result.CanonicalName,
				SourceBaseTypes: topTypes(obs.sourceTypes, 3),
				TargetBaseTypes: topTypes(obs.targetTypes, 3),
				Confidence:      result.Confidence,
			})
		case "too_vague", "invalid":
			// Skip
		default:
			candidates = append(candidates, CandidateRelation{
				ProposedName:    rel,
				Decision:        "", // pending LLM review
				SourceBaseTypes: topTypes(obs.sourceTypes, 3),
				TargetBaseTypes: topTypes(obs.targetTypes, 3),
				Confidence:      0,
			})
		}
	}

	if accepted > 0 {
		log.Printf("Schema evolution: auto-accepted %d relation aliases", accepted)
	}
	return accepted, candidates
}

// MergeSchemaDefinitions integrates LLM-provided schema definitions into the schema.
func (s *Schema) MergeSchemaDefinitions(entityTypes []EntityType, relationTypes []RelationType) {
	for _, et := range entityTypes {
		if et.Name == "" {
			continue
		}
		existing := s.GetEntityType(et.Name)
		if existing == nil {
			s.AddEntityType(et)
		} else {
			s.mu.Lock()
			if existing.Description == "" && et.Description != "" {
				existing.Description = et.Description
			}
			if existing.ParentType == "" && et.ParentType != "" {
				existing.ParentType = et.ParentType
			}
			s.mu.Unlock()
		}
	}
	for _, rt := range relationTypes {
		if rt.Name == "" {
			continue
		}
		existing := s.GetRelationType(rt.Name)
		if existing == nil {
			s.AddRelationType(rt)
		} else {
			s.mu.Lock()
			if existing.Description == "" && rt.Description != "" {
				existing.Description = rt.Description
			}
			if rt.Symmetric {
				existing.Symmetric = true
			}
			for _, src := range rt.SourceTypes {
				if !containsType(existing.SourceTypes, src) {
					existing.SourceTypes = append(existing.SourceTypes, src)
				}
			}
			for _, tgt := range rt.TargetTypes {
				if !containsType(existing.TargetTypes, tgt) {
					existing.TargetTypes = append(existing.TargetTypes, tgt)
				}
			}
			s.mu.Unlock()
		}
	}
}

type relObservation struct {
	sourceTypes map[string]int
	targetTypes map[string]int
}

func topTypes(freq map[string]int, n int) []string {
	if len(freq) == 0 {
		return nil
	}
	type kv struct {
		key   string
		count int
	}
	var sorted []kv
	for k, v := range freq {
		sorted = append(sorted, kv{k, v})
	}
	for i := 0; i < len(sorted)-1 && i < n; i++ {
		maxIdx := i
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].count > sorted[maxIdx].count {
				maxIdx = j
			}
		}
		sorted[i], sorted[maxIdx] = sorted[maxIdx], sorted[i]
	}
	limit := n
	if limit > len(sorted) {
		limit = len(sorted)
	}
	result := make([]string, limit)
	for i := 0; i < limit; i++ {
		result[i] = sorted[i].key
	}
	return result
}

func getEvidence(e models.Entity) string {
	if e.Properties == nil {
		return ""
	}
	ev, _ := e.Properties["evidence"].(string)
	return ev
}

func filterEmpty(ss []string) []string {
	result := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}
