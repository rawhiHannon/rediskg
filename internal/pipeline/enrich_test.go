package pipeline

import (
	"testing"

	"rediskg/pkg/models"
)

func svc(name string) models.CandidateEntity {
	return models.CandidateEntity{
		Mention:       name,
		CanonicalName: name,
		BaseTypes:     []models.ScoredType{{Type: "service", Score: 0.9}},
	}
}

func TestAddServiceCanonRules(t *testing.T) {
	// Tests the two *generic* collapses the post-refactor function still
	// does: singular -> plural (when both forms appear) and bare-modifier
	// stripping driven by schema.Canonicalization.ServiceModifiers from
	// ontology.json. The healthcare-specific synonym table was deleted.
	entities := []models.CandidateEntity{
		svc("blood test"), svc("blood tests"),
		svc("basic blood tests"),
		svc("vaccination"), svc("vaccinations"),
	}
	aliasMap := map[string]string{}
	addServiceCanonRules(entities, aliasMap)

	want := map[string]string{
		"blood test":        "blood tests", // singular/plural
		"basic blood tests": "blood tests", // modifier strip ("basic ")
		"vaccination":       "vaccinations", // singular/plural
	}
	for k, v := range want {
		if aliasMap[k] != v {
			t.Errorf("aliasMap[%q] = %q, want %q", k, aliasMap[k], v)
		}
	}

	// "corporate blood panels" used to map via the synonym table; without
	// the table, the bare form "blood panels" isn't in the corpus so
	// nothing collapses. That's the intended behaviour after the removal.
	mustNotCollapse := []string{"corporate blood panels", "routine vaccination administration"}
	for _, k := range mustNotCollapse {
		if _, ok := aliasMap[k]; ok {
			t.Errorf("aliasMap[%q] should be absent without domain synonym table; got %q", k, aliasMap[k])
		}
	}
}

func TestExtractTemporalFacts(t *testing.T) {
	fg := &models.FinalGraph{
		Entities: []models.KGEntity{
			{CanonicalName: "cedargate haifa central", BaseTypes: []string{"organization"}},
		},
		Edges: []models.KGEdge{
			{
				From: "cedargate health network", RelationID: "HAS_BRANCH", To: "cedargate haifa central",
				Evidence: []models.EvidenceRef{{Text: "Cedargate Haifa Central opened on 2024-05-19."}},
			},
			{
				From: "cedargate health network", RelationID: "CONTRACTED_WITH", To: "al-amal laboratory",
				Evidence: []models.EvidenceRef{{Text: "The Al-Amal agreement is valid through December 2026."}},
			},
		},
	}
	extractTemporalFacts(fg)

	if got := fg.Edges[0].Temporal["opened_on"]; got != "2024-05-19" {
		t.Errorf("opened_on = %q, want 2024-05-19", got)
	}
	if got := fg.Edges[1].Temporal["valid_through"]; got != "December 2026" {
		t.Errorf("valid_through = %q, want 'December 2026'", got)
	}
	if got := fg.Entities[0].Properties["opened_on"]; got != "2024-05-19" {
		t.Errorf("branch entity opened_on = %v, want 2024-05-19", got)
	}
}

func TestCompleteBranchEdges(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"cedargate health network": {CanonicalName: "cedargate health network", BaseTypes: []string{"organization"}},
		"cedargate akko family clinic": {CanonicalName: "cedargate akko family clinic", BaseTypes: []string{"organization"}, DomainTypes: []string{"clinic"}, Status: "active"},
		"cedargate haifa central":  {CanonicalName: "cedargate haifa central", BaseTypes: []string{"organization"}, DomainTypes: []string{"branch_office"}, Status: "active"},
		"northlab diagnostics":     {CanonicalName: "northlab diagnostics", BaseTypes: []string{"organization"}, DomainTypes: []string{"lab"}, Status: "active"},
	}
	edges := []models.CandidateEdge{
		{FromMention: "cedargate health network", RelationID: "HAS_BRANCH", ToMention: "cedargate akko family clinic"},
	}
	out := completeBranchEdges(edges, entities)

	found := false
	for _, e := range out {
		if e.FromMention == "cedargate health network" && e.ToMention == "cedargate haifa central" && e.RelationID == "HAS_BRANCH" {
			found = true
		}
		if e.ToMention == "northlab diagnostics" {
			t.Errorf("must not link unrelated org northlab diagnostics as a branch")
		}
	}
	if !found {
		t.Errorf("expected synthetic HAS_BRANCH edge to cedargate haifa central")
	}
}
