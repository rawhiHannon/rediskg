package pipeline

import (
	"testing"

	"rediskg/pkg/models"
)

// --- Alias resolution tests ---

func TestBuildAliasMap_BasicAlias(t *testing.T) {
	entities := []models.CandidateEntity{
		{
			Mention:       "al-amal laboratory",
			CanonicalName: "al-amal laboratory",
			BaseTypes:     []models.ScoredType{{Type: "organization", Score: 0.9}},
			Aliases: []models.LangText{
				{Text: "aal", Lang: "en"},
				{Text: "al-amal lab", Lang: "en"},
			},
			Evidence: []models.EvidenceRef{{Text: "Al-Amal Laboratory (AAL)"}},
		},
	}

	aliasMap := buildAliasMap(entities)

	if aliasMap["aal"] != "al-amal laboratory" {
		t.Errorf("expected aal -> al-amal laboratory, got %q", aliasMap["aal"])
	}
	if aliasMap["al-amal lab"] != "al-amal laboratory" {
		t.Errorf("expected al-amal lab -> al-amal laboratory, got %q", aliasMap["al-amal lab"])
	}
}

func TestBuildAliasMap_CaseNormalization(t *testing.T) {
	entities := []models.CandidateEntity{
		{
			Mention:       "CedarGate Health Network",
			CanonicalName: "cedargate health network",
			BaseTypes:     []models.ScoredType{{Type: "organization", Score: 0.95}},
		},
	}

	aliasMap := buildAliasMap(entities)

	// Mention (lowercased) should map to canonical
	if _, ok := aliasMap["cedargate health network"]; ok {
		t.Error("canonical should not be in alias map pointing to itself")
	}
}

// --- Canonical entity selection tests ---

func TestSelectCanonicalEntities_MergesDuplicates(t *testing.T) {
	entities := []models.CandidateEntity{
		{
			Mention:       "cedar branch north",
			CanonicalName: "cedar branch north",
			BaseTypes:     []models.ScoredType{{Type: "organization", Score: 0.9}},
			FunctionalRoles: []string{"branch"},
			Status:        "active",
		},
		{
			Mention:       "cedar branch north",
			CanonicalName: "cedar branch north",
			BaseTypes:     []models.ScoredType{{Type: "location", Score: 0.6}},
			FunctionalRoles: []string{"operated_unit"},
		},
	}

	aliasMap := map[string]string{}
	canonical := selectCanonicalEntities(entities, aliasMap)

	ent, ok := canonical["cedar branch north"]
	if !ok {
		t.Fatal("expected canonical entity 'cedar branch north'")
	}

	if len(ent.BaseTypes) < 1 || ent.BaseTypes[0] != "organization" {
		t.Errorf("expected primary base type 'organization', got %v", ent.BaseTypes)
	}
	if ent.Status != "active" {
		t.Errorf("expected status 'active', got %q", ent.Status)
	}
	if !containsStr(ent.FunctionalRoles, "branch") {
		t.Error("expected functional role 'branch'")
	}
	if !containsStr(ent.FunctionalRoles, "operated_unit") {
		t.Error("expected functional role 'operated_unit'")
	}
}

// --- Status-aware edge rewriting tests ---

func TestRewriteStatusAwareEdges_PlannedOffersBecomesPlannedService(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"jerusalem south": {
			CanonicalName:   "jerusalem south",
			Status:          "planned",
			FunctionalRoles: []string{"planned_unit", "branch"},
		},
	}

	edges := []models.CandidateEdge{
		{
			FromMention: "jerusalem south",
			RelationID:  "OFFERS",
			ToMention:   "primary care",
			Confidence:  0.85,
		},
		{
			FromMention: "haifa central",
			RelationID:  "OFFERS",
			ToMention:   "lab services",
			Confidence:  0.9,
		},
	}

	result := rewriteStatusAwareEdges(edges, entities)

	if result[0].RelationID != "PLANNED_SERVICE" {
		t.Errorf("expected planned entity OFFERS to become PLANNED_SERVICE, got %q", result[0].RelationID)
	}
	if result[1].RelationID != "OFFERS" {
		t.Errorf("expected active entity OFFERS to remain OFFERS, got %q", result[1].RelationID)
	}
}

// --- Post-solver validation tests ---

func TestPostSolverValidation_RemovesSelfLoops(t *testing.T) {
	fg := &models.FinalGraph{
		Entities: []models.KGEntity{
			{CanonicalName: "entity_a", BaseTypes: []string{"organization"}},
			{CanonicalName: "entity_b", BaseTypes: []string{"organization"}},
		},
		Edges: []models.KGEdge{
			{From: "entity_a", RelationID: "HAS_BRANCH", To: "entity_b"},
			{From: "entity_a", RelationID: "PART_OF", To: "entity_a"}, // self-loop
		},
	}

	result := postSolverValidation(fg, map[string]string{})

	if len(result.Edges) != 1 {
		t.Errorf("expected 1 edge after removing self-loop, got %d", len(result.Edges))
	}
}

func TestPostSolverValidation_RejectsStaleAliasEndpoints(t *testing.T) {
	aliasMap := map[string]string{
		"aal": "al-amal laboratory",
	}

	fg := &models.FinalGraph{
		Entities: []models.KGEntity{
			{CanonicalName: "aal", BaseTypes: []string{"organization"}},
			{CanonicalName: "al-amal laboratory", BaseTypes: []string{"organization"}},
			{CanonicalName: "cedargate", BaseTypes: []string{"organization"}},
		},
		Edges: []models.KGEdge{
			{From: "aal", RelationID: "PARTNERS_WITH", To: "cedargate"},                  // stale alias
			{From: "al-amal laboratory", RelationID: "PARTNERS_WITH", To: "cedargate"},   // valid
			{From: "aal", RelationID: "ALIAS_OF", To: "al-amal laboratory"},               // ALIAS_OF is ok
		},
	}

	result := postSolverValidation(fg, aliasMap)

	for _, edge := range result.Edges {
		if edge.From == "aal" && edge.RelationID == "PARTNERS_WITH" {
			t.Error("stale alias endpoint 'aal' should have been rejected for PARTNERS_WITH")
		}
	}

	// ALIAS_OF edge with alias endpoint should be kept
	foundAlias := false
	for _, edge := range result.Edges {
		if edge.RelationID == "ALIAS_OF" {
			foundAlias = true
		}
	}
	if !foundAlias {
		t.Error("ALIAS_OF edge should be preserved")
	}
}

func TestPostSolverValidation_RemovesOrphanEntities(t *testing.T) {
	fg := &models.FinalGraph{
		Entities: []models.KGEntity{
			{CanonicalName: "connected_a", BaseTypes: []string{"organization"}},
			{CanonicalName: "connected_b", BaseTypes: []string{"person"}},
			{CanonicalName: "orphan", BaseTypes: []string{"concept"}},
		},
		Edges: []models.KGEdge{
			{From: "connected_a", RelationID: "MANAGES", To: "connected_b"},
		},
	}

	result := postSolverValidation(fg, map[string]string{})

	if len(result.Entities) != 2 {
		t.Errorf("expected 2 entities (orphan removed), got %d", len(result.Entities))
	}
	for _, ent := range result.Entities {
		if ent.CanonicalName == "orphan" {
			t.Error("orphan entity should have been removed")
		}
	}
}

// --- Normalize relations test ---

func TestNormalizeRelations_RemovesSelfLoops(t *testing.T) {
	edges := []models.CandidateEdge{
		{FromMention: "entity_a", RelationID: "MANAGES", ToMention: "entity_b"},
		{FromMention: "entity_a", RelationID: "PART_OF", ToMention: "entity_a"}, // self-loop
	}

	result := normalizeRelations(edges)

	if len(result) != 1 {
		t.Errorf("expected 1 edge after self-loop removal, got %d", len(result))
	}
}
