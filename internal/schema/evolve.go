package schema

import (
	"log"
	"strings"

	"rediskg/pkg/models"
)

// EvolveFromEntities updates the schema with entity types discovered during extraction.
// New types are added; existing types are left unchanged.
func (s *Schema) EvolveFromEntities(entities []models.Entity) int {
	added := 0
	for _, e := range entities {
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		if typ == "" {
			continue
		}
		if !s.HasEntityType(typ) {
			s.AddEntityType(EntityType{
				Name:        typ,
				Description: "", // will be enriched by LLM later
			})
			added++
		}
	}
	if added > 0 {
		log.Printf("Schema evolution: added %d new entity types", added)
	}
	return added
}

// EvolveFromTriples updates the schema with relation types discovered during extraction.
// For each new relation, it records the source/target types observed.
// Overly specific relations (4+ words) are skipped — they indicate the LLM encoded
// entity-specific info into the relation name instead of using a generic relation.
func (s *Schema) EvolveFromTriples(triples []models.Triple) int {
	added := 0
	skipped := 0
	// Collect observed type pairs for each relation
	relObs := map[string]*relObservation{}
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
		if s.HasRelationType(rel) {
			// Update existing relation with newly observed type pairs
			s.mu.Lock()
			existing := s.RelationTypes[rel]
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
			s.mu.Unlock()
			continue
		}
		// New relation type
		srcTypes := topTypes(obs.sourceTypes, 3)
		tgtTypes := topTypes(obs.targetTypes, 3)
		s.AddRelationType(RelationType{
			Name:        rel,
			SourceTypes: srcTypes,
			TargetTypes: tgtTypes,
		})
		added++
	}

	if added > 0 {
		log.Printf("Schema evolution: added %d new relation types", added)
	}
	return added
}

// MergeSchemaDefinitions integrates LLM-provided schema definitions into the schema.
// This is called after the LLM reviews and refines the schema.
func (s *Schema) MergeSchemaDefinitions(entityTypes []EntityType, relationTypes []RelationType) {
	for _, et := range entityTypes {
		if et.Name == "" {
			continue
		}
		existing := s.GetEntityType(et.Name)
		if existing == nil {
			s.AddEntityType(et)
		} else {
			// Enrich existing with description/parent if missing
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
			// Merge source/target types
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

// topTypes returns the top N types by count from a frequency map.
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
	// Simple selection sort (small N)
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
