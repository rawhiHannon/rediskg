package schema

// BaseType represents a universal upper-ontology type.
// Domain-specific types inherit from these.
type BaseType struct {
	ID          string
	Description string
}

// DefaultBaseTypes returns the predefined universal base type table.
// These are broad and domain-independent. Domain-specific categories
// (branch_office, service_center, etc.) become domain_types under these.
var PredefinedBaseTypes = []BaseType{
	{ID: "person", Description: "Named individual (doctor, student, employee, artist, etc.)"},
	{ID: "organization", Description: "Company, institution, agency, network, group, club, charity"},
	{ID: "location", Description: "Geographic place (country, city, neighborhood, airport, etc.)"},
	{ID: "address", Description: "Street address or postal location"},
	{ID: "event", Description: "Named incident, meeting, appointment, match, election, surgery"},
	{ID: "document", Description: "Invoice, contract, report, email, policy, manual"},
	{ID: "service", Description: "Delivery, consultation, blood test, repair, internet service"},
	{ID: "product", Description: "Manufactured item, software subscription, insurance package"},
	{ID: "role", Description: "Job title or function (manager, CEO, nurse, developer)"},
	{ID: "technology", Description: "Mobile app, platform, database, AI model, API, sensor system"},
	{ID: "physical_object", Description: "Tangible object (knife, laptop, car, medicine bottle, machine)"},
	{ID: "biological_entity", Description: "Living organism, species, biological structure"},
	{ID: "substance", Description: "Chemical, drug, material, compound, blood sample"},
	{ID: "quantity", Description: "Measurable amount, metric, percentage, weight"},
	{ID: "date_time", Description: "Date, time, duration, schedule, temporal reference"},
	{ID: "money", Description: "Currency amount, price, salary, budget"},
	{ID: "language", Description: "Natural language (English, Arabic, Hebrew, etc.)"},
	{ID: "law_or_policy", Description: "Regulation, law, official policy, compliance requirement"},
	{ID: "creative_work", Description: "Book, film, song, artwork, publication"},
	{ID: "identifier", Description: "ID number, code, reference, serial number"},
	{ID: "concept", Description: "Abstract topic (privacy, risk, strategy, quality, workflow)"},
}

// BaseTypeSet is a lookup set for fast validation.
var BaseTypeSet map[string]bool

// PredefinedFunctionalRoles defines the controlled vocabulary for entity roles.
// These are domain-agnostic roles that drive relation validation.
var PredefinedFunctionalRoles = []string{
	"parent_organization", // top-level org that owns branches/subsidiaries
	"branch",              // operational unit of a parent org
	"operator",            // entity that operates/runs other entities
	"operated_unit",       // entity operated by an operator
	"planned_unit",        // entity that is planned but not yet active
	"staff_member",        // person who works at/for an entity
	"deputy_manager",      // person in a deputy/acting manager role
	"external_partner",    // independent partner entity (not a subsidiary)
	"service_provider",    // entity that provides services
	"headquarters",        // entity serving as HQ location
	"regional_hub",        // entity serving as regional center
}

// FunctionalRoleSet is a lookup set for fast validation.
var FunctionalRoleSet map[string]bool

// PredefinedStatuses defines the controlled vocabulary for entity status.
var PredefinedStatuses = []string{
	"active",      // currently operating
	"planned",     // planned but not yet active
	"inactive",    // temporarily not operating
	"former",      // no longer exists or operates
	"prospective", // under consideration
	"unknown",     // status not determinable from evidence
}

// StatusSet is a lookup set for fast validation.
var StatusSet map[string]bool

// RelationRule defines functional-role-based validation for a relation.
type RelationRule struct {
	SourceRoles []string // source entity must have one of these roles (empty = any)
	TargetRoles []string // target entity must have one of these roles (empty = any)
	ForbiddenTargetStatuses []string // target must NOT have these statuses
	ForbiddenSourceStatuses []string // source must NOT have these statuses
}

// RelationRules maps relation IDs to their functional-role-based validation rules.
var RelationRules map[string]RelationRule

func init() {
	BaseTypeSet = make(map[string]bool, len(PredefinedBaseTypes))
	for _, bt := range PredefinedBaseTypes {
		BaseTypeSet[bt.ID] = true
	}

	FunctionalRoleSet = make(map[string]bool, len(PredefinedFunctionalRoles))
	for _, r := range PredefinedFunctionalRoles {
		FunctionalRoleSet[r] = true
	}

	StatusSet = make(map[string]bool, len(PredefinedStatuses))
	for _, s := range PredefinedStatuses {
		StatusSet[s] = true
	}

	RelationRules = map[string]RelationRule{
		"HAS_BRANCH": {
			SourceRoles:             []string{"parent_organization", "operator"},
			TargetRoles:             []string{"branch", "operated_unit"},
			ForbiddenTargetStatuses: []string{"planned"},
		},
		"HAS_PLANNED_BRANCH": {
			SourceRoles: []string{"parent_organization", "operator"},
			TargetRoles: []string{"branch", "operated_unit", "planned_unit"},
		},
		"MANAGES": {
			SourceRoles: []string{"staff_member"},
		},
		"HAS_DEPUTY_MANAGER": {
			TargetRoles: []string{"deputy_manager", "staff_member"},
		},
		"PARTNERS_WITH": {
			SourceRoles: []string{"external_partner", "parent_organization"},
			TargetRoles: []string{"external_partner", "parent_organization"},
		},
		"BASED_AT": {
			SourceRoles: []string{"staff_member"},
		},
		"VISITS": {
			SourceRoles: []string{"staff_member", "service_provider"},
		},
		"PROVIDES_SERVICE_FOR": {
			SourceRoles: []string{"staff_member", "service_provider"},
		},
		"PROVIDES_REMOTE_SERVICE_FOR": {
			SourceRoles: []string{"staff_member", "service_provider"},
		},
		"HEADQUARTERED_AT": {
			TargetRoles: []string{"headquarters"},
		},
	}
}

// IsValidBaseType checks if a type is a predefined base type.
func IsValidBaseType(t string) bool {
	return BaseTypeSet[t]
}

// IsValidFunctionalRole checks if a role is in the controlled vocabulary.
func IsValidFunctionalRole(r string) bool {
	return FunctionalRoleSet[r]
}

// IsValidStatus checks if a status is in the controlled vocabulary.
func IsValidStatus(s string) bool {
	return StatusSet[s]
}

// GetRelationRule returns the functional-role validation rule for a relation, or nil.
func GetRelationRule(relationID string) *RelationRule {
	if r, ok := RelationRules[relationID]; ok {
		return &r
	}
	return nil
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

// PredefinedRelations defines the stable relation families.
// LLM-extracted raw relation names are normalized to these IDs.
var PredefinedRelations = []RelationFamily{
	{
		Category: "STRUCTURE",
		Relations: []RelationDef{
			{ID: "HAS_BRANCH", Description: "Organization has an active branch/site", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization", "location"}},
			{ID: "HAS_PLANNED_BRANCH", Description: "Organization has a planned/future branch", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization", "location"}},
			{ID: "PART_OF", Description: "Entity is part of a larger entity", SourceTypes: []string{"organization", "location"}, TargetTypes: []string{"organization", "location"}},
			{ID: "HEADQUARTERED_AT", Description: "Organization headquarters location", SourceTypes: []string{"organization"}, TargetTypes: []string{"location", "address"}},
		},
	},
	{
		Category: "LOCATION",
		Relations: []RelationDef{
			{ID: "LOCATED_AT", Description: "Entity is at a specific address", TargetTypes: []string{"address", "location"}},
			{ID: "LOCATED_IN", Description: "Entity is within a geographic area", TargetTypes: []string{"location"}},
			{ID: "NEAR", Description: "Entity is near another entity", Symmetric: true},
		},
	},
	{
		Category: "PEOPLE",
		Relations: []RelationDef{
			{ID: "HAS_ROLE", Description: "Person holds a role/title", SourceTypes: []string{"person"}, TargetTypes: []string{"role"}},
			{ID: "MANAGES", Description: "Person is the main manager of entity", SourceTypes: []string{"person"}, TargetTypes: []string{"organization", "location"}},
			{ID: "HAS_DEPUTY_MANAGER", Description: "Organization has a deputy/acting manager", SourceTypes: []string{"organization"}, TargetTypes: []string{"person"}},
			{ID: "BASED_AT", Description: "Person's primary work location", SourceTypes: []string{"person"}, TargetTypes: []string{"organization", "location"}},
			{ID: "VISITS", Description: "Person visits/rotates to a location", SourceTypes: []string{"person"}, TargetTypes: []string{"organization", "location"}},
			{ID: "PROVIDES_REMOTE_SERVICE_FOR", Description: "Person provides remote service for org", SourceTypes: []string{"person"}, TargetTypes: []string{"organization"}},
			{ID: "PROVIDES_SERVICE_FOR", Description: "Person provides on-site service for org", SourceTypes: []string{"person"}, TargetTypes: []string{"organization"}},
			{ID: "REPORTS_TO", Description: "Person reports to another person", SourceTypes: []string{"person"}, TargetTypes: []string{"person"}},
			{ID: "FOUNDED_BY", Description: "Organization founded by person", SourceTypes: []string{"organization"}, TargetTypes: []string{"person"}},
		},
	},
	{
		Category: "SERVICE",
		Relations: []RelationDef{
			{ID: "OFFERS", Description: "Organization offers a service/product", SourceTypes: []string{"organization"}, TargetTypes: []string{"service", "product"}},
			{ID: "DOES_NOT_OFFER", Description: "Organization explicitly does not offer", SourceTypes: []string{"organization"}, TargetTypes: []string{"service", "product"}},
			{ID: "REQUIRES", Description: "Service/product requires another", TargetTypes: []string{"service", "product", "technology"}},
			{ID: "SPECIALIZES_IN", Description: "Entity specializes in a domain/service", TargetTypes: []string{"service", "concept"}},
		},
	},
	{
		Category: "PARTNER_CONTRACT",
		Relations: []RelationDef{
			{ID: "PARTNERS_WITH", Description: "Two independent entities partner", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization"}, Symmetric: true},
			{ID: "CONTRACTED_WITH", Description: "Entity has a contract with another", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization"}, Symmetric: true},
			{ID: "HAS_AGREEMENT_WITH", Description: "Entity has agreement with another", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization"}, Symmetric: true},
			{ID: "NO_CONTRACT_WITH", Description: "Explicitly no contract between entities", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization"}, Symmetric: true},
			{ID: "EVALUATING_PARTNERSHIP_WITH", Description: "Considering partnership", SourceTypes: []string{"organization"}, TargetTypes: []string{"organization"}},
		},
	},
	{
		Category: "EVENT",
		Relations: []RelationDef{
			{ID: "OCCURRED_ON", Description: "Event occurred on date", SourceTypes: []string{"event"}, TargetTypes: []string{"date_time"}},
			{ID: "INVOLVES", Description: "Event involves entity"},
			{ID: "CAUSED_BY", Description: "Event caused by another event/entity"},
			{ID: "AFFECTS", Description: "Event/action affects entity"},
		},
	},
	{
		Category: "TECHNOLOGY",
		Relations: []RelationDef{
			{ID: "INTEGRATED_WITH", Description: "System integrated with another", SourceTypes: []string{"technology", "organization"}, TargetTypes: []string{"technology"}, Symmetric: true},
			{ID: "OWNS", Description: "Entity owns another entity"},
			{ID: "USES", Description: "Entity uses a technology/tool", TargetTypes: []string{"technology", "product"}},
			{ID: "HAS_PORTAL", Description: "Organization has a tech portal", SourceTypes: []string{"organization"}, TargetTypes: []string{"technology"}},
		},
	},
	{
		Category: "IDENTITY",
		Relations: []RelationDef{
			{ID: "ALIAS_OF", Description: "Entity is an alias of another (internal use only)", Symmetric: true},
		},
	},
}

// RelationIndex is a fast lookup from relation ID to its definition.
var RelationIndex map[string]*RelationDef

// RelationAliasIndex maps common LLM-generated relation names to canonical IDs.
var RelationAliasIndex map[string]string

func init() {
	RelationIndex = make(map[string]*RelationDef)
	for i := range PredefinedRelations {
		for j := range PredefinedRelations[i].Relations {
			rel := &PredefinedRelations[i].Relations[j]
			RelationIndex[rel.ID] = rel
		}
	}

	// Common aliases the LLM might generate -> canonical relation ID
	RelationAliasIndex = map[string]string{
		// Structure
		"BRANCH_OF":      "PART_OF",
		"OPERATES":       "HAS_BRANCH",
		"OPERATED_BY":    "HAS_BRANCH",
		"HAS_SITE":       "HAS_BRANCH",
		"PLANS_BRANCH":   "HAS_PLANNED_BRANCH",
		"SUBSIDIARY_OF":  "PART_OF",
		"BELONGS_TO":     "PART_OF",
		"HQ_AT":          "HEADQUARTERED_AT",
		"HAS_HQ":         "HEADQUARTERED_AT",

		// Location
		"IN":             "LOCATED_IN",
		"AT":             "LOCATED_AT",
		"ADDRESS":        "LOCATED_AT",
		"HAS_ADDRESS":    "LOCATED_AT",

		// People
		"WORKS_AT":       "BASED_AT",
		"EMPLOYED_BY":    "BASED_AT",
		"EMPLOYED_AT":    "BASED_AT",
		"MANAGED_BY":     "MANAGES",
		"BRANCH_MANAGER_OF": "MANAGES",
		"HAS_PRACTITIONER": "PROVIDES_SERVICE_FOR",
		"PROVIDES_FOR":   "PROVIDES_SERVICE_FOR",
		"SERVES_AT":      "PROVIDES_SERVICE_FOR",
		"STAFFED_BY":     "PROVIDES_SERVICE_FOR",
		"DEPUTY_MANAGER_OF": "HAS_DEPUTY_MANAGER",
		"ACTING_MANAGER_OF": "HAS_DEPUTY_MANAGER",

		// Service
		"PROVIDES":       "OFFERS",
		"HAS_SERVICE":    "OFFERS",
		"OFFERS_SERVICE": "OFFERS",

		// Partnership
		"PARTNER_OF":            "PARTNERS_WITH",
		"HAS_PARTNERSHIP_WITH":  "PARTNERS_WITH",
		"FULFILLMENT_PARTNER_FOR": "PARTNERS_WITH",
		"HAS_CONTRACT_WITH":     "CONTRACTED_WITH",

		// Technology
		"CONNECTED_TO":   "INTEGRATED_WITH",
		"HAS_SYSTEM":     "USES",
		"RUNS_ON":        "USES",

		// General
		"RELATED_TO":     "", // reject: too vague
		"ASSOCIATED_WITH": "", // reject: too vague
		"HANDLES":        "", // reject: too vague without context
	}
}

// ResolveRelation normalizes a raw LLM relation name to a canonical internal ID.
// Returns the canonical ID and whether it was found.
// Empty string return means the relation should be rejected.
func ResolveRelation(raw string) (string, bool) {
	// Direct match
	if _, ok := RelationIndex[raw]; ok {
		return raw, true
	}
	// Alias match
	if canonical, ok := RelationAliasIndex[raw]; ok {
		if canonical == "" {
			return "", false // explicitly rejected
		}
		return canonical, true
	}
	// Unknown — keep as candidate but flag
	return raw, false
}

// GetRelationDef returns the definition for a relation ID, or nil if unknown.
func GetRelationDef(id string) *RelationDef {
	return RelationIndex[id]
}

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
