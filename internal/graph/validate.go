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
	"VISITS":           {sourceTypes: []string{"person"}, targetTypes: []string{"organization", "location"}},
	"OFFERS_SERVICE":   {sourceTypes: []string{"organization"}, targetTypes: []string{"service"}},
	"DOES_NOT_OFFER":   {sourceTypes: []string{"organization"}, targetTypes: []string{"service"}},
	"HAS_PARTNER":      {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"CONTRACTED_WITH":  {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"NO_CONTRACT":      {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"REPORTS_TO":       {sourceTypes: []string{"person"}, targetTypes: []string{"person"}},
	"SPECIALIZES_IN":   {sourceTypes: []string{"person"}, targetTypes: []string{"service", "concept"}},
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
	// First: fix entity types deterministically in the entity map
	locked := correctEntityTypes(entityMap)

	// Second: propagate types from graph structure (replaces hardcoded city lists etc.)
	PropagateTypesFromTriples(triples, entityMap, locked)

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

		// Apply relation-based type repair
		t = repairTypesFromRelation(t)

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

	log.Printf("Validation: normalized %d, type-corrected %d, flipped %d, converted %d PART_OF→HAS_BRANCH, alias-fixed %d, demoted %d MANAGED_BY→WORKS_AT, rejected %d (of %d total)",
		normalized, typeCorrected, flipped, converted, aliasFixed, demoted, rejected, len(triples))

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

	// technology -> ALIAS_OF -> organization → convert to USES_TECHNOLOGY
	if t.Node1Type == "technology" && t.Node2Type == "organization" {
		t.Edge = "USES_TECHNOLOGY"
		t.Node1, t.Node2 = t.Node2, t.Node1
		t.Node1Type, t.Node2Type = "organization", "technology"
		return t, true
	}

	// organization -> ALIAS_OF -> technology → convert to USES_TECHNOLOGY
	if t.Node1Type == "organization" && t.Node2Type == "technology" {
		t.Edge = "USES_TECHNOLOGY"
		return t, true
	}

	// HQ -> ALIAS_OF -> organization → convert to HAS_HEADQUARTERS (HQ is a facility, not an alias)
	if isHQNode(t.Node1) && t.Node2Type == "organization" {
		t.Edge = "HAS_HEADQUARTERS"
		// org → HAS_HEADQUARTERS → hq
		t.Node1, t.Node2 = t.Node2, t.Node1
		t.Node1Type, t.Node2Type = "organization", "organization"
		return t, true
	}
	if isHQNode(t.Node2) && t.Node1Type == "organization" {
		t.Edge = "HAS_HEADQUARTERS"
		t.Node2Type = "organization"
		return t, true
	}

	// Valid ALIAS_OF: same type or both empty types
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

// correctEntityTypes applies deterministic type corrections to the entity map.
// Returns the set of entity names that were deterministically corrected (protected from propagation override).
func correctEntityTypes(entityMap map[string]string) map[string]bool {
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
		return strings.HasPrefix(lower, "dr.") || strings.HasPrefix(lower, "dr ")
	case "address":
		return addressPatternEN.MatchString(name) || addressPatternHE.MatchString(name) || hasAddressIndicator(lower)
	case "event":
		return incidentPattern.MatchString(name)
	case "organization":
		return isHQNode(lower) || isFacilityByName(lower) || isOrgByName(lower)
	case "technology":
		return looksLikeTechnology(lower) && !looksLikePersonName(lower)
	case "role":
		return isRoleTitle(lower)
	}
	return false
}

// inferEntityType applies deterministic rules to correct entity types based on name patterns.
func inferEntityType(name, currentType string) string {
	lower := strings.ToLower(name)

	// Rule 1: Names starting with "dr." or "dr " are persons
	if strings.HasPrefix(lower, "dr.") || strings.HasPrefix(lower, "dr ") {
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

	// Rule 5: Names containing HQ/headquarters/facility words are organizations
	if isHQNode(lower) || isFacilityByName(lower) {
		return "organization"
	}

	// Rule 6: Organization by general keywords (clinic, hospital, etc.)
	if isOrgByName(lower) {
		return "organization"
	}

	// Rule 7: Technology by keywords (portal, system, software, etc.)
	if looksLikeTechnology(lower) && !looksLikePersonName(lower) {
		return "technology"
	}

	// Rule 8: Known role titles should be typed as role
	if isRoleTitle(lower) {
		return "role"
	}

	// Rule 9: Short 2-3 word names without org/service/tech keywords that are mistyped
	// as organization or event are likely persons
	if (currentType == "organization" || currentType == "event" || currentType == "") && looksLikePersonName(lower) {
		return "person"
	}

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
	facilityWords := []string{
		"center", "centre", "hub", "campus", "annex", "office",
		"branch", "headquarters", "hq", "building", "facility",
		"warehouse", "depot", "terminal",
	}
	for _, w := range facilityWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// isOrgByName checks if a name contains general organization keywords.
// No domain-specific prefixes — only structural patterns.
func isOrgByName(lower string) bool {
	orgWords := []string{
		"clinic", "hospital", "pharmacy", "laboratory", "diagnostics",
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

// looksLikePersonName checks if a name looks like a person's name.
// A 2-3 word name with no org/service/location keywords is likely a person.
func looksLikePersonName(lower string) bool {
	words := strings.Fields(lower)
	if len(words) < 2 || len(words) > 4 {
		return false
	}

	// Names with org keywords are not persons
	orgKeywords := []string{
		"clinic", "center", "hospital", "pharmacy", "lab", "laboratory",
		"network", "hub", "transport", "medical", "health", "care",
		"insurance", "diagnostics", "services", "inc", "ltd", "corp",
		"company", "group", "foundation",
	}
	for _, kw := range orgKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}

	// Names with service/concept keywords are not persons
	nonPersonKeywords := []string{
		"service", "delivery", "courier", "testing", "monitoring",
		"therapy", "consultation", "treatment", "screening",
		"counseling", "vaccination", "medicine", "policy", "protocol",
		"agreement", "portal", "system", "technology",
	}
	for _, kw := range nonPersonKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}

	// Passed all exclusion filters — likely a person name
	return true
}

// repairTypesFromRelation uses the relation itself to fix missing or wrong entity types.
func repairTypesFromRelation(t models.Triple) models.Triple {
	rule, ok := directionRules[t.Edge]
	if !ok {
		return t
	}

	// Fill in missing types from the rule
	if len(rule.sourceTypes) > 0 && t.Node1Type == "" {
		t.Node1Type = rule.sourceTypes[0]
	}
	if len(rule.targetTypes) > 0 && t.Node2Type == "" {
		t.Node2Type = rule.targetTypes[0]
	}

	// For relations with unambiguous type expectations, force-correct types
	switch t.Edge {
	case "OFFERS_SERVICE", "DOES_NOT_OFFER":
		if t.Node2Type != "service" {
			t.Node2Type = "service"
		}
		if t.Node1Type != "organization" {
			t.Node1Type = "organization"
		}
	case "MANAGED_BY", "DEPUTY_MANAGER":
		if t.Node2Type != "person" {
			t.Node2Type = "person"
		}
		if t.Node1Type != "organization" {
			t.Node1Type = "organization"
		}
	case "WORKS_AT":
		if t.Node1Type != "person" {
			t.Node1Type = "person"
		}
		if t.Node2Type != "organization" {
			t.Node2Type = "organization"
		}
	case "VISITS":
		if t.Node1Type != "person" {
			t.Node1Type = "person"
		}
	case "SPECIALIZES_IN":
		if t.Node1Type != "person" {
			t.Node1Type = "person"
		}
		if t.Node2Type != "service" && t.Node2Type != "concept" {
			t.Node2Type = "service"
		}
	case "LOCATED_AT":
		if t.Node1Type != "organization" {
			t.Node1Type = "organization"
		}
		if t.Node2Type != "address" {
			t.Node2Type = "address"
		}
	case "LOCATED_IN":
		if t.Node2Type != "location" {
			t.Node2Type = "location"
		}
	case "INVOLVED_IN":
		if t.Node1Type != "person" {
			t.Node1Type = "person"
		}
		if t.Node2Type != "event" {
			t.Node2Type = "event"
		}
	case "USES_TECHNOLOGY":
		if t.Node1Type != "organization" {
			t.Node1Type = "organization"
		}
		if t.Node2Type != "technology" {
			t.Node2Type = "technology"
		}
	case "HAS_ROLE":
		if t.Node1Type != "person" {
			t.Node1Type = "person"
		}
		if t.Node2Type != "role" && t.Node2Type != "concept" {
			t.Node2Type = "role"
		}
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
// Uses the entity's type (set by LLM or propagation) as the primary signal,
// with tech keyword matching as a secondary check.
func looksLikeTechnology(name string) bool {
	lower := strings.ToLower(name)

	// Positive: tech keywords in the name
	techWords := []string{
		"portal", "system", "software", "platform", "app", "module",
		"dashboard", "api", "database", "server", "cloud", "sync",
		"tracker", "scanner", "monitor", "tool", "suite", "engine",
		"pro", "lite", "plus", "hub",
	}
	for _, w := range techWords {
		if strings.Contains(lower, w) {
			return true
		}
	}

	// Negative: org keywords in the name — not technology
	if isOrgByName(lower) {
		return false
	}

	// Negative: person-like names
	if strings.HasPrefix(lower, "dr.") || strings.HasPrefix(lower, "dr ") {
		return false
	}

	// Negative: multi-word names without tech keywords are unlikely tech
	words := strings.Fields(lower)
	if len(words) >= 2 {
		return false
	}

	// Single word with no tech indicator — ambiguous, allow
	return true
}
