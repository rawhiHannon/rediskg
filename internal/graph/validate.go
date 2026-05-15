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
	"has_branch": "HAS_BRANCH", "branch_of": "HAS_BRANCH",

	// Person → Person
	"reports_to": "REPORTS_TO", "supervises": "REPORTS_TO",
	"referred_by": "REFERRED_BY",

	// Person → Service/Role
	"specializes_in": "SPECIALIZES_IN", "specialization": "SPECIALIZES_IN",
	"role": "HAS_ROLE", "has_role": "HAS_ROLE", "position": "HAS_ROLE",

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
	"PART_OF":          {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"HAS_BRANCH":       {sourceTypes: []string{"organization"}, targetTypes: []string{"organization"}},
	"INVOLVED_IN":      {sourceTypes: []string{"person"}, targetTypes: []string{"event"}},
	"USES_TECHNOLOGY":  {sourceTypes: []string{"organization"}, targetTypes: []string{"technology"}},
}

// Patterns for deterministic type correction
var (
	addressPatternEN = regexp.MustCompile(`(?i)\d+\s+\w+\s+(street|st|avenue|ave|road|rd|boulevard|blvd|lane|ln|drive|dr|way|plaza|square)`)
	addressPatternHE = regexp.MustCompile(`(?i)(רחוב|רח'|שד'|שדרות)\s+`)
	incidentPattern  = regexp.MustCompile(`(?i)^incident\s`)
)

// ValidateAndNormalizeTriples normalizes relation names, fixes entity types
// using deterministic rules, and corrects edge direction.
func ValidateAndNormalizeTriples(triples []models.Triple, entityMap map[string]string) []models.Triple {
	// First pass: fix entity types deterministically in the entity map
	correctEntityTypes(entityMap)

	var result []models.Triple
	flipped := 0
	normalized := 0
	rejected := 0
	typeCorrected := 0

	for _, t := range triples {
		// Enrich types from the entity map if not already set
		if t.Node1Type == "" {
			t.Node1Type = entityMap[t.Node1]
		}
		if t.Node2Type == "" {
			t.Node2Type = entityMap[t.Node2]
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

		// Apply deterministic type corrections on the triple itself
		n1Fixed := inferEntityType(t.Node1, t.Node1Type)
		n2Fixed := inferEntityType(t.Node2, t.Node2Type)
		if n1Fixed != t.Node1Type || n2Fixed != t.Node2Type {
			typeCorrected++
		}
		t.Node1Type = n1Fixed
		t.Node2Type = n2Fixed

		// Update entity map with corrected types
		if t.Node1Type != "" {
			entityMap[t.Node1] = t.Node1Type
		}
		if t.Node2Type != "" {
			entityMap[t.Node2] = t.Node2Type
		}

		// Drop RELATED_TO — too vague for a factual graph
		if t.Edge == "RELATED_TO" {
			rejected++
			continue
		}

		// Check direction and flip if needed
		if shouldFlip(t) {
			t.Node1, t.Node2 = t.Node2, t.Node1
			t.Node1Type, t.Node2Type = t.Node2Type, t.Node1Type
			flipped++
		}

		// Validate: reject edges that violate type constraints even after flip attempt
		if !isValidEdge(t) {
			rejected++
			continue
		}

		result = append(result, t)
	}

	log.Printf("Validation: normalized %d, type-corrected %d, flipped %d, rejected %d (of %d total)",
		normalized, typeCorrected, flipped, rejected, len(triples))

	return result
}

// correctEntityTypes applies deterministic type corrections to the entity map.
func correctEntityTypes(entityMap map[string]string) {
	for name, typ := range entityMap {
		fixed := inferEntityType(name, typ)
		if fixed != typ {
			entityMap[name] = fixed
		}
	}
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

	// Rule 3: Names containing street/road indicators with a number are addresses
	if hasAddressIndicator(lower) {
		return "address"
	}

	// Rule 4: Names starting with "incident" are events
	if incidentPattern.MatchString(name) {
		return "event"
	}

	// Rule 5: Common person name patterns — names with common Arabic/Hebrew first names
	// that were mistyped as organization/event
	if (currentType == "organization" || currentType == "event" || currentType == "") && looksLikePersonName(lower) {
		return "person"
	}

	return currentType
}

// hasAddressIndicator checks if a string looks like a street address.
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
// This catches cases where persons are mistyped as organizations.
func looksLikePersonName(lower string) bool {
	// If name has exactly 2-3 space-separated words and no org indicators, it might be a person
	words := strings.Fields(lower)
	if len(words) < 2 || len(words) > 4 {
		return false
	}

	// Names with org keywords are not persons
	orgKeywords := []string{
		"clinic", "center", "hospital", "pharmacy", "lab", "laboratory",
		"network", "hub", "transport", "medical", "health", "care",
		"inc", "ltd", "corp", "company", "group", "foundation",
	}
	for _, kw := range orgKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}

	// Names with location/service keywords are not persons
	nonPersonKeywords := []string{
		"service", "delivery", "courier", "testing", "monitoring",
		"therapy", "consultation", "treatment", "screening",
	}
	for _, kw := range nonPersonKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}

	return false // conservative: don't reclassify without strong signal
}

// normalizeRelation maps a raw relation string to its canonical form.
func normalizeRelation(edge string) string {
	lower := strings.ToLower(strings.TrimSpace(edge))

	// Direct lookup
	if canonical, ok := canonicalRelations[lower]; ok {
		return canonical
	}

	// Try with underscores instead of spaces
	underscored := strings.ReplaceAll(lower, " ", "_")
	if canonical, ok := canonicalRelations[underscored]; ok {
		return canonical
	}

	// Not recognized — return as-is (uppercase)
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

	// Check if flipped direction matches
	sourceFlipOK := containsType(rule.sourceTypes, t.Node2Type)
	targetFlipOK := containsType(rule.targetTypes, t.Node1Type)

	return sourceFlipOK && targetFlipOK
}

// isValidEdge checks whether an edge satisfies its type constraints.
// Returns true if no rule exists (unknown relation) or if types match.
func isValidEdge(t models.Triple) bool {
	rule, ok := directionRules[t.Edge]
	if !ok {
		return true // unknown relation, allow through
	}

	if t.Node1Type == "" || t.Node2Type == "" {
		return true // can't validate without types, allow through
	}

	sourceOK := containsType(rule.sourceTypes, t.Node1Type)
	targetOK := containsType(rule.targetTypes, t.Node2Type)

	return sourceOK && targetOK
}

func containsType(allowed []string, entityType string) bool {
	for _, a := range allowed {
		if a == entityType {
			return true
		}
	}
	return false
}
