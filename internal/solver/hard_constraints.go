package solver

import (
	"regexp"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// HardConstraintResult indicates whether an edge passes or fails a hard constraint.
type HardConstraintResult struct {
	Pass    bool
	Reason  string
	FixEdge *models.CandidateEdge // if non-nil, replace the edge with this fix
}

// ApplyHardConstraints filters candidate edges through all hard constraints.
// Returns edges that pass + edges that were fixed (direction flipped, relation changed).
func ApplyHardConstraints(
	edges []models.CandidateEdge,
	entities map[string]*models.CanonicalEntity,
	aliasMap map[string]string, // alias -> canonical name
) []models.CandidateEdge {
	var result []models.CandidateEdge

	for _, edge := range edges {
		r := checkAllConstraints(edge, entities, aliasMap)
		if r.Pass {
			result = append(result, edge)
		} else if r.FixEdge != nil {
			result = append(result, *r.FixEdge)
		}
		// else: rejected
	}

	return result
}

func checkAllConstraints(
	edge models.CandidateEdge,
	entities map[string]*models.CanonicalEntity,
	aliasMap map[string]string,
) HardConstraintResult {
	// Constraint 1: Raw value endpoints (dates, times) cannot be graph nodes
	if r := checkRawValueEndpoint(edge); !r.Pass {
		return r
	}

	// Constraint 2: Document titles cannot become business entities
	if r := checkDocumentTitle(edge, entities); !r.Pass {
		return r
	}

	// Constraint 3: Planned entities cannot have active-only relations
	if r := checkPlannedStatus(edge, entities); !r.Pass {
		return r
	}

	// Constraint 4: Internal branches cannot partner with parent
	if r := checkBranchPartnership(edge, entities); !r.Pass {
		return r
	}

	// Constraint 5: Deputy managers cannot be main managers
	if r := checkDeputyManager(edge, entities); !r.Pass {
		return r
	}

	// Constraint 6: Alias endpoints cannot own normal facts
	if r := checkAliasEndpoint(edge, aliasMap); !r.Pass {
		return r
	}

	// Constraint 7: ALIAS_OF must have compatible types and non-generic target
	if r := checkAliasCompatibility(edge, entities); !r.Pass {
		return r
	}

	// Constraint 8: Negative relations must have negation in evidence
	if r := checkNegativeRelationEvidence(edge); !r.Pass {
		return r
	}

	// Constraint 9: CONTRACTED_WITH must have contract/agreement evidence
	if r := checkContractEvidence(edge); !r.Pass {
		return r
	}

	// Constraint 10: OFFERS cannot be inferred from contract/agreement evidence
	if r := checkOffersEvidence(edge); !r.Pass {
		return r
	}

	// Constraint 11: Relation signature must match schema (base types)
	if r := checkRelationSignature(edge, entities); !r.Pass {
		return r
	}

	// Constraint 12: Schema-driven relation rule (roles, domain types, direction)
	if r := checkRelationRule(edge, entities); !r.Pass {
		return r
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 1: Document titles cannot become business entities.
func checkDocumentTitle(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	docPatterns := []string{
		"knowledge base", "internal operations", "last reviewed",
		"document owner", "version", "report", "manual",
		"policy document", "user guide", "reference guide",
	}

	for _, endpoint := range []string{edge.FromMention, edge.ToMention} {
		lower := strings.ToLower(endpoint)
		for _, pattern := range docPatterns {
			if strings.Contains(lower, pattern) {
				// This looks like a document title — check if it's being used as org/location
				ent := entities[endpoint]
				if ent != nil && !containsStr(ent.BaseTypes, "document") {
					return HardConstraintResult{
						Pass:   false,
						Reason: "document title '" + endpoint + "' used as non-document entity",
					}
				}
			}
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 2: Planned entities cannot have active-only relations.
// Uses entity.Status and functional roles instead of keyword scanning.
func checkPlannedStatus(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	// Check if this relation has forbidden target/source statuses
	rule := schema.GetRelationRule(edge.RelationID)
	if rule != nil {
		for _, endpoint := range []string{edge.FromMention, edge.ToMention} {
			ent := entities[endpoint]
			if ent == nil {
				continue
			}
			isSource := endpoint == edge.FromMention

			if isSource && len(rule.ForbiddenSourceStatuses) > 0 {
				for _, forbidden := range rule.ForbiddenSourceStatuses {
					if ent.Status == forbidden || ent.HasRole("planned_unit") && forbidden == "planned" {
						return HardConstraintResult{
							Pass:   false,
							Reason: edge.RelationID + " source '" + endpoint + "' has forbidden status: " + forbidden,
						}
					}
				}
			}
			if !isSource && len(rule.ForbiddenTargetStatuses) > 0 {
				for _, forbidden := range rule.ForbiddenTargetStatuses {
					if ent.Status == forbidden || ent.HasRole("planned_unit") && forbidden == "planned" {
						return HardConstraintResult{
							Pass:   false,
							Reason: edge.RelationID + " target '" + endpoint + "' has forbidden status: " + forbidden,
						}
					}
				}
			}
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 3: Internal branches cannot be partners of their parent.
// Uses functional roles: branch + parent_organization on same pair = not partners.
func checkBranchPartnership(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	if edge.RelationID != "PARTNERS_WITH" {
		return HardConstraintResult{Pass: true}
	}

	fromEnt := entities[edge.FromMention]
	toEnt := entities[edge.ToMention]
	if fromEnt == nil || toEnt == nil {
		return HardConstraintResult{Pass: true}
	}

	// If one is a branch/operated_unit and the other is parent/operator, reject
	fromIsBranch := fromEnt.HasRole("branch") || fromEnt.HasRole("operated_unit")
	fromIsParent := fromEnt.HasRole("parent_organization") || fromEnt.HasRole("operator")
	toIsBranch := toEnt.HasRole("branch") || toEnt.HasRole("operated_unit")
	toIsParent := toEnt.HasRole("parent_organization") || toEnt.HasRole("operator")

	if (fromIsBranch && toIsParent) || (fromIsParent && toIsBranch) {
		return HardConstraintResult{
			Pass:   false,
			Reason: "PARTNERS_WITH between parent/branch: " + edge.FromMention + " / " + edge.ToMention,
		}
	}

	// Fallback: name substring check
	fromLower := strings.ToLower(edge.FromMention)
	toLower := strings.ToLower(edge.ToMention)
	if strings.Contains(fromLower, toLower) || strings.Contains(toLower, fromLower) {
		return HardConstraintResult{
			Pass:   false,
			Reason: "PARTNERS_WITH between likely parent-child: " + edge.FromMention + " / " + edge.ToMention,
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 4: Deputy managers cannot be main managers.
// Uses functional roles instead of evidence keyword scanning.
func checkDeputyManager(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	if edge.RelationID != "MANAGES" {
		return HardConstraintResult{Pass: true}
	}

	fromEnt := entities[edge.FromMention]
	if fromEnt == nil {
		return HardConstraintResult{Pass: true}
	}

	if fromEnt.HasRole("deputy_manager") {
		// Convert to HAS_DEPUTY_MANAGER with flipped direction
		fixed := edge
		fixed.RelationID = "HAS_DEPUTY_MANAGER"
		fixed.FromMention, fixed.ToMention = edge.ToMention, edge.FromMention
		return HardConstraintResult{
			Pass:    false,
			Reason:  edge.FromMention + " has deputy_manager role, converting MANAGES to HAS_DEPUTY_MANAGER",
			FixEdge: &fixed,
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 5: Alias endpoints cannot own normal facts.
func checkAliasEndpoint(edge models.CandidateEdge, aliasMap map[string]string) HardConstraintResult {
	if edge.RelationID == "ALIAS_OF" {
		return HardConstraintResult{Pass: true} // ALIAS_OF itself is fine
	}

	if _, isAlias := aliasMap[edge.FromMention]; isAlias {
		return HardConstraintResult{
			Pass:   false,
			Reason: "alias endpoint '" + edge.FromMention + "' cannot own facts",
		}
	}
	if _, isAlias := aliasMap[edge.ToMention]; isAlias {
		return HardConstraintResult{
			Pass:   false,
			Reason: "alias endpoint '" + edge.ToMention + "' cannot own facts",
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 6: Relation source/target types must match schema definition.
func checkRelationSignature(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	relDef := schema.GetRelationDef(edge.RelationID)
	if relDef == nil {
		return HardConstraintResult{
			Pass:   false,
			Reason: "unknown relation '" + edge.RelationID + "' not in schema",
		}
	}

	fromEnt := entities[edge.FromMention]
	toEnt := entities[edge.ToMention]

	// Check source types
	if len(relDef.SourceTypes) > 0 && fromEnt != nil {
		if !hasAnyBaseType(fromEnt.BaseTypes, relDef.SourceTypes) {
			return HardConstraintResult{
				Pass:   false,
				Reason: edge.FromMention + " base types " + strings.Join(fromEnt.BaseTypes, ",") + " invalid for " + edge.RelationID + " source",
			}
		}
	}

	// Check target types
	if len(relDef.TargetTypes) > 0 && toEnt != nil {
		if !hasAnyBaseType(toEnt.BaseTypes, relDef.TargetTypes) {
			return HardConstraintResult{
				Pass:   false,
				Reason: edge.ToMention + " base types " + strings.Join(toEnt.BaseTypes, ",") + " invalid for " + edge.RelationID + " target",
			}
		}
	}

	return HardConstraintResult{Pass: true}
}

// checkRelationRule is the generic schema-driven constraint checker.
// It validates roles, domain types, and forbidden roles/domain types for both
// source and target. If the rule has CanFlipDirection and the edge fails,
// it tries the flipped direction before rejecting.
func checkRelationRule(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	rule := schema.GetRelationRule(edge.RelationID)
	if rule == nil {
		return HardConstraintResult{Pass: true}
	}

	from := entities[edge.FromMention]
	to := entities[edge.ToMention]

	// Try original direction
	if reason := validateEdgeAgainstRule(edge.FromMention, edge.ToMention, from, to, rule, edge.RelationID); reason == "" {
		return HardConstraintResult{Pass: true}
	}

	// If flipping is allowed, try swapped direction
	if rule.CanFlipDirection {
		if reason := validateEdgeAgainstRule(edge.ToMention, edge.FromMention, to, from, rule, edge.RelationID); reason == "" {
			fixed := edge
			fixed.FromMention, fixed.ToMention = edge.ToMention, edge.FromMention
			return HardConstraintResult{
				Pass:    false,
				Reason:  "flipped " + edge.RelationID + " direction to satisfy schema rule",
				FixEdge: &fixed,
			}
		}
	}

	// Both directions fail — return the original failure reason
	reason := validateEdgeAgainstRule(edge.FromMention, edge.ToMention, from, to, rule, edge.RelationID)
	return HardConstraintResult{
		Pass:   false,
		Reason: reason,
	}
}

// validateEdgeAgainstRule checks one direction of an edge against a RelationRule.
// Returns empty string if valid, or a rejection reason.
func validateEdgeAgainstRule(
	fromName, toName string,
	from, to *models.CanonicalEntity,
	rule *schema.RelationRule,
	relationID string,
) string {
	// Source checks
	if from != nil {
		if len(rule.SourceRoles) > 0 && !hasAnyRole(from, rule.SourceRoles) {
			return fromName + " lacks required role for " + relationID + " source (needs one of: " + strings.Join(rule.SourceRoles, ", ") + ")"
		}
		if len(rule.ForbiddenSourceRoles) > 0 && hasAnyRole(from, rule.ForbiddenSourceRoles) {
			return fromName + " has forbidden role for " + relationID + " source"
		}
		if len(rule.SourceDomainTypes) > 0 && !hasAnyDomainType(from.DomainTypes, rule.SourceDomainTypes) {
			return fromName + " lacks required domain type for " + relationID + " source (needs one of: " + strings.Join(rule.SourceDomainTypes, ", ") + ")"
		}
		if len(rule.ForbiddenSourceDomainTypes) > 0 && hasAnyDomainType(from.DomainTypes, rule.ForbiddenSourceDomainTypes) {
			return fromName + " has forbidden domain type for " + relationID + " source"
		}
	}

	// Target checks
	if to != nil {
		if len(rule.TargetRoles) > 0 && !hasAnyRole(to, rule.TargetRoles) {
			return toName + " lacks required role for " + relationID + " target (needs one of: " + strings.Join(rule.TargetRoles, ", ") + ")"
		}
		if len(rule.ForbiddenTargetRoles) > 0 && hasAnyRole(to, rule.ForbiddenTargetRoles) {
			return toName + " has forbidden role for " + relationID + " target"
		}
		if len(rule.TargetDomainTypes) > 0 && !hasAnyDomainType(to.DomainTypes, rule.TargetDomainTypes) {
			return toName + " lacks required domain type for " + relationID + " target (needs one of: " + strings.Join(rule.TargetDomainTypes, ", ") + ")"
		}
		if len(rule.ForbiddenTargetDomainTypes) > 0 && hasAnyDomainType(to.DomainTypes, rule.ForbiddenTargetDomainTypes) {
			return toName + " has forbidden domain type for " + relationID + " target"
		}
	}

	return ""
}

// hasAnyDomainType checks if any domain type in the entity's list matches any target type.
func hasAnyDomainType(domainTypes []string, targets []string) bool {
	for _, dt := range domainTypes {
		dtLower := strings.ToLower(dt)
		for _, t := range targets {
			if dtLower == t {
				return true
			}
		}
	}
	return false
}

// hasAnyRole checks if entity has at least one of the specified roles.
func hasAnyRole(ent *models.CanonicalEntity, roles []string) bool {
	for _, role := range roles {
		if ent.HasRole(role) {
			return true
		}
	}
	return false
}

// hasAnyBaseType checks if entity's base types intersect with allowed types.
func hasAnyBaseType(entityTypes, allowedTypes []string) bool {
	for _, et := range entityTypes {
		for _, at := range allowedTypes {
			if et == at {
				return true
			}
		}
	}
	return false
}

func containsStr(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// checkNegativeRelationEvidence rejects negative relations (DOES_NOT_*, NO_CONTRACT_WITH)
// when the evidence text does not contain explicit negation language.
// This catches LLM hallucinations where a positive fact gets a negative relation ID.
func checkNegativeRelationEvidence(edge models.CandidateEdge) HardConstraintResult {
	if !strings.HasPrefix(edge.RelationID, "DOES_NOT_") &&
		edge.RelationID != "NO_CONTRACT_WITH" {
		return HardConstraintResult{Pass: true}
	}

	ev := strings.ToLower(edge.EvidenceText)

	negationIndicators := []string{
		"does not", "doesn't", "do not", "did not", "didn't",
		"no contract", "no agreement", "no active contract",
		"not available", "not responsible",
		"not provide", "does not provide",
		"not handle", "does not handle",
		"not process", "does not process",
		"not offer", "does not offer",
		"no longer", "not covered by",
	}

	for _, indicator := range negationIndicators {
		if strings.Contains(ev, indicator) {
			return HardConstraintResult{Pass: true}
		}
	}

	return HardConstraintResult{
		Pass:   false,
		Reason: "negative relation " + edge.RelationID + " without negation in evidence: " + truncate(edge.EvidenceText, 80),
	}
}

// checkOffersEvidence rejects OFFERS edges that are inferred from contract/agreement
// evidence. These should be modeled as CONTRACTED_WITH or partner relations instead.
func checkOffersEvidence(edge models.CandidateEdge) HardConstraintResult {
	if edge.RelationID != "OFFERS" {
		return HardConstraintResult{Pass: true}
	}

	ev := strings.ToLower(edge.EvidenceText)
	if strings.Contains(ev, "contract with") ||
		strings.Contains(ev, "agreement with") {
		return HardConstraintResult{
			Pass:   false,
			Reason: "OFFERS inferred from contract/agreement evidence; prefer partner/contract relation",
		}
	}

	return HardConstraintResult{Pass: true}
}

// checkContractEvidence rejects CONTRACTED_WITH edges whose evidence does not
// mention a contract or agreement. This prevents the LLM from hallucinating
// contractual relationships from generic partnership language.
func checkContractEvidence(edge models.CandidateEdge) HardConstraintResult {
	if edge.RelationID != "CONTRACTED_WITH" {
		return HardConstraintResult{Pass: true}
	}

	ev := strings.ToLower(edge.EvidenceText)
	contractIndicators := []string{
		"contract", "agreement", "contracted", "signed",
		"engagement letter", "service agreement", "memorandum",
		"mou", "sla",
	}

	for _, indicator := range contractIndicators {
		if strings.Contains(ev, indicator) {
			return HardConstraintResult{Pass: true}
		}
	}

	return HardConstraintResult{
		Pass:   false,
		Reason: "CONTRACTED_WITH without contract/agreement evidence: " + truncate(edge.EvidenceText, 80),
	}
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// checkRawValueEndpoint rejects edges whose endpoints look like raw temporal/quantity values.
// These slip through when the entity filter runs before edge rewriting.
func checkRawValueEndpoint(edge models.CandidateEdge) HardConstraintResult {
	for _, endpoint := range []string{edge.FromMention, edge.ToMention} {
		name := strings.ToLower(strings.TrimSpace(endpoint))
		if looksLikeRawTemporalValue(name) {
			return HardConstraintResult{
				Pass:   false,
				Reason: "raw value endpoint '" + endpoint + "' is not a valid entity",
			}
		}
	}
	return HardConstraintResult{Pass: true}
}

// looksLikeRawTemporalValue checks if a mention looks like a date, time, day-of-week,
// or schedule fragment that should not be a standalone entity.
func looksLikeRawTemporalValue(s string) bool {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^\d{1,2}:\d{2}$`),
		regexp.MustCompile(`^\d{1,2}:\d{2}\s`),
		regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`),
		regexp.MustCompile(`^q[1-4]\s+\d{4}$`),
		regexp.MustCompile(`^(daily|weekly|monthly|yearly|biweekly)$`),
		regexp.MustCompile(`^(monday|tuesday|wednesday|thursday|friday|saturday|sunday)s?\b`),
		regexp.MustCompile(`^(twice|once|three times)\s+per\s+(day|week|month|year)$`),
		regexp.MustCompile(`\b\d+\s+(business\s+)?days?\b`),
		regexp.MustCompile(`at least .* days? in advance`),
		regexp.MustCompile(`^\d+\s*(am|pm)$`),
		regexp.MustCompile(`^every\s+`),
	}

	for _, p := range patterns {
		if p.MatchString(s) {
			return true
		}
	}
	return false
}

// checkAliasCompatibility rejects ALIAS_OF edges with incompatible types or generic targets.
func checkAliasCompatibility(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	if edge.RelationID != "ALIAS_OF" {
		return HardConstraintResult{Pass: true}
	}

	fromEnt := entities[edge.FromMention]
	toEnt := entities[edge.ToMention]
	if fromEnt == nil || toEnt == nil {
		return HardConstraintResult{Pass: true}
	}

	// Base types must overlap
	if !hasCompatibleBaseTypes(fromEnt.BaseTypes, toEnt.BaseTypes) {
		return HardConstraintResult{
			Pass:   false,
			Reason: "ALIAS_OF incompatible base types: " + edge.FromMention + " (" + strings.Join(fromEnt.BaseTypes, ",") + ") -> " + edge.ToMention + " (" + strings.Join(toEnt.BaseTypes, ",") + ")",
		}
	}

	// Target must not be a generic single-word entity
	if isGenericAliasTarget(edge.ToMention) {
		return HardConstraintResult{
			Pass:   false,
			Reason: "ALIAS_OF target is too generic: " + edge.ToMention,
		}
	}

	return HardConstraintResult{Pass: true}
}

// hasCompatibleBaseTypes checks if two type lists share at least one type.
func hasCompatibleBaseTypes(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// isGenericAliasTarget returns true if the name is a single generic word
// that should not be the target of an ALIAS_OF relation.
func isGenericAliasTarget(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	generic := map[string]bool{
		"diagnostics": true, "services": true, "system": true,
		"portal": true, "branch": true, "clinic": true,
		"laboratory": true, "center": true, "office": true,
		"department": true, "unit": true, "site": true,
	}
	return generic[name]
}
