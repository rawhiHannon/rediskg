// Package schema holds the ontology types + lookup tables. The actual
// ontology content (base types, relations, aliases, rules) is NOT defined
// in this file — it lives in ontology.json next to it and is embedded at
// build time. The Go code here is generic infrastructure; the JSON is data.
//
// Swap the JSON file (or call LoadFromBytes) to point the same engine at a
// different domain.
package schema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
)

// BaseType represents a universal upper-ontology type.
type BaseType struct {
	ID          string
	Description string
}

// RelationFamily groups related relation IDs under a semantic category.
type RelationFamily struct {
	Category  string
	Relations []RelationDef
}

// RelationDef defines a stable internal relation with its constraints.
type RelationDef struct {
	ID          string   // Stable internal relation ID (e.g. "HAS_BRANCH")
	Description string   // Human-readable description
	SourceTypes []string // Allowed source base types (empty = any)
	TargetTypes []string // Allowed target base types (empty = any)
	Symmetric   bool     // Direction doesn't matter
	InverseOf   string   // If set, this is the inverse of another relation
}

// RelationRule defines functional-role-based validation for a relation. The
// constraint engine is generic; the rule data is loaded from JSON.
type RelationRule struct {
	SourceRoles    []string
	TargetRoles    []string

	ForbiddenSourceRoles []string
	ForbiddenTargetRoles []string

	SourceDomainTypes []string
	TargetDomainTypes []string

	ForbiddenSourceDomainTypes []string
	ForbiddenTargetDomainTypes []string

	ForbiddenSourceStatuses []string
	ForbiddenTargetStatuses []string

	CanFlipDirection bool
}

// CanonicalizationRules are tenant-specific name-normalisation rules. Used
// by the canonicalization phase in the ingest pipeline. Everything here is
// data; the engine that applies it lives in the pipeline package.
type CanonicalizationRules struct {
	// ServiceModifiers are stripped from service-name prefixes ONLY when
	// the bare form already exists as an extracted service (safe collapse).
	ServiceModifiers []string `json:"service_modifiers"`
	BranchHints      []string `json:"branch_hints"`
	// MeaningChangingServiceModifiers are prefixes that make a service
	// genuinely different (e.g. "remote" nutrition counselling is a
	// delivery-mode variant, not a synonym). When two service mentions
	// disagree on one of these, we refuse to alias them together — even
	// if the LLM tried to.
	MeaningChangingServiceModifiers []string `json:"meaning_changing_service_modifiers"`
}

// Ontology is the externally-loadable representation. JSON serialisation
// shape lives here too so the file and the in-memory model stay in sync.
type Ontology struct {
	BaseTypes []BaseType `json:"-"`
	// Raw JSON form is decoded into rawBaseTypes first.
	FunctionalRoles []string `json:"functional_roles"`
	Statuses        struct {
		Entity []string `json:"entity"`
		Edge   []string `json:"edge"`
	} `json:"statuses"`
	Relations         []RelationFamily        `json:"-"`
	RelationAliases   map[string]string       `json:"relation_aliases"`
	// RelationInverseAliases map raw LLM relation names whose canonical
	// target is the inverse direction (e.g. "MANAGED_BY" → "MANAGES")
	// so the candidate-edge writer must flip endpoints when resolving.
	RelationInverseAliases map[string]string       `json:"relation_inverse_aliases"`
	RelationRules          map[string]RelationRule `json:"-"`
	Canonicalization       CanonicalizationRules   `json:"canonicalization"`
}

// On-disk JSON shapes (snake_case keys). Decoded then projected onto the
// in-memory Ontology with cleaner Go-style names.
type ontologyJSON struct {
	BaseTypes []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"base_types"`
	FunctionalRoles []string `json:"functional_roles"`
	Statuses        struct {
		Entity []string `json:"entity"`
		Edge   []string `json:"edge"`
	} `json:"statuses"`
	Relations []struct {
		Category string `json:"category"`
		Items    []struct {
			ID          string   `json:"id"`
			Description string   `json:"description"`
			SourceTypes []string `json:"source_types,omitempty"`
			TargetTypes []string `json:"target_types,omitempty"`
			Symmetric   bool     `json:"symmetric,omitempty"`
			InverseOf   string   `json:"inverse_of,omitempty"`
		} `json:"items"`
	} `json:"relations"`
	RelationAliases        map[string]string `json:"relation_aliases"`
	RelationInverseAliases map[string]string `json:"relation_inverse_aliases"`
	RelationRules          []struct {
		Relation                   string   `json:"relation"`
		SourceRoles                []string `json:"source_roles,omitempty"`
		TargetRoles                []string `json:"target_roles,omitempty"`
		ForbiddenSourceRoles       []string `json:"forbidden_source_roles,omitempty"`
		ForbiddenTargetRoles       []string `json:"forbidden_target_roles,omitempty"`
		SourceDomainTypes          []string `json:"source_domain_types,omitempty"`
		TargetDomainTypes          []string `json:"target_domain_types,omitempty"`
		ForbiddenSourceDomainTypes []string `json:"forbidden_source_domain_types,omitempty"`
		ForbiddenTargetDomainTypes []string `json:"forbidden_target_domain_types,omitempty"`
		ForbiddenSourceStatuses    []string `json:"forbidden_source_statuses,omitempty"`
		ForbiddenTargetStatuses    []string `json:"forbidden_target_statuses,omitempty"`
		CanFlipDirection           bool     `json:"can_flip_direction,omitempty"`
	} `json:"relation_rules"`
	Canonicalization CanonicalizationRules `json:"canonicalization"`
}

//go:embed ontology.json
var defaultOntologyJSON []byte

// ── Backwards-compatible package-level state ───────────────────
//
// These are populated at init from the embedded JSON. Existing callers
// (across the codebase) reference these vars directly, so we preserve the
// names while the *content* now comes from data, not source code.
var (
	PredefinedBaseTypes       []BaseType
	BaseTypeSet               map[string]bool
	PredefinedFunctionalRoles []string
	FunctionalRoleSet         map[string]bool
	PredefinedStatuses        []string
	StatusSet                 map[string]bool
	PredefinedEdgeStatuses    []string
	EdgeStatusSet             map[string]bool
	PredefinedRelations       []RelationFamily
	RelationIndex             map[string]*RelationDef
	RelationAliasIndex        map[string]string
	// RelationInverseAliasIndex maps raw LLM relation names whose canonical
	// is the inverse direction. Callers must swap (from, to) endpoints when
	// resolving via this map.
	RelationInverseAliasIndex map[string]string
	RelationRules             map[string]RelationRule
	Canonicalization          CanonicalizationRules
)

func init() {
	if err := LoadFromBytes(defaultOntologyJSON); err != nil {
		log.Printf("schema: failed to load embedded ontology.json: %v", err)
	}
}

// LoadFromBytes replaces the active ontology with the one encoded in data.
// Used by tests and any caller that wants to swap domains at runtime.
func LoadFromBytes(data []byte) error {
	var raw ontologyJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode ontology json: %w", err)
	}
	return applyOntology(raw)
}

func applyOntology(raw ontologyJSON) error {
	baseTypes := make([]BaseType, len(raw.BaseTypes))
	baseTypeSet := make(map[string]bool, len(raw.BaseTypes))
	for i, bt := range raw.BaseTypes {
		baseTypes[i] = BaseType{ID: bt.ID, Description: bt.Description}
		baseTypeSet[bt.ID] = true
	}

	roleSet := make(map[string]bool, len(raw.FunctionalRoles))
	for _, r := range raw.FunctionalRoles {
		roleSet[r] = true
	}

	statusSet := make(map[string]bool, len(raw.Statuses.Entity))
	for _, s := range raw.Statuses.Entity {
		statusSet[s] = true
	}
	edgeStatusSet := make(map[string]bool, len(raw.Statuses.Edge))
	for _, s := range raw.Statuses.Edge {
		edgeStatusSet[s] = true
	}

	relations := make([]RelationFamily, len(raw.Relations))
	relationIndex := map[string]*RelationDef{}
	for i, fam := range raw.Relations {
		f := RelationFamily{Category: fam.Category}
		f.Relations = make([]RelationDef, len(fam.Items))
		for j, it := range fam.Items {
			f.Relations[j] = RelationDef{
				ID:          it.ID,
				Description: it.Description,
				SourceTypes: it.SourceTypes,
				TargetTypes: it.TargetTypes,
				Symmetric:   it.Symmetric,
				InverseOf:   it.InverseOf,
			}
		}
		relations[i] = f
	}
	// Index after the slice is materialised so the pointers stay valid.
	for fi := range relations {
		for ri := range relations[fi].Relations {
			rel := &relations[fi].Relations[ri]
			relationIndex[rel.ID] = rel
		}
	}

	rules := make(map[string]RelationRule, len(raw.RelationRules))
	for _, r := range raw.RelationRules {
		rules[r.Relation] = RelationRule{
			SourceRoles:                r.SourceRoles,
			TargetRoles:                r.TargetRoles,
			ForbiddenSourceRoles:       r.ForbiddenSourceRoles,
			ForbiddenTargetRoles:       r.ForbiddenTargetRoles,
			SourceDomainTypes:          r.SourceDomainTypes,
			TargetDomainTypes:          r.TargetDomainTypes,
			ForbiddenSourceDomainTypes: r.ForbiddenSourceDomainTypes,
			ForbiddenTargetDomainTypes: r.ForbiddenTargetDomainTypes,
			ForbiddenSourceStatuses:    r.ForbiddenSourceStatuses,
			ForbiddenTargetStatuses:    r.ForbiddenTargetStatuses,
			CanFlipDirection:           r.CanFlipDirection,
		}
	}

	// Commit atomically — replace package vars only after every section
	// decoded successfully.
	PredefinedBaseTypes = baseTypes
	BaseTypeSet = baseTypeSet
	PredefinedFunctionalRoles = append([]string(nil), raw.FunctionalRoles...)
	FunctionalRoleSet = roleSet
	PredefinedStatuses = append([]string(nil), raw.Statuses.Entity...)
	StatusSet = statusSet
	PredefinedEdgeStatuses = append([]string(nil), raw.Statuses.Edge...)
	EdgeStatusSet = edgeStatusSet
	PredefinedRelations = relations
	RelationIndex = relationIndex
	RelationAliasIndex = raw.RelationAliases
	if RelationAliasIndex == nil {
		RelationAliasIndex = map[string]string{}
	}
	RelationInverseAliasIndex = raw.RelationInverseAliases
	if RelationInverseAliasIndex == nil {
		RelationInverseAliasIndex = map[string]string{}
	}
	RelationRules = rules
	Canonicalization = raw.Canonicalization
	return nil
}

// IsValidBaseType checks if a type is a known base type.
func IsValidBaseType(t string) bool { return BaseTypeSet[t] }

// IsValidFunctionalRole checks if a role is in the controlled vocabulary.
func IsValidFunctionalRole(r string) bool { return FunctionalRoleSet[r] }

// IsValidStatus checks if a status is in the entity-status vocabulary.
func IsValidStatus(s string) bool { return StatusSet[s] }

// IsValidEdgeStatus checks if a status is in the edge-status vocabulary.
func IsValidEdgeStatus(s string) bool { return EdgeStatusSet[s] }

// GetRelationRule returns the constraint rule for a relation ID, or nil.
func GetRelationRule(relationID string) *RelationRule {
	if r, ok := RelationRules[relationID]; ok {
		return &r
	}
	return nil
}

// ResolveRelation normalises a raw LLM relation name to a canonical ID.
// "" return + ok=false means explicitly rejected by the alias table.
// Inverse aliases are resolved here too — callers should use
// ResolveRelationWithFlip when they need to know whether to swap endpoints.
func ResolveRelation(raw string) (string, bool) {
	canonical, ok, _ := ResolveRelationWithFlip(raw)
	return canonical, ok
}

// ResolveRelationWithFlip is the full resolver: returns the canonical ID,
// whether it was a known relation, and whether the caller should swap
// (from, to) endpoints because the raw name was an inverse alias (e.g.
// "MANAGED_BY" → ("MANAGES", true, true) means "the canonical is MANAGES
// but flip the endpoints").
func ResolveRelationWithFlip(raw string) (canonical string, known bool, flip bool) {
	if _, ok := RelationIndex[raw]; ok {
		return raw, true, false
	}
	if c, ok := RelationAliasIndex[raw]; ok {
		if c == "" {
			return "", false, false
		}
		return c, true, false
	}
	if c, ok := RelationInverseAliasIndex[raw]; ok && c != "" {
		return c, true, true
	}
	return raw, false, false
}

// GetRelationDef returns the definition for a relation ID, or nil if unknown.
func GetRelationDef(id string) *RelationDef { return RelationIndex[id] }

// AllRelationIDs returns all valid canonical relation IDs.
func AllRelationIDs() []string {
	ids := make([]string, 0, len(RelationIndex))
	for id := range RelationIndex {
		ids = append(ids, id)
	}
	return ids
}

// FormatFunctionalRolesForPrompt returns a prompt-ready functional roles list.
func FormatFunctionalRolesForPrompt() string {
	var lines []string
	for _, r := range PredefinedFunctionalRoles {
		lines = append(lines, "- "+r)
	}
	return joinWithNewlines(lines)
}

// FormatStatusesForPrompt returns a prompt-ready statuses list.
func FormatStatusesForPrompt() string {
	var lines []string
	for _, s := range PredefinedStatuses {
		lines = append(lines, "- "+s)
	}
	return joinWithNewlines(lines)
}

// FormatBaseTypesForPrompt returns a prompt-ready base type list.
func FormatBaseTypesForPrompt() string {
	var lines []string
	for _, bt := range PredefinedBaseTypes {
		lines = append(lines, "- "+bt.ID+": "+bt.Description)
	}
	return joinWithNewlines(lines)
}

// FormatRelationsForPrompt returns a prompt-ready relation list.
func FormatRelationsForPrompt() string {
	var lines []string
	for _, fam := range PredefinedRelations {
		lines = append(lines, "\n## "+fam.Category+":")
		for _, rel := range fam.Relations {
			constraint := ""
			if len(rel.SourceTypes) > 0 || len(rel.TargetTypes) > 0 {
				constraint = " ("
				if len(rel.SourceTypes) > 0 {
					constraint += joinComma(rel.SourceTypes)
				} else {
					constraint += "any"
				}
				constraint += " -> "
				if len(rel.TargetTypes) > 0 {
					constraint += joinComma(rel.TargetTypes)
				} else {
					constraint += "any"
				}
				constraint += ")"
			}
			lines = append(lines, "- "+rel.ID+": "+rel.Description+constraint)
		}
	}
	return joinWithNewlines(lines)
}

func joinWithNewlines(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += "\n"
		}
		result += s
	}
	return result
}

func joinComma(ss []string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}
