package solver

import (
	"testing"

	"rediskg/pkg/models"
)

func TestApplyHardConstraints_RejectsAliasEndpoints(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"al-amal laboratory": {CanonicalName: "al-amal laboratory", BaseTypes: []string{"organization"}},
		"cedargate":          {CanonicalName: "cedargate", BaseTypes: []string{"organization"}},
	}
	aliasMap := map[string]string{"aal": "al-amal laboratory"}

	edges := []models.CandidateEdge{
		{FromMention: "aal", RelationID: "PARTNERS_WITH", ToMention: "cedargate"},
	}

	result := ApplyHardConstraints(edges, entities, aliasMap)
	if len(result) != 0 {
		t.Error("alias endpoint 'aal' should be rejected for PARTNERS_WITH")
	}
}

func TestApplyHardConstraints_AllowsAliasOf(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"aal":                {CanonicalName: "aal", BaseTypes: []string{"organization"}},
		"al-amal laboratory": {CanonicalName: "al-amal laboratory", BaseTypes: []string{"organization"}},
	}
	aliasMap := map[string]string{"aal": "al-amal laboratory"}

	edges := []models.CandidateEdge{
		{FromMention: "aal", RelationID: "ALIAS_OF", ToMention: "al-amal laboratory"},
	}

	result := ApplyHardConstraints(edges, entities, aliasMap)
	if len(result) != 1 {
		t.Error("ALIAS_OF edge with alias endpoint should be allowed")
	}
}

func TestApplyHardConstraints_FlipsDeputyManager(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"sarah cohen": {
			CanonicalName:   "sarah cohen",
			BaseTypes:       []string{"person"},
			FunctionalRoles: []string{"deputy_manager", "staff_member"},
		},
		"north branch": {
			CanonicalName: "north branch",
			BaseTypes:     []string{"organization"},
		},
	}

	edges := []models.CandidateEdge{
		{FromMention: "sarah cohen", RelationID: "MANAGES", ToMention: "north branch"},
	}

	result := ApplyHardConstraints(edges, entities, map[string]string{})

	if len(result) != 1 {
		t.Fatalf("expected 1 fixed edge, got %d", len(result))
	}
	if result[0].RelationID != "HAS_DEPUTY_MANAGER" {
		t.Errorf("expected MANAGES to be converted to HAS_DEPUTY_MANAGER, got %q", result[0].RelationID)
	}
	if result[0].FromMention != "north branch" || result[0].ToMention != "sarah cohen" {
		t.Error("expected direction to be flipped: north branch -> sarah cohen")
	}
}

func TestApplyHardConstraints_RejectsBranchPartnership(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"cedargate": {
			CanonicalName:   "cedargate",
			BaseTypes:       []string{"organization"},
			FunctionalRoles: []string{"parent_organization"},
		},
		"north branch": {
			CanonicalName:   "north branch",
			BaseTypes:       []string{"organization"},
			FunctionalRoles: []string{"branch"},
		},
	}

	edges := []models.CandidateEdge{
		{FromMention: "cedargate", RelationID: "PARTNERS_WITH", ToMention: "north branch"},
	}

	result := ApplyHardConstraints(edges, entities, map[string]string{})
	if len(result) != 0 {
		t.Error("PARTNERS_WITH between parent and branch should be rejected")
	}
}
