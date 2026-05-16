package schema

import (
	"log"
	"strings"
	"unicode"

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
	Direction         string   `json:"direction,omitempty"` // e.g. "organization -> person"
	SourceBaseTypes   []string `json:"source_base_types"`
	TargetBaseTypes   []string `json:"target_base_types"`
	Symmetric         bool     `json:"symmetric"`
	Aliases           []string `json:"aliases"`
	InverseAliases    []string `json:"inverse_aliases"`
	RejectAliases     []string `json:"reject_aliases,omitempty"`
}

// SchemaNormalization holds the complete normalization result from the LLM.
type SchemaNormalization struct {
	TypeRules      []TypeNormalizationRule      `json:"type_normalization"`
	RelationRules  []RelationNormalizationRule  `json:"relation_normalization"`
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

	// Apply type normalization rules
	for _, rule := range norm.TypeRules {
		canonical := strings.ToLower(rule.CanonicalDomainType)
		if canonical == "" {
			continue
		}

		bases := make([]string, 0, len(rule.BaseTypes))
		for _, b := range rule.BaseTypes {
			bases = append(bases, strings.ToLower(b))
		}

		// Register the canonical domain type
		if len(bases) == 1 {
			s.RegisterDomainType(canonical, rule.Notes, bases[0])
		} else if len(bases) > 1 {
			s.RegisterDomainTypeMultiBase(canonical, rule.Notes, bases)
		}

		// Register aliases
		for _, alias := range rule.Aliases {
			alias = strings.ToLower(alias)
			if alias != canonical {
				s.RegisterTypeAlias(alias, canonical)
			}
		}
	}

	// Apply relation normalization rules
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

		// Register the canonical relation
		s.AddRelationType(RelationType{
			Name:        canonical,
			Description: rule.Direction,
			SourceTypes: srcTypes,
			TargetTypes: tgtTypes,
			Symmetric:   rule.Symmetric,
		})

		// Register aliases (same-direction)
		for _, alias := range rule.Aliases {
			alias = strings.ToUpper(alias)
			if alias != canonical {
				s.RegisterRelationAlias(alias, canonical, false)
			}
		}

		// Register inverse aliases (need direction flip)
		for _, inv := range rule.InverseAliases {
			inv = strings.ToUpper(inv)
			if inv != canonical {
				s.RegisterRelationAlias(inv, canonical, true)
			}
		}

		// Register rejected aliases
		for _, rej := range rule.RejectAliases {
			rej = strings.ToUpper(rej)
			s.mu.Lock()
			s.relationAliases[rej] = RelationAliasInfo{Canonical: "__REJECTED__", Flip: false}
			s.mu.Unlock()
		}
	}

	// Register globally rejected relations
	for _, rej := range norm.RejectedRelations {
		rel := strings.ToUpper(rej.Relation)
		s.mu.Lock()
		s.relationAliases[rel] = RelationAliasInfo{Canonical: "__REJECTED__", Flip: false}
		s.mu.Unlock()
	}

	log.Printf("Schema normalization applied: %d type rules, %d relation rules, %d rejected relations",
		len(norm.TypeRules), len(norm.RelationRules), len(norm.RejectedRelations))
}

// RewriteEntities applies the normalized schema to entities:
// - Resolves type aliases to canonical
// - Detects role-like types on person-named entities → keeps person as base, role as domain
// - Ensures standalone role entities (not person-like) keep type "role"
func (s *Schema) RewriteEntities(entities []models.Entity) []models.Entity {
	result := make([]models.Entity, len(entities))
	roleFixed := 0
	roleKept := 0

	for i, e := range entities {
		result[i] = e

		// Resolve type through alias
		resolvedType := s.NormalizeEntityType(e.Type)
		result[i].Type = resolvedType

		// Fix HQ/headquarters entities — these are organizations, not locations
		if looksLikeHQ(e.Name) && (resolvedType == "location" || resolvedType == "") {
			result[i].BaseType = "organization"
			result[i].Type = "organization"
			continue
		}

		// Check if this is a role-like type (via schema)
		et := s.GetEntityType(resolvedType)
		if et != nil && isRoleBaseType(et, s) {
			if looksLikePersonName(e.Name) {
				// Person with a role type → keep as person, role becomes domain_type
				result[i].BaseType = "person"
				result[i].DomainType = resolvedType
				result[i].Type = "person"
				roleFixed++
			} else {
				// Standalone role entity (job title, position) → type is "role"
				result[i].BaseType = "role"
				result[i].DomainType = resolvedType
				result[i].Type = "role"
				roleKept++
			}
		} else if looksLikeRoleName(e.Name) && !looksLikePersonName(e.Name) {
			// Name-based role detection: catches profession phrases typed as "person"
			// e.g. "diabetes educator", "mental wellness counselor", "visiting dermatologist"
			result[i].BaseType = "role"
			result[i].DomainType = resolvedType
			result[i].Type = "role"
			roleKept++
		} else if et != nil {
			// Set base type from schema
			if len(et.BaseTypes) > 0 {
				result[i].BaseType = et.BaseTypes[0]
			} else if et.ParentType != "" && s.IsBaseType(et.ParentType) {
				result[i].BaseType = et.ParentType
			}
			if et.DomainType {
				result[i].DomainType = resolvedType
			}
		}
	}

	if roleFixed > 0 || roleKept > 0 {
		log.Printf("Schema rewrite: %d person+role entities, %d standalone role entities", roleFixed, roleKept)
	}
	return result
}

// RewriteTriples applies the normalized schema to triples:
// - Removes ALIAS_OF triples (aliases belong in entity standardization, not the graph)
// - Resolves relation aliases
// - Flips inverse relations
// - Fixes HAS_ROLE target typing (role entities that got rewritten to "person")
// - Resolves entity type aliases
// - Rejects triples with rejected relations
// - Validates base-type constraints
func (s *Schema) RewriteTriples(triples []models.Triple, entityMap map[string]string) []models.Triple {
	result := make([]models.Triple, 0, len(triples))
	normalized := 0
	flipped := 0
	rejected := 0
	aliasRemoved := 0
	roleFixed := 0

	for _, t := range triples {
		// Remove ALIAS_OF triples — entity aliases are handled by standardization, not as edges
		if strings.ToUpper(t.Edge) == "ALIAS_OF" {
			aliasRemoved++
			continue
		}

		// Reject triples where an endpoint is typed as "alias" — these should have been rewritten
		if entityMap[t.Node1] == "alias" || entityMap[t.Node2] == "alias" {
			rejected++
			continue
		}

		// Apply entity types
		if mapType, ok := entityMap[t.Node1]; ok && mapType != "" {
			t.Node1Type = s.NormalizeEntityType(mapType)
		} else {
			t.Node1Type = s.NormalizeEntityType(t.Node1Type)
		}
		if mapType, ok := entityMap[t.Node2]; ok && mapType != "" {
			t.Node2Type = s.NormalizeEntityType(mapType)
		} else {
			t.Node2Type = s.NormalizeEntityType(t.Node2Type)
		}

		// Fix HAS_ROLE target typing: if the target entity was role-rewritten to "person",
		// the HAS_ROLE triple should still point to type "role"
		if strings.ToUpper(t.Edge) == "HAS_ROLE" && t.Node2Type == "person" {
			// The target of HAS_ROLE is a role entity — fix its type
			t.Node2Type = "role"
			roleFixed++
		}

		// Resolve relation
		canonical, shouldFlip := s.NormalizeTripleRelation(t.Edge)

		// Check if rejected
		if canonical == "__REJECTED__" {
			rejected++
			continue
		}

		if canonical != t.Edge {
			t.Edge = canonical
			normalized++
		}
		if shouldFlip {
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			flipped++
		}

		// Strict relation-level enforcement rules
		edge := strings.ToUpper(t.Edge)
		srcBase := s.ResolveBaseType(t.Node1Type)
		tgtBase := s.ResolveBaseType(t.Node2Type)

		if !applyRelationEnforcement(edge, srcBase, tgtBase, &t) {
			rejected++
			continue
		}

		// Re-read after enforcement may have flipped
		srcBase = s.ResolveBaseType(t.Node1Type)
		tgtBase = s.ResolveBaseType(t.Node2Type)

		// Validate base-type constraints from schema
		rt := s.GetRelationType(t.Edge)
		if rt != nil && len(rt.SourceTypes) > 0 && len(rt.TargetTypes) > 0 {
			if srcBase != "" && tgtBase != "" {
				srcOK := containsType(rt.SourceTypes, srcBase) || containsType(rt.SourceTypes, t.Node1Type)
				tgtOK := containsType(rt.TargetTypes, tgtBase) || containsType(rt.TargetTypes, t.Node2Type)
				if !srcOK || !tgtOK {
					// Try flipping
					flipSrcOK := containsType(rt.SourceTypes, tgtBase) || containsType(rt.SourceTypes, t.Node2Type)
					flipTgtOK := containsType(rt.TargetTypes, srcBase) || containsType(rt.TargetTypes, t.Node1Type)
					if flipSrcOK && flipTgtOK {
						t.Node1, t.Node2 = t.Node2, t.Node1
						t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
						flipped++
					} else {
						// Either side violates and flip doesn't fix — reject
						rejected++
						continue
					}
				}
			}
		}

		result = append(result, t)
	}

	log.Printf("Schema rewrite triples: %d normalized, %d flipped, %d rejected, %d alias_of removed, %d role-types fixed (of %d)",
		normalized, flipped, rejected, aliasRemoved, roleFixed, len(triples))
	return result
}

// applyRelationEnforcement applies hard-coded semantic rules for specific relations.
// Returns false if the triple should be rejected entirely.
func applyRelationEnforcement(edge, srcBase, tgtBase string, t *models.Triple) bool {
	switch edge {
	case "OFFERS", "PROVIDES":
		// Target must be service-like. Source must be organization-like.
		if tgtBase != "" && tgtBase != "service" && tgtBase != "product" {
			// Target is not a service — reject
			return false
		}
		if srcBase == "person" || srcBase == "location" || srcBase == "role" {
			// Person/location/role cannot offer services
			return false
		}

	case "PART_OF", "BELONGS_TO":
		// Must be child → parent (smaller entity → larger entity)
		// If both are organizations, try to detect direction issues
		if srcBase != "" && tgtBase != "" {
			// Location cannot be PART_OF an organization (use LOCATED_IN)
			if srcBase == "location" && tgtBase == "organization" {
				return false
			}
			// Person cannot be PART_OF anything (use WORKS_AT/BASED_AT)
			if srcBase == "person" {
				return false
			}
		}

	case "LOCATED_IN", "LOCATED_AT":
		// Target must be location or address. Source must not be person.
		if tgtBase != "" && tgtBase != "location" && tgtBase != "address" {
			// If source is location/address and target isn't, try flipping
			if srcBase == "location" || srcBase == "address" {
				t.Node1, t.Node2 = t.Node2, t.Node1
				t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
				return true
			}
			return false
		}
		if srcBase == "person" {
			// Person uses BASED_AT/WORKS_AT, not LOCATED_IN
			return false
		}

	case "HAS_ROLE":
		// Source must be person, target must be role
		if srcBase != "" && srcBase != "person" {
			// If target is person and source is role, flip
			if tgtBase == "person" && srcBase == "role" {
				t.Node1, t.Node2 = t.Node2, t.Node1
				t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
				return true
			}
			return false
		}
		if tgtBase != "" && tgtBase != "role" {
			return false
		}

	case "OPERATES", "HAS_BRANCH":
		// OPERATES: parent org → child org/branch
		// Both should be organizations. If child→parent detected, flip.
		if srcBase == "organization" && tgtBase == "organization" {
			// Use name heuristics: if target name contains source name prefix, it's likely child→parent (wrong)
			srcLower := strings.ToLower(t.Node1)
			tgtLower := strings.ToLower(t.Node2)
			// If source looks like a branch name (shorter, subset of target), flip
			if len(srcLower) < len(tgtLower) && strings.Contains(tgtLower, strings.Fields(srcLower)[0]) {
				// Source is shorter/less specific — likely branch→network, flip
				t.Node1, t.Node2 = t.Node2, t.Node1
				t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
				return true
			}
		}
		if srcBase == "person" || srcBase == "role" || srcBase == "service" {
			return false
		}

	case "MANAGES", "MANAGED_BY":
		// MANAGES: person → organization (or person → person for team leads)
		// Source must be person
		if edge == "MANAGES" && srcBase != "" && srcBase != "person" {
			// If target is person, flip
			if tgtBase == "person" {
				t.Node1, t.Node2 = t.Node2, t.Node1
				t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
				return true
			}
			return false
		}
	}

	return true
}

// isRoleBaseType checks if an entity type is role-like (base_type is "role").
func isRoleBaseType(et *EntityType, s *Schema) bool {
	if et == nil {
		return false
	}
	// Direct check
	if et.ParentType == "role" {
		return true
	}
	for _, b := range et.BaseTypes {
		if b == "role" {
			return true
		}
	}
	// Check via resolution
	resolved := s.ResolveBaseType(et.Name)
	return resolved == "role"
}

// looksLikePersonName uses heuristics to detect person names.
func looksLikePersonName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}

	lower := strings.ToLower(name)

	// Check common person name prefixes
	prefixes := []string{"dr.", "dr ", "prof.", "prof ", "mr.", "mr ", "mrs.", "mrs ", "ms.", "ms "}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}

	// Reject role-like names (job titles, positions) — these are NOT person names
	roleWords := []string{"officer", "manager", "director", "supervisor", "coordinator",
		"specialist", "assistant", "administrator", "consultant", "therapist",
		"nurse", "technician", "chief", "head", "lead", "senior", "junior",
		"deputy", "associate", "practitioner", "surgeon", "physician", "dentist",
		"pharmacist", "physiotherapist", "radiologist", "pathologist", "anesthetist"}
	for _, rw := range roleWords {
		if strings.Contains(lower, rw) {
			return false
		}
	}

	// Reject non-person words (organizations, places, things)
	nonPersonWords := []string{"clinic", "hospital", "center", "branch", "service", "network",
		"lab", "pharmacy", "institute", "foundation", "portal", "system",
		"department", "unit", "ward", "team", "group", "committee"}
	for _, npw := range nonPersonWords {
		if strings.Contains(lower, npw) {
			return false
		}
	}

	// Check if it's 2-4 words, each starting with uppercase (typical name pattern)
	words := strings.Fields(name)
	if len(words) < 2 || len(words) > 4 {
		return false
	}

	allCapitalized := true
	for _, w := range words {
		if len(w) == 0 {
			continue
		}
		first := rune(w[0])
		if !unicode.IsUpper(first) && !unicode.IsLetter(first) {
			allCapitalized = false
			break
		}
	}

	return allCapitalized
}

// looksLikeHQ detects headquarters/HQ entity names that should be typed "organization" not "location".
func looksLikeHQ(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, " hq") || strings.HasSuffix(lower, " hq") ||
		strings.Contains(lower, "headquarters") || strings.Contains(lower, "head office") ||
		strings.HasPrefix(lower, "hq ")
}

// looksLikeRoleName detects generic job/profession phrases that should be typed "role".
// These are multi-word phrases containing role indicators but NO personal name component.
// Examples: "diabetes educator", "mental wellness counselor", "visiting dermatologist"
func looksLikeRoleName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}

	// Reject document/system/directory-like names — NOT roles
	nonRoleWords := []string{"directory", "catalog", "registry", "database", "system",
		"platform", "portal", "index", "report", "document", "manual", "guide",
		"handbook", "protocol", "policy", "schedule", "calendar", "log", "list"}
	for _, nrw := range nonRoleWords {
		if strings.Contains(lower, nrw) {
			return false
		}
	}

	// Must contain at least one role-indicator word
	roleIndicators := []string{
		"officer", "manager", "director", "supervisor", "coordinator",
		"specialist", "assistant", "administrator", "consultant", "therapist",
		"nurse", "technician", "chief", "head", "lead", "senior", "junior",
		"deputy", "associate", "practitioner", "surgeon", "physician", "dentist",
		"pharmacist", "physiotherapist", "radiologist", "pathologist", "anesthetist",
		"counselor", "counsellor", "educator", "instructor", "trainer",
		"executive", "analyst", "engineer", "architect", "planner",
		"dermatologist", "cardiologist", "oncologist", "neurologist", "psychologist",
		"psychiatrist", "pediatrician", "obstetrician", "gynecologist",
	}

	hasRoleWord := false
	for _, rw := range roleIndicators {
		if strings.Contains(lower, rw) {
			hasRoleWord = true
			break
		}
	}

	if !hasRoleWord {
		return false
	}

	// Reject if it also looks like a named person (e.g. "Dr. John Smith" or "nurse coordinator yossi cohen")
	// A named person typically has a proper name after the role word.
	// Simple heuristic: if the phrase is 1-3 words and ALL are common/role words, it's a role.
	words := strings.Fields(lower)
	if len(words) > 4 {
		return false
	}

	return true
}
