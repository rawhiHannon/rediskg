package graph

import (
	"log"
	"regexp"
	"strings"

	"rediskg/pkg/models"
)

// canonicalRelations maps loose relation names to canonical relation types.
var canonicalRelations = map[string]string{
	// Person → Organization
	"works_at": "WORKS_AT", "works at": "WORKS_AT", "employed_at": "WORKS_AT", "based_at": "WORKS_AT",
	"visits": "VISITS", "visiting": "VISITS",

	// Organization → Person
	"managed_by": "MANAGED_BY", "manages": "MANAGED_BY", "branch_manager": "MANAGED_BY", "manager_of": "MANAGED_BY",
	"deputy_manager": "DEPUTY_MANAGER", "deputy_managed_by": "DEPUTY_MANAGER", "assistant_manager": "DEPUTY_MANAGER",
	"founded_by": "FOUNDED_BY", "founder": "FOUNDED_BY",

	// Organization → Service
	"offers": "OFFERS_SERVICE", "provides": "OFFERS_SERVICE", "has_service": "OFFERS_SERVICE",
	"offers_service": "OFFERS_SERVICE", "available_at": "OFFERS_SERVICE", "currently_offers": "OFFERS_SERVICE",
	"does_not_offer": "DOES_NOT_OFFER", "does_not_provide": "DOES_NOT_OFFER",
	"not_available": "DOES_NOT_OFFER", "unavailable_at": "DOES_NOT_OFFER",
	"does_not_offer_service": "DOES_NOT_OFFER",

	// Organization → Organization
	"has_partner": "HAS_PARTNER", "partners_with": "HAS_PARTNER", "partner_of": "HAS_PARTNER",
	"contracted_with": "CONTRACTED_WITH", "has_agreement": "CONTRACTED_WITH",
	"no_contract": "NO_CONTRACT",
	"subsidiary_of": "PART_OF", "part_of": "PART_OF", "belongs_to": "PART_OF",
	"has_branch": "HAS_BRANCH", "branch_of": "PART_OF",
	"headquarters_at": "HAS_HEADQUARTERS", "headquartered_at": "HAS_HEADQUARTERS",
	"has_headquarters": "HAS_HEADQUARTERS",
	"has_planned_branch": "HAS_PLANNED_BRANCH", "planned_branch": "HAS_PLANNED_BRANCH",

	// Person → Person
	"reports_to": "REPORTS_TO", "supervises": "REPORTS_TO",
	"referred_by": "REFERRED_BY",

	// Person → Service/Role
	"specializes_in": "SPECIALIZES_IN", "specialization": "SPECIALIZES_IN",
	"role": "HAS_ROLE", "has_role": "HAS_ROLE", "position": "HAS_ROLE",
	"role_at": "WORKS_AT",

	// Person → Event
	"involved_in": "INVOLVED_IN",

	// Organization → Location/Address
	"located_in": "LOCATED_IN", "location": "LOCATED_IN",
	"located_at": "LOCATED_AT",

	// Organization → Technology
	"uses_technology": "USES_TECHNOLOGY", "uses": "USES_TECHNOLOGY",

	// Entity → Entity
	"alias_of": "ALIAS_OF", "also_known_as": "ALIAS_OF", "aka": "ALIAS_OF",

	// Catch-all
	"related_to": "RELATED_TO",
}

// directionRules defines expected (sourceType, targetType) for each canonical relation.
type directionRule struct {
	sourceTypes []string
	targetTypes []string
}

var directionRules = map[string]directionRule{
	"WORKS_AT":         {sourceTypes: []string{"person"}, targetTypes: []string{"organization"}},
	"MANAGED_BY":       {sourceTypes: []string{"organization"}, targetTypes: []string{"person"}},
	"DEPUTY_MANAGER":   {sourceTypes: []string{"organization"}, targetTypes: []string{"person"}},
	"VISITS":           {sourceTypes: []string{"person"}, targetTypes: []string{"organization"}},
	"OFFERS_SERVICE":   {sourceTypes: []string{"organization"}, targetTypes: []string{"service"}},
	"DOES_NOT_OFFER":   {sourceTypes: []string{"organization"}, targetTypes: []string{"service"}},
	"HAS_PARTNER":      {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"CONTRACTED_WITH":  {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"NO_CONTRACT":      {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"REPORTS_TO":       {sourceTypes: []string{"person"}, targetTypes: []string{"person"}},
	"SPECIALIZES_IN":   {sourceTypes: []string{"person"}, targetTypes: []string{"service"}},
	"HAS_ROLE":         {sourceTypes: []string{"person"}, targetTypes: []string{"role", "concept"}},
	"LOCATED_IN":       {sourceTypes: []string{"organization", "address"}, targetTypes: []string{"location"}},
	"LOCATED_AT":       {sourceTypes: []string{"organization"}, targetTypes: []string{"address"}},
	"FOUNDED_BY":       {sourceTypes: []string{"organization"}, targetTypes: []string{"person"}},
	"HAS_BRANCH":       {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"INVOLVED_IN":      {sourceTypes: []string{"person"}, targetTypes: []string{"event"}},
	"USES_TECHNOLOGY":  {sourceTypes: []string{"organization"}, targetTypes: []string{"technology"}},
	"REFERRED_BY":      {sourceTypes: []string{"person"}, targetTypes: []string{"person"}},
	"HAS_HEADQUARTERS":  {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"HAS_PLANNED_BRANCH": {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
}

// Known role titles — used to detect person->ALIAS_OF->role and convert to HAS_ROLE
var roleTitles = map[string]bool{
	"ceo": true, "cfo": true, "coo": true, "cmo": true, "cto": true, "cio": true,
	"chief executive officer": true, "chief financial officer": true,
	"chief operations officer": true, "chief medical officer": true,
	"chief technology officer": true, "chief information officer": true,
	"head of digital systems": true, "head of operations": true,
	"head of finance": true, "head of hr": true, "head of it": true,
	"branch manager": true, "deputy manager": true, "assistant manager": true,
	"manager": true, "director": true, "supervisor": true,
}


// Patterns for deterministic type correction
var (
	addressPatternEN = regexp.MustCompile(`(?i)\d+\s+\w+\s+(street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|way|plaza|square)`)
	addressPatternHE = regexp.MustCompile(`(?i)(רחוב|רח'|שד'|שדרות)\s+`)
	incidentPattern  = regexp.MustCompile(`(?i)^incident\s`)
)

// PropagateTypesFromTriples infers entity types from how entities are used in relations.
// For example, if entity X appears as the target of LOCATED_IN, X is a location.
// This replaces hardcoded city/location lists with structural inference.
func PropagateTypesFromTriples(triples []models.Triple, entityMap map[string]string, locked map[string]bool) {
	// First pass: normalize relation names so we can reason about them
	normalizedEdges := make([]string, len(triples))
	for i, t := range triples {
		normalizedEdges[i] = normalizeRelation(t.Edge)
	}

	// Collect type votes from relation structure
	// Each relation implies types for its source and target
	votes := map[string]map[string]int{} // entity -> type -> count
	addVote := func(entity, typ string) {
		if entity == "" || typ == "" {
			return
		}
		if votes[entity] == nil {
			votes[entity] = map[string]int{}
		}
		votes[entity][typ]++
	}

	for i, t := range triples {
		edge := normalizedEdges[i]
		rule, ok := directionRules[edge]
		if !ok {
			continue
		}
		// If a relation has exactly one expected source type, vote for it
		if len(rule.sourceTypes) == 1 {
			addVote(t.Node1, rule.sourceTypes[0])
		}
		if len(rule.targetTypes) == 1 {
			addVote(t.Node2, rule.targetTypes[0])
		}
	}

	// Apply votes: only override if the entity has no type or the structural
	// evidence is strong (2+ relations agree)
	propagated := 0
	for entity, typeCounts := range votes {
		currentType := entityMap[entity]

		bestType := ""
		bestCount := 0
		for t, count := range typeCounts {
			if count > bestCount {
				bestType = t
				bestCount = count
			}
		}

		if bestType == "" {
			continue
		}

		// Never override deterministically locked types (dr., incident, address patterns, etc.)
		if locked[entity] {
			continue
		}

		// Fill missing types unconditionally
		if currentType == "" {
			entityMap[entity] = bestType
			propagated++
			continue
		}

		// Override existing type only with evidence, applying different thresholds:
		// - "concept" is the weakest LLM type: override with 1+ structural vote
		// - Other types need 2+ votes to override
		// - Never override "person" — it's usually correct from LLM
		if currentType != bestType {
			if currentType == "person" {
				continue
			}
			threshold := 2
			if currentType == "concept" {
				threshold = 1
			}
			if bestCount >= threshold {
				entityMap[entity] = bestType
				propagated++
			}
		}
	}

	if propagated > 0 {
		log.Printf("Type propagation: inferred %d entity types from graph structure", propagated)
	}
}

// ValidateAndNormalizeTriples normalizes relation names, fixes entity types
// using deterministic rules, and corrects edge direction.
func ValidateAndNormalizeTriples(triples []models.Triple, entityMap map[string]string) []models.Triple {
	// Fix entity types deterministically in the entity map
	CorrectEntityTypes(entityMap)

	var result []models.Triple
	flipped := 0
	normalized := 0
	rejected := 0
	typeCorrected := 0
	converted := 0
	aliasFixed := 0

	for _, t := range triples {
		// ALWAYS override triple types with entityMap types.
		// The entityMap has been corrected by deterministic rules and propagation.
		// The LLM types on triples are often wrong (e.g. "metrolab diagnostics" as person).
		if mapType, ok := entityMap[t.Node1]; ok && mapType != "" {
			t.Node1Type = mapType
		}
		if mapType, ok := entityMap[t.Node2]; ok && mapType != "" {
			t.Node2Type = mapType
		}

		// Normalize relation name
		edge := normalizeRelation(t.Edge)
		if edge != t.Edge {
			normalized++
		}
		t.Edge = edge

		// Normalize entity types
		t.Node1Type = strings.ToLower(strings.TrimSpace(t.Node1Type))
		t.Node2Type = strings.ToLower(strings.TrimSpace(t.Node2Type))

		// Apply deterministic type corrections based on name patterns
		n1Fixed := inferEntityType(t.Node1, t.Node1Type)
		n2Fixed := inferEntityType(t.Node2, t.Node2Type)
		if n1Fixed != t.Node1Type || n2Fixed != t.Node2Type {
			typeCorrected++
		}
		t.Node1Type = n1Fixed
		t.Node2Type = n2Fixed

		// Fill missing types from relation (ONLY fill empty, never override)
		t = fillMissingTypesFromRelation(t)

		// Update entity map with corrected types
		if t.Node1Type != "" {
			entityMap[t.Node1] = t.Node1Type
		}
		if t.Node2Type != "" {
			entityMap[t.Node2] = t.Node2Type
		}

		// Drop RELATED_TO — too vague
		if t.Edge == "RELATED_TO" {
			rejected++
			continue
		}

		// Fix bad ALIAS_OF edges
		fixed, ok := fixBadAlias(t)
		if !ok {
			rejected++
			continue
		}
		if fixed.Edge != t.Edge {
			aliasFixed++
		}
		t = fixed

		// Convert PART_OF to HAS_BRANCH by flipping (child→parent becomes parent→child)
		if t.Edge == "PART_OF" {
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			t.Edge = "HAS_BRANCH"
			converted++
		}

		// If HAS_BRANCH target looks like HQ, convert to HAS_HEADQUARTERS
		if t.Edge == "HAS_BRANCH" && isHQNode(t.Node2) {
			t.Edge = "HAS_HEADQUARTERS"
		}

		// Check direction and flip if needed
		if shouldFlip(t) {
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			flipped++
		}

		// Guard: USES_TECHNOLOGY target must actually be technology, not an org/person/event
		if t.Edge == "USES_TECHNOLOGY" && !looksLikeTechnology(t.Node2) {
			rejected++
			continue
		}

		// Strict validation: reject edges with unknown relations or missing types
		if !isValidEdge(t) {
			rejected++
			continue
		}

		result = append(result, t)
	}

	// Post-pass: convert parent-child HAS_PARTNER to HAS_BRANCH
	result = fixParentChildPartner(result)

	// Post-pass: limit MANAGED_BY to 1 per org, convert extras to WORKS_AT
	result, demoted := limitManagedBy(result)

	// Post-pass: resolve contradictions (NO_CONTRACT removes HAS_PARTNER/CONTRACTED_WITH)
	result, contradictions := resolveContradictions(result)

	// Post-pass: HAS_PLANNED_BRANCH trumps HAS_BRANCH for the same pair
	result, plannedDedup := deduplicatePlannedBranches(result)

	log.Printf("Validation: normalized %d, type-corrected %d, flipped %d, converted %d PART_OF→HAS_BRANCH, alias-fixed %d, demoted %d MANAGED_BY→WORKS_AT, contradictions %d, planned-dedup %d, rejected %d (of %d total)",
		normalized, typeCorrected, flipped, converted, aliasFixed, demoted, contradictions, plannedDedup, rejected, len(triples))

	return result
}

// fixBadAlias detects and corrects misused ALIAS_OF edges.
// Returns (fixed triple, keep). If keep is false, the triple should be dropped.
func fixBadAlias(t models.Triple) (models.Triple, bool) {
	if t.Edge != "ALIAS_OF" {
		return t, true
	}

	// person -> ALIAS_OF -> role title → convert to HAS_ROLE
	if t.Node1Type == "person" && isRoleTitle(t.Node2) {
		t.Edge = "HAS_ROLE"
		t.Node2Type = "role"
		return t, true
	}

	// role title -> ALIAS_OF -> person → convert to HAS_ROLE (flipped)
	if t.Node2Type == "person" && isRoleTitle(t.Node1) {
		t.Edge = "HAS_ROLE"
		t.Node1, t.Node2 = t.Node2, t.Node1
		t.Node1Type, t.Node2Type = "person", "role"
		return t, true
	}

	// HQ -> ALIAS_OF -> organization → drop (HQ relationships handled elsewhere)
	if isHQNode(t.Node1) || isHQNode(t.Node2) {
		return t, false
	}

	// Cross-type aliases are invalid — reject them
	// (e.g., technology -> ALIAS_OF -> organization would corrupt types)
	if t.Node1Type != "" && t.Node2Type != "" && t.Node1Type != t.Node2Type {
		return t, false
	}

	// Valid ALIAS_OF: same type or one/both types empty.
	// Ensure direction: shorter/abbreviation → canonical (longer name).
	if len(t.Node1) > len(t.Node2) {
		t.Node1, t.Node2 = t.Node2, t.Node1
		t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
	}
	return t, true
}

// isRoleTitle checks if a name is a known role/title.
func isRoleTitle(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if roleTitles[lower] {
		return true
	}
	// Check for "head of X" pattern
	if strings.HasPrefix(lower, "head of ") {
		return true
	}
	// Check for "chief X officer" pattern
	if strings.HasPrefix(lower, "chief ") && strings.HasSuffix(lower, " officer") {
		return true
	}
	return false
}

// isExecutiveRole checks if a role name is a C-suite/executive-level title.
func isExecutiveRole(role string) bool {
	lower := strings.ToLower(role)
	executiveTitles := []string{
		"ceo", "cfo", "coo", "cmo", "cto", "cio",
		"chief ", "president", "vice president", "vp ",
		"head of ", "director of ",
	}
	for _, t := range executiveTitles {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// fixParentChildPartner converts HAS_PARTNER to HAS_BRANCH when one entity name
// contains the other, indicating a parent-child relationship rather than a peer partnership.
func fixParentChildPartner(triples []models.Triple) []models.Triple {
	for i, t := range triples {
		if t.Edge != "HAS_PARTNER" {
			continue
		}
		n1 := strings.ToLower(t.Node1)
		n2 := strings.ToLower(t.Node2)
		// If one name is a prefix/substring of the other, they're parent-child
		if n1 != n2 && (strings.Contains(n2, n1) || strings.Contains(n1, n2)) {
			// The shorter name is the parent
			if len(n1) <= len(n2) {
				// node1 is parent, node2 is child → HAS_BRANCH direction is correct
				triples[i].Edge = "HAS_BRANCH"
			} else {
				// node2 is parent, node1 is child → flip
				triples[i].Node1, triples[i].Node2 = t.Node2, t.Node1
				triples[i].Node1Type, triples[i].Node2Type = t.Node2Type, t.Node1Type
				triples[i].Edge = "HAS_BRANCH"
			}
		}
	}
	return triples
}

// limitManagedBy ensures at most 1 MANAGED_BY per organization and demotes
// executives (people with C-suite HAS_ROLE edges) from MANAGED_BY to WORKS_AT.
func limitManagedBy(triples []models.Triple) ([]models.Triple, int) {
	// First pass: identify people who have executive-level HAS_ROLE edges
	executives := map[string]bool{}
	for _, t := range triples {
		if t.Edge == "HAS_ROLE" && isExecutiveRole(t.Node2) {
			executives[t.Node1] = true
		}
	}

	// Second pass: limit MANAGED_BY and demote executives
	managedOrgs := map[string]bool{}
	demoted := 0

	result := make([]models.Triple, 0, len(triples))
	for _, t := range triples {
		if t.Edge == "MANAGED_BY" {
			// Executives should not be MANAGED_BY — they WORK_AT
			if executives[t.Node2] || managedOrgs[t.Node1] {
				result = append(result, models.Triple{
					Node1:     t.Node2,
					Node1Type: t.Node2Type,
					Node2:     t.Node1,
					Node2Type: t.Node1Type,
					Edge:      "WORKS_AT",
					ChunkID:   t.ChunkID,
					Weight:    t.Weight,
				})
				demoted++
				continue
			}
			managedOrgs[t.Node1] = true
		}
		result = append(result, t)
	}

	return result, demoted
}

// resolveContradictions removes edges that contradict negative-fact edges.
// For example, if NO_CONTRACT exists between A and B, then CONTRACTED_WITH,
// HAS_PARTNER, and USES_TECHNOLOGY edges between the same pair are removed.
func resolveContradictions(triples []models.Triple) ([]models.Triple, int) {
	// Collect negative-fact pairs (unordered)
	type pair struct{ a, b string }
	negativePairs := map[pair]bool{}
	for _, t := range triples {
		if t.Edge == "NO_CONTRACT" {
			negativePairs[pair{t.Node1, t.Node2}] = true
			negativePairs[pair{t.Node2, t.Node1}] = true
		}
	}

	if len(negativePairs) == 0 {
		return triples, 0
	}

	contradicts := map[string]bool{
		"CONTRACTED_WITH": true,
		"HAS_PARTNER":     true,
		"USES_TECHNOLOGY": true,
	}

	removed := 0
	result := make([]models.Triple, 0, len(triples))
	for _, t := range triples {
		if contradicts[t.Edge] && negativePairs[pair{t.Node1, t.Node2}] {
			removed++
			continue
		}
		result = append(result, t)
	}
	return result, removed
}

// deduplicatePlannedBranches removes HAS_BRANCH edges when HAS_PLANNED_BRANCH
// exists for the same (parent, child) pair. Planned status trumps active.
func deduplicatePlannedBranches(triples []models.Triple) ([]models.Triple, int) {
	type pair struct{ parent, child string }
	planned := map[pair]bool{}
	for _, t := range triples {
		if t.Edge == "HAS_PLANNED_BRANCH" {
			planned[pair{t.Node1, t.Node2}] = true
		}
	}

	if len(planned) == 0 {
		return triples, 0
	}

	removed := 0
	result := make([]models.Triple, 0, len(triples))
	for _, t := range triples {
		if t.Edge == "HAS_BRANCH" && planned[pair{t.Node1, t.Node2}] {
			removed++
			continue
		}
		result = append(result, t)
	}
	return result, removed
}

// CorrectEntityTypes applies deterministic type corrections to the entity map.
// Returns the set of entity names that were deterministically corrected (protected from propagation override).
func CorrectEntityTypes(entityMap map[string]string) map[string]bool {
	locked := map[string]bool{}
	for name, typ := range entityMap {
		fixed := inferEntityType(name, typ)
		if fixed != typ {
			entityMap[name] = fixed
		}
		// Lock any entity where inferEntityType returned a definitive type
		// (i.e., it matched a rule and didn't just return currentType unchanged).
		// This prevents propagation from overriding correct types.
		if fixed != typ || typeMatchedRule(name, fixed) {
			locked[name] = true
		}
	}
	return locked
}

// typeMatchedRule returns true if inferEntityType would match a specific rule for this name
// (not just fall through to returning currentType). Used to lock confirmed types.
func typeMatchedRule(name, typ string) bool {
	lower := strings.ToLower(name)
	switch typ {
	case "person":
		return hasProfessionalTitlePrefix(lower)
	case "address":
		return addressPatternEN.MatchString(name) || addressPatternHE.MatchString(name) || hasAddressIndicator(lower)
	case "event":
		return incidentPattern.MatchString(name)
	case "role":
		return isRoleTitle(lower)
	case "organization":
		return isHQNode(lower) || isFacilityByName(lower) || isOrgByName(lower)
	case "technology":
		return looksLikeTechnology(lower)
	}
	return false
}

// hasProfessionalTitlePrefix checks if a name starts with a professional title
// (dr., dietitian, physiotherapist, nurse, etc.) indicating a person.
func hasProfessionalTitlePrefix(lower string) bool {
	prefixes := []string{
		"dr.", "dr ", "prof.", "prof ",
		"dietitian ", "physiotherapist ", "pharmacist ", "therapist ",
		"nurse ", "midwife ", "dentist ", "optometrist ", "psychologist ",
		"psychiatrist ", "surgeon ", "radiologist ", "pathologist ",
		"technician ", "paramedic ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// inferEntityType applies deterministic rules to correct entity types based on name patterns.
// Rule priority: person prefix > address > event > role > facility > org keywords > tech > person fallback
func inferEntityType(name, currentType string) string {
	lower := strings.ToLower(name)

	// Rule 1: Names starting with professional titles are persons
	if hasProfessionalTitlePrefix(lower) {
		return "person"
	}

	// Rule 2: Names matching address patterns are addresses
	if addressPatternEN.MatchString(name) || addressPatternHE.MatchString(name) {
		return "address"
	}

	// Rule 3: Names containing street/road indicators are addresses
	if hasAddressIndicator(lower) {
		return "address"
	}

	// Rule 4: Names starting with "incident" are events
	if incidentPattern.MatchString(name) {
		return "event"
	}

	// Rule 5: Known role titles (MUST come before facility/org/tech checks
	// because "branch manager" contains "branch", "chief operations officer"
	// contains "office" as substring, "head of digital systems" contains "system")
	if isRoleTitle(lower) {
		return "role"
	}

	// Rule 6: Names containing HQ/headquarters/facility words are organizations
	if isHQNode(lower) || isFacilityByName(lower) {
		return "organization"
	}

	// Rule 7: Organization by general keywords (clinic, hospital, etc.)
	if isOrgByName(lower) {
		return "organization"
	}

	// Rule 8: Technology by keywords (portal, system, software, etc.)
	if looksLikeTechnology(lower) {
		return "technology"
	}

	// Rule 9 removed: ambiguous names (person vs. org vs. concept) are now
	// classified by the LLM via ClassifyEntityTypes in the pipeline.

	return currentType
}

// isHQNode checks if a node name refers to a headquarters.
func isHQNode(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, " hq") || strings.Contains(lower, "headquarters")
}

// isFacilityByName checks if a name contains general facility/building words
// that indicate an organization or physical location, not a person.
func isFacilityByName(lower string) bool {
	// These are safe as substring matches (unlikely to appear inside other words)
	safeSubstrings := []string{
		"center", "centre", "hub", "campus", "annex",
		"headquarters", "building", "facility",
		"warehouse", "depot", "terminal",
	}
	for _, w := range safeSubstrings {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// These need whole-word matching to avoid false positives
	// ("office" in "officer", "branch" in "branching")
	wholeWords := []string{"office", "branch", "hq"}
	for _, w := range wholeWords {
		if containsWord(lower, w) {
			return true
		}
	}
	return false
}

// isOrgByName checks if a name contains general organization keywords.
// No domain-specific prefixes — only structural patterns.
func isOrgByName(lower string) bool {
	orgWords := []string{
		"clinic", "hospital", "pharmacy", "laboratory",
		"imaging", "transport", "courier", "network", "insurance",
		"bank", "university", "school", "institute", "foundation",
		"corporation", "agency", "authority", "ministry",
	}
	for _, w := range orgWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// Check for "lab"/"labs" as whole words (avoid matching "label", "collaborate")
	if containsWord(lower, "lab") || containsWord(lower, "labs") {
		return true
	}
	// "diagnostics" is only an org indicator in multi-word names (e.g. "northlab diagnostics")
	// Standalone "diagnostics" is a service name
	if len(strings.Fields(lower)) >= 2 && strings.Contains(lower, "diagnostics") {
		return true
	}
	return false
}

// hasAddressIndicator checks if a string contains street address words.
func hasAddressIndicator(lower string) bool {
	indicators := []string{
		" street,", " street ", " st,", " st ",
		" avenue,", " avenue ", " ave,", " ave ",
		" road,", " road ", " rd,", " rd ",
		" boulevard,", " boulevard ", " blvd,", " blvd ",
	}
	for _, ind := range indicators {
		if strings.Contains(lower, ind) {
			return true
		}
	}
	return false
}

// fillMissingTypesFromRelation uses the relation to fill ONLY empty entity types.
// It never overrides existing types — if the relation contradicts a known type,
// the edge will be rejected later by isValidEdge.
func fillMissingTypesFromRelation(t models.Triple) models.Triple {
	rule, ok := directionRules[t.Edge]
	if !ok {
		return t
	}

	if t.Node1Type == "" && len(rule.sourceTypes) == 1 {
		t.Node1Type = rule.sourceTypes[0]
	}
	if t.Node2Type == "" && len(rule.targetTypes) == 1 {
		t.Node2Type = rule.targetTypes[0]
	}

	return t
}

// normalizeRelation maps a raw relation string to its canonical form.
func normalizeRelation(edge string) string {
	lower := strings.ToLower(strings.TrimSpace(edge))

	if canonical, ok := canonicalRelations[lower]; ok {
		return canonical
	}

	underscored := strings.ReplaceAll(lower, " ", "_")
	if canonical, ok := canonicalRelations[underscored]; ok {
		return canonical
	}

	return strings.ToUpper(underscored)
}

// shouldFlip returns true if the edge direction is reversed based on entity types.
func shouldFlip(t models.Triple) bool {
	rule, ok := directionRules[t.Edge]
	if !ok {
		return false
	}

	if t.Node1Type == "" || t.Node2Type == "" {
		return false
	}

	sourceOK := containsType(rule.sourceTypes, t.Node1Type)
	targetOK := containsType(rule.targetTypes, t.Node2Type)

	if sourceOK && targetOK {
		return false
	}

	sourceFlipOK := containsType(rule.sourceTypes, t.Node2Type)
	targetFlipOK := containsType(rule.targetTypes, t.Node1Type)

	return sourceFlipOK && targetFlipOK
}

// isValidEdge checks whether an edge satisfies its type constraints.
// Strict: rejects edges with unknown relations or missing types.
func isValidEdge(t models.Triple) bool {
	rule, ok := directionRules[t.Edge]
	if !ok {
		// ALIAS_OF and HAS_ROLE have no strict direction rule — allow through
		if t.Edge == "ALIAS_OF" || t.Edge == "HAS_ROLE" {
			return true
		}
		return false
	}

	if t.Node1Type == "" || t.Node2Type == "" {
		return false
	}

	sourceOK := containsType(rule.sourceTypes, t.Node1Type)
	targetOK := containsType(rule.targetTypes, t.Node2Type)

	return sourceOK && targetOK
}

// containsWord checks if a word appears as a whole word in the string
// (not as a substring of a larger word like "lab" in "collaborate").
func containsWord(s, word string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], word)
		if i < 0 {
			return false
		}
		start := idx + i
		end := start + len(word)
		leftOK := start == 0 || !isAlpha(s[start-1])
		rightOK := end == len(s) || !isAlpha(s[end])
		if leftOK && rightOK {
			return true
		}
		idx = start + 1
	}
}

func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func containsType(allowed []string, entityType string) bool {
	for _, a := range allowed {
		if a == entityType {
			return true
		}
	}
	return false
}

// looksLikeTechnology checks if a name plausibly refers to technology/software.
// Only returns true if the name contains an explicit tech keyword.
func looksLikeTechnology(name string) bool {
	lower := strings.ToLower(name)

	techWords := []string{
		"portal", "software", "platform", "module",
		"dashboard", "api", "database", "server", "cloud", "sync",
		"scanner", "suite", "engine",
	}
	for _, w := range techWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// Whole-word matches for terms that are substrings of non-tech words
	// ("system" in "ecosystem", "app" in "happy", "tool" in "stool",
	//  "tracker" in unrelated contexts, "monitor" in "monitoring")
	techWholeWords := []string{"system", "app", "tool", "tracker"}
	for _, w := range techWholeWords {
		if containsWord(lower, w) {
			return true
		}
	}

	return false
}
