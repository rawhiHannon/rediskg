package solver

import (
	"testing"

	"rediskg/pkg/models"
)

func TestAreCompetingRelations_ResolvesAliases(t *testing.T) {
	// WORKS_AT is an alias for BASED_AT; they should compete with VISITS
	relations := map[string]bool{
		"WORKS_AT": true,
		"VISITS":   true,
	}

	if !areCompetingRelations(relations) {
		t.Error("WORKS_AT (alias of BASED_AT) and VISITS should be competing relations")
	}
}

func TestAreCompetingRelations_NonCompeting(t *testing.T) {
	relations := map[string]bool{
		"HAS_BRANCH": true,
		"MANAGES":    true,
	}

	if areCompetingRelations(relations) {
		t.Error("HAS_BRANCH and MANAGES should not compete")
	}
}

func TestBuildAlternativeGroups_GroupsCompeting(t *testing.T) {
	edges := []models.CandidateEdge{
		{FromMention: "dr. smith", RelationID: "BASED_AT", ToMention: "north branch", Confidence: 0.9},
		{FromMention: "dr. smith", RelationID: "VISITS", ToMention: "north branch", Confidence: 0.6},
	}

	result := BuildAlternativeGroups(edges)

	if result[0].AlternativeGroup == "" || result[1].AlternativeGroup == "" {
		t.Error("competing BASED_AT and VISITS for same pair should be grouped")
	}
	if result[0].AlternativeGroup != result[1].AlternativeGroup {
		t.Error("competing edges should be in the same alternative group")
	}
}

func TestSelectFinalGraph_PicksBestFromGroup(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"dr. smith":    {CanonicalName: "dr. smith", BaseTypes: []string{"person"}},
		"north branch": {CanonicalName: "north branch", BaseTypes: []string{"organization"}},
	}

	edges := []models.CandidateEdge{
		{FromMention: "dr. smith", RelationID: "BASED_AT", ToMention: "north branch",
			Confidence: 0.9, EvidenceScore: 0.9, SchemaFitScore: 1.0, AlternativeGroup: "g1"},
		{FromMention: "dr. smith", RelationID: "VISITS", ToMention: "north branch",
			Confidence: 0.5, EvidenceScore: 0.5, SchemaFitScore: 1.0, AlternativeGroup: "g1"},
	}

	fg := SelectFinalGraph(edges, entities)

	if len(fg.Edges) != 1 {
		t.Fatalf("expected 1 edge from alternative group, got %d", len(fg.Edges))
	}
	if fg.Edges[0].RelationID != "BASED_AT" {
		t.Errorf("expected BASED_AT (higher score) to win, got %q", fg.Edges[0].RelationID)
	}
}

func TestDeduplicateEdges_MergesSame(t *testing.T) {
	edges := []models.CandidateEdge{
		{FromMention: "a", RelationID: "HAS_BRANCH", ToMention: "b", Confidence: 0.8},
		{FromMention: "a", RelationID: "HAS_BRANCH", ToMention: "b", Confidence: 0.7},
	}

	result := deduplicateEdges(edges)

	if len(result) != 1 {
		t.Errorf("expected 1 edge after dedup, got %d", len(result))
	}
}
