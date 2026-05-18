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

// --- Raw value endpoint tests ---

func TestCheckRawValueEndpoint_RejectsTemporalEndpoint(t *testing.T) {
	edges := []models.CandidateEdge{
		{FromMention: "org_a", RelationID: "OFFERS", ToMention: "10:00"},
		{FromMention: "2024-11-06", RelationID: "OCCURRED_ON", ToMention: "org_b"},
		{FromMention: "org_a", RelationID: "OFFERS", ToMention: "pediatrics"},
	}

	result := ApplyHardConstraints(edges, map[string]*models.CanonicalEntity{
		"org_a":      {CanonicalName: "org_a", BaseTypes: []string{"organization"}},
		"org_b":      {CanonicalName: "org_b", BaseTypes: []string{"organization"}},
		"pediatrics": {CanonicalName: "pediatrics", BaseTypes: []string{"service"}},
	}, map[string]string{})

	for _, e := range result {
		if e.ToMention == "10:00" || e.FromMention == "2024-11-06" {
			t.Error("raw temporal endpoint should have been rejected")
		}
	}
}

// --- Alias compatibility tests ---

func TestCheckAliasCompatibility_RejectsIncompatibleTypes(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"nld": {
			CanonicalName: "nld",
			BaseTypes:     []string{"organization"},
		},
		"diagnostics": {
			CanonicalName: "diagnostics",
			BaseTypes:     []string{"service"},
		},
	}

	edges := []models.CandidateEdge{
		{FromMention: "nld", RelationID: "ALIAS_OF", ToMention: "diagnostics"},
	}

	result := ApplyHardConstraints(edges, entities, map[string]string{})
	if len(result) != 0 {
		t.Error("ALIAS_OF between organization and service should be rejected")
	}
}

func TestCheckAliasCompatibility_RejectsGenericTarget(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"nld": {
			CanonicalName: "nld",
			BaseTypes:     []string{"organization"},
		},
		"laboratory": {
			CanonicalName: "laboratory",
			BaseTypes:     []string{"organization"},
		},
	}

	edges := []models.CandidateEdge{
		{FromMention: "nld", RelationID: "ALIAS_OF", ToMention: "laboratory"},
	}

	result := ApplyHardConstraints(edges, entities, map[string]string{})
	if len(result) != 0 {
		t.Error("ALIAS_OF to generic target 'laboratory' should be rejected")
	}
}

func TestCheckAliasCompatibility_AllowsCompatible(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"aal": {
			CanonicalName: "aal",
			BaseTypes:     []string{"organization"},
		},
		"al-amal laboratory": {
			CanonicalName: "al-amal laboratory",
			BaseTypes:     []string{"organization"},
		},
	}

	edges := []models.CandidateEdge{
		{FromMention: "aal", RelationID: "ALIAS_OF", ToMention: "al-amal laboratory"},
	}

	result := ApplyHardConstraints(edges, entities, map[string]string{})
	if len(result) != 1 {
		t.Error("ALIAS_OF between compatible organizations should be allowed")
	}
}

// --- Negative relation evidence tests ---

func TestCheckNegativeRelationEvidence_RejectsWithoutNegation(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "balancecare",
			RelationID:   "DOES_NOT_HANDLE_REIMBURSEMENT_FOR",
			ToMention:    "carmel west",
			EvidenceText: "BalanceCare Insurance Services handles private reimbursement submissions for Carmel West patients.",
		},
	}

	result := ApplyHardConstraints(edges, map[string]*models.CanonicalEntity{
		"balancecare": {CanonicalName: "balancecare", BaseTypes: []string{"organization"}},
		"carmel west": {CanonicalName: "carmel west", BaseTypes: []string{"organization"}},
	}, map[string]string{})

	if len(result) != 0 {
		t.Error("negative relation with positive evidence should be rejected")
	}
}

func TestCheckNegativeRelationEvidence_AllowsWithNegation(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "balancecare",
			RelationID:   "DOES_NOT_HANDLE_BILLING_FOR",
			ToMention:    "haifa central",
			EvidenceText: "It does not handle Haifa Central claims.",
		},
	}

	result := ApplyHardConstraints(edges, map[string]*models.CanonicalEntity{
		"balancecare":  {CanonicalName: "balancecare", BaseTypes: []string{"organization"}},
		"haifa central": {CanonicalName: "haifa central", BaseTypes: []string{"organization"}},
	}, map[string]string{})

	if len(result) != 1 {
		t.Error("negative relation with negation evidence should be allowed")
	}
}
