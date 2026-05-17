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

	// Constraint 2: Planned branches cannot have active relations
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

	// Constraint 6: Relation signature must match schema
	if r := checkRelationSignature(edge, entities); !r.Pass {
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

// Constraint 2: Planned entities cannot have active relations.
func checkPlannedStatus(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	activeRelations := map[string]bool{
		"HAS_BRANCH": true, "OPERATES": true, "OFFERS": true,
		"PROVIDES_SERVICE_FOR": true, "BASED_AT": true,
		"MANAGES": true, "VISITS": true,
	}

	if !activeRelations[edge.RelationID] {
		return HardConstraintResult{Pass: true}
	}

	plannedKeywords := []string{"planned", "future", "upcoming", "not yet open", "under construction"}

	for _, endpoint := range []string{edge.FromMention, edge.ToMention} {
		ent := entities[endpoint]
		if ent == nil {
			continue
		}
		// Check evidence for planned keywords
		for _, ev := range ent.Evidence {
			lower := strings.ToLower(ev.Text)
			for _, kw := range plannedKeywords {
				if strings.Contains(lower, kw) {
					return HardConstraintResult{
						Pass:   false,
						Reason: "active relation " + edge.RelationID + " involves planned entity '" + endpoint + "'",
					}
				}
			}
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 3: Internal branches cannot be partners of their parent.
func checkBranchPartnership(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	if edge.RelationID != "PARTNERS_WITH" {
		return HardConstraintResult{Pass: true}
	}

	fromEnt := entities[edge.FromMention]
	toEnt := entities[edge.ToMention]
	if fromEnt == nil || toEnt == nil {
		return HardConstraintResult{Pass: true}
	}

	// Check if one is likely a branch of the other (shared prefix/name)
	fromLower := strings.ToLower(edge.FromMention)
	toLower := strings.ToLower(edge.ToMention)

	// Simple heuristic: if one name is a prefix/substring of the other
	if strings.Contains(fromLower, toLower) || strings.Contains(toLower, fromLower) {
		// Likely parent-child, not partners
		return HardConstraintResult{
			Pass:   false,
			Reason: "PARTNERS_WITH between likely parent-child: " + edge.FromMention + " / " + edge.ToMention,
		}
	}

	return HardConstraintResult{Pass: true}
}

// Constraint 4: Deputy managers cannot be main managers.
func checkDeputyManager(edge models.CandidateEdge, entities map[string]*models.CanonicalEntity) HardConstraintResult {
	if edge.RelationID != "MANAGES" {
		return HardConstraintResult{Pass: true}
	}

	fromEnt := entities[edge.FromMention]
	if fromEnt == nil {
		return HardConstraintResult{Pass: true}
	}

	deputyKeywords := []string{"deputy", "acting", "interim", "assistant manager", "vice manager"}

	// Check entity's evidence for deputy role
	for _, ev := range fromEnt.Evidence {
		lower := strings.ToLower(ev.Text)
		for _, kw := range deputyKeywords {
			if strings.Contains(lower, kw) {
				// Convert to HAS_DEPUTY_MANAGER with flipped direction
				fixed := edge
				fixed.RelationID = "HAS_DEPUTY_MANAGER"
				fixed.FromMention, fixed.ToMention = edge.ToMention, edge.FromMention
				return HardConstraintResult{
					Pass:    false,
					Reason:  edge.FromMention + " is deputy, converting MANAGES to HAS_DEPUTY_MANAGER",
					FixEdge: &fixed,
				}
			}
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
