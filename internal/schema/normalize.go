package schema

import (
	"log"
	"strings"

	"rediskg/pkg/models"
)

// TypeCandidate aggregates a proposed type with usage counts and examples.
type TypeCandidate struct {
	Name     string          `json:"name"`
	Count    int             `json:"count"`
	Examples []EntityExample `json:"examples"`
}

// EntityExample shows how a type candidate was used.
type EntityExample struct {
	EntityName  string `json:"entity_name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
}

// RelationCandidate aggregates a proposed relation with usage counts and examples.
type RelationCandidate struct {
	Name     string          `json:"name"`
	Count    int             `json:"count"`
	Examples []TripleExample `json:"examples"`
}

// TripleExample shows how a relation candidate was used.
type TripleExample struct {
	From     string `json:"from"`
	FromType string `json:"from_type"`
	Relation string `json:"relation"`
	To       string `json:"to"`
	ToType   string `json:"to_type"`
	Evidence string `json:"evidence,omitempty"`
}

// TypeNormalizationRule is the LLM's decision for one canonical type group.
type TypeNormalizationRule struct {
	CanonicalDomainType string   `json:"canonical_domain_type"`
	BaseTypes           []string `json:"base_types"`
	Aliases             []string `json:"aliases"`
	Notes               string   `json:"notes,omitempty"`
}

// RelationNormalizationRule is the LLM's decision for one canonical relation group.
type RelationNormalizationRule struct {
	CanonicalRelation string   `json:"canonical_relation"`
	Direction         string   `json:"direction,omitempty"`
	SourceBaseTypes   []string `json:"source_base_types"`
	TargetBaseTypes   []string `json:"target_base_types"`
	Symmetric         bool     `json:"symmetric"`
	Aliases           []string `json:"aliases"`
	InverseAliases    []string `json:"inverse_aliases"`
	RejectAliases     []string `json:"reject_aliases,omitempty"`
}

// SchemaNormalization holds the complete normalization result from the LLM.
type SchemaNormalization struct {
	TypeRules     []TypeNormalizationRule     `json:"type_normalization"`
	RelationRules []RelationNormalizationRule `json:"relation_normalization"`
	RejectedRelations []struct {
		Relation string `json:"relation"`
		Reason   string `json:"reason"`
	} `json:"rejected_relations,omitempty"`
}

// CollectTypeCandidates aggregates all proposed types from entities with counts and examples.
func CollectTypeCandidates(entities []models.Entity, maxExamples int) []TypeCandidate {
	counts := map[string]int{}
	examples := map[string][]EntityExample{}

	for _, e := range entities {
		typ := strings.ToLower(strings.TrimSpace(e.Type))
		if typ == "" {
			continue
		}
		counts[typ]++
		if len(examples[typ]) < maxExamples {
			desc, _ := e.Properties["description"].(string)
			ev, _ := e.Properties["evidence"].(string)
			examples[typ] = append(examples[typ], EntityExample{
				EntityName:  e.Name,
				Type:        typ,
				Description: desc,
				Evidence:    ev,
			})
		}
	}

	result := make([]TypeCandidate, 0, len(counts))
	for name, count := range counts {
		result = append(result, TypeCandidate{
			Name:     name,
			Count:    count,
			Examples: examples[name],
		})
	}
	return result
}

// CollectRelationCandidates aggregates all proposed relations from triples with counts and examples.
func CollectRelationCandidates(triples []models.Triple, maxExamples int) []RelationCandidate {
	counts := map[string]int{}
	examples := map[string][]TripleExample{}

	for _, t := range triples {
		rel := strings.ToUpper(strings.TrimSpace(t.Edge))
		if rel == "" {
			continue
		}
		counts[rel]++
		if len(examples[rel]) < maxExamples {
			examples[rel] = append(examples[rel], TripleExample{
				From:     t.Node1,
				FromType: t.Node1Type,
				Relation: rel,
				To:       t.Node2,
				ToType:   t.Node2Type,
				Evidence: t.Evidence,
			})
		}
	}

	result := make([]RelationCandidate, 0, len(counts))
	for name, count := range counts {
		result = append(result, RelationCandidate{
			Name:     name,
			Count:    count,
			Examples: examples[name],
		})
	}
	return result
}

// ApplyNormalization applies the LLM's schema normalization result to the schema.
// Registers canonical types, aliases, and relation rules.
func (s *Schema) ApplyNormalization(norm *SchemaNormalization) {
	if norm == nil {
		return
	}

	for _, rule := range norm.TypeRules {
		canonical := strings.ToLower(rule.CanonicalDomainType)
		if canonical == "" {
			continue
		}

		bases := make([]string, 0, len(rule.BaseTypes))
		for _, b := range rule.BaseTypes {
			bases = append(bases, strings.ToLower(b))
		}

		if len(bases) == 1 {
			s.RegisterDomainType(canonical, rule.Notes, bases[0])
		} else if len(bases) > 1 {
			s.RegisterDomainTypeMultiBase(canonical, rule.Notes, bases)
		}

		for _, alias := range rule.Aliases {
			alias = strings.ToLower(alias)
			if alias != canonical {
				s.RegisterTypeAlias(alias, canonical)
			}
		}
	}

	for _, rule := range norm.RelationRules {
		canonical := strings.ToUpper(rule.CanonicalRelation)
		if canonical == "" {
			continue
		}

		srcTypes := make([]string, 0, len(rule.SourceBaseTypes))
		for _, t := range rule.SourceBaseTypes {
			srcTypes = append(srcTypes, strings.ToLower(t))
		}
		tgtTypes := make([]string, 0, len(rule.TargetBaseTypes))
		for _, t := range rule.TargetBaseTypes {
			tgtTypes = append(tgtTypes, strings.ToLower(t))
		}

		s.AddRelationType(RelationType{
			Name:        canonical,
			Description: rule.Direction,
			SourceTypes: srcTypes,
			TargetTypes: tgtTypes,
			Symmetric:   rule.Symmetric,
		})

		for _, alias := range rule.Aliases {
			alias = strings.ToUpper(alias)
			if alias != canonical {
				s.RegisterRelationAlias(alias, canonical, false)
			}
		}

		for _, inv := range rule.InverseAliases {
			inv = strings.ToUpper(inv)
			if inv != canonical {
				s.RegisterRelationAlias(inv, canonical, true)
			}
		}

		for _, rej := range rule.RejectAliases {
			rej = strings.ToUpper(rej)
			s.mu.Lock()
			s.relationAliases[rej] = RelationAliasInfo{Canonical: "__REJECTED__", Flip: false}
			s.mu.Unlock()
		}
	}

	for _, rej := range norm.RejectedRelations {
		rel := strings.ToUpper(rej.Relation)
		s.mu.Lock()
		s.relationAliases[rel] = RelationAliasInfo{Canonical: "__REJECTED__", Flip: false}
		s.mu.Unlock()
	}

	log.Printf("Schema normalization applied: %d type rules, %d relation rules, %d rejected relations",
		len(norm.TypeRules), len(norm.RelationRules), len(norm.RejectedRelations))
}
