package solver

import (
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
	// Constraint 1: Document titles cannot become business entities
	if r := checkDocumentTitle(edge, entities); !r.Pass {
		return r
	}

	// Constraint 2: Planned entities cannot have active-only relations
	if r := checkPlannedStatus(edge, entities); !r.Pass {
		return r
	}

	// Constraint 3: Internal branches cannot partner with parent
	if r := checkBranchPartnership(edge, entities); !r.Pass {
		return r
	}

	// Constraint 4: Deputy managers cannot be main managers
	if r := checkDeputyManager(edge, entities); !r.Pass {
		return r
	}

	// Constraint 5: Alias endpoints cannot own normal facts
	if r := checkAliasEndpoint(edge, aliasMap); !r.Pass {
		return r
	}

	// Constraint 6: Relation signature must match schema (base types)
	if r := checkRelationSignature(edge, entities); !r.Pass {
		return r
	}

	// Constraint 7: Relation must satisfy functional role rules
	if r := checkRelationRoles(edge, entities); !r.Pass {
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
		return HardConstraintResult{Pass: true} // unknown relation — let through for now
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

// Constraint 7: Relation must satisfy functional role rules.
// This is the domain-agnostic replacement for hardcoded domain checks.
func checkRelationRoles(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	rule := schema.GetRelationRule(edge.RelationID)
	if rule == nil {
		return HardConstraintResult{Pass: true} // no role rule defined
	}

	fromEnt := entities[edge.FromMention]
	toEnt := entities[edge.ToMention]

	// Check source roles
	if len(rule.SourceRoles) > 0 && fromEnt != nil {
		if !hasAnyRole(fromEnt, rule.SourceRoles) {
			return HardConstraintResult{
				Pass:   false,
				Reason: edge.FromMention + " lacks required role for " + edge.RelationID + " source (needs one of: " + strings.Join(rule.SourceRoles, ", ") + ")",
			}
		}
	}

	// Check target roles
	if len(rule.TargetRoles) > 0 && toEnt != nil {
		if !hasAnyRole(toEnt, rule.TargetRoles) {
			return HardConstraintResult{
				Pass:   false,
				Reason: edge.ToMention + " lacks required role for " + edge.RelationID + " target (needs one of: " + strings.Join(rule.TargetRoles, ", ") + ")",
			}
		}
	}

	return HardConstraintResult{Pass: true}
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
