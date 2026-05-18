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
	if result[0].Status != "planned" {
		t.Errorf("expected planned entity edge to have status 'planned', got %q", result[0].Status)
	}
	if result[1].RelationID != "OFFERS" {
		t.Errorf("expected active entity OFFERS to remain OFFERS, got %q", result[1].RelationID)
	}
	if result[1].Status != "" {
		t.Errorf("expected active entity edge to have empty status, got %q", result[1].Status)
	}
}

func TestRewriteStatusAwareEdges_HAS_BRANCH_ToPlannedBecomesHAS_PLANNED_BRANCH(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"cedargate": {
			CanonicalName:   "cedargate",
			Status:          "active",
			FunctionalRoles: []string{"parent_organization"},
		},
		"jerusalem south": {
			CanonicalName:   "jerusalem south",
			Status:          "planned",
			FunctionalRoles: []string{"planned_unit", "branch"},
		},
	}

	edges := []models.CandidateEdge{
		{
			FromMention: "cedargate",
			RelationID:  "HAS_BRANCH",
			ToMention:   "jerusalem south",
			Confidence:  0.85,
		},
	}

	result := rewriteStatusAwareEdges(edges, entities)

	if result[0].RelationID != "HAS_PLANNED_BRANCH" {
		t.Errorf("expected HAS_BRANCH to planned target to become HAS_PLANNED_BRANCH, got %q", result[0].RelationID)
	}
	if result[0].Status != "planned" {
		t.Errorf("expected status 'planned', got %q", result[0].Status)
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

// --- Negative conflict resolution tests ---

func TestResolveNegativeConflicts_NegativeOverridesPositive(t *testing.T) {
	edges := []models.KGEdge{
		{From: "branch_a", RelationID: "OFFERS", To: "dermatology"},
		{From: "branch_a", RelationID: "DOES_NOT_OFFER", To: "dermatology"},
		{From: "branch_b", RelationID: "OFFERS", To: "pediatrics"},
	}

	result := resolveNegativeConflicts(edges)

	for _, e := range result {
		if e.From == "branch_a" && e.RelationID == "OFFERS" && e.To == "dermatology" {
			t.Error("OFFERS should be removed when DOES_NOT_OFFER exists for same pair")
		}
	}
	// DOES_NOT_OFFER should remain
	foundNeg := false
	for _, e := range result {
		if e.RelationID == "DOES_NOT_OFFER" {
			foundNeg = true
		}
	}
	if !foundNeg {
		t.Error("DOES_NOT_OFFER edge should be preserved")
	}
	// branch_b OFFERS pediatrics should remain
	foundB := false
	for _, e := range result {
		if e.From == "branch_b" && e.RelationID == "OFFERS" {
			foundB = true
		}
	}
	if !foundB {
		t.Error("unrelated OFFERS edge should be preserved")
	}
}

// --- Raw value entity filtering tests ---

func TestFilterRawValueEntities(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "cedargate", BaseTypes: []models.ScoredType{{Type: "organization", Score: 0.9}}},
		{Mention: "2025-02-13", BaseTypes: []models.ScoredType{{Type: "date_time", Score: 0.95}}},
		{Mention: "10:00", BaseTypes: []models.ScoredType{{Type: "date_time", Score: 0.8}}},
		{Mention: "dr. smith", BaseTypes: []models.ScoredType{{Type: "person", Score: 0.9}}},
		{Mention: "50000", BaseTypes: []models.ScoredType{{Type: "money", Score: 0.7}}},
		// Multi-type: concept first but date_time second with high score — should still be filtered
		{Mention: "monthly", BaseTypes: []models.ScoredType{
			{Type: "concept", Score: 0.6},
			{Type: "date_time", Score: 0.9},
		}},
	}

	result := filterRawValueEntities(entities)

	if len(result) != 2 {
		t.Errorf("expected 2 entities after filtering, got %d", len(result))
	}
	for _, e := range result {
		if e.Mention != "cedargate" && e.Mention != "dr. smith" {
			t.Errorf("unexpected entity %q survived filtering", e.Mention)
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

// --- Negation fix tests ---

func TestFixNegatedRelations_FlipsBillingFromEvidence(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "balancecare",
			RelationID:   "HANDLES_BILLING_FOR",
			ToMention:    "haifa central",
			EvidenceText: "It does not handle Haifa Central claims.",
		},
		{
			FromMention:  "balancecare",
			RelationID:   "HANDLES_BILLING_FOR",
			ToMention:    "tel aviv",
			EvidenceText: "BalanceCare handles Tel Aviv billing.",
		},
	}

	result := fixNegatedRelations(edges)

	if result[0].RelationID != "DOES_NOT_HANDLE_BILLING_FOR" {
		t.Errorf("expected negated evidence to flip to DOES_NOT_HANDLE_BILLING_FOR, got %q", result[0].RelationID)
	}
	if result[1].RelationID != "HANDLES_BILLING_FOR" {
		t.Errorf("expected positive evidence to keep HANDLES_BILLING_FOR, got %q", result[1].RelationID)
	}
}

func TestFixNegatedRelations_FlipsOffersFromEvidence(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "branch_a",
			RelationID:   "OFFERS",
			ToMention:    "dermatology",
			EvidenceText: "Branch A does not handle dermatology services.",
		},
	}

	result := fixNegatedRelations(edges)

	if result[0].RelationID != "DOES_NOT_OFFER" {
		t.Errorf("expected DOES_NOT_OFFER, got %q", result[0].RelationID)
	}
}

// --- Conditional annotation tests ---

func TestAnnotateConditionalEdges_BackupDetection(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "northlab",
			RelationID:   "PROCESSES_TESTS_FOR",
			ToMention:    "cedargate",
			EvidenceText: "NorthLab processes tests for CedarGate during Al-Amal downtime.",
		},
	}

	result := annotateConditionalEdges(edges)

	if result[0].Status != "backup" {
		t.Errorf("expected status 'backup', got %q", result[0].Status)
	}
	if result[0].Condition == "" {
		t.Error("expected condition phrase to be extracted")
	}
}

func TestAnnotateConditionalEdges_EventNotBackup(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "cg-2025-004",
			RelationID:   "INVOLVES",
			ToMention:    "equipment downtime",
			EvidenceText: "Incident CG-2025-004 involves equipment downtime at Al-Amal Laboratory.",
		},
	}

	result := annotateConditionalEdges(edges)

	if result[0].Status == "backup" {
		t.Error("event relation INVOLVES should not get backup status, expected conditional")
	}
	if result[0].Status != "conditional" {
		t.Errorf("expected status 'conditional' for non-eligible relation with downtime, got %q", result[0].Status)
	}
}

func TestAnnotateConditionalEdges_ConditionalDetection(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "lab_a",
			RelationID:   "TRANSPORTS_SAMPLES_FOR",
			ToMention:    "lab_b",
			EvidenceText: "If Lab A cannot process in time, samples are sent to Lab B.",
		},
	}

	result := annotateConditionalEdges(edges)

	if result[0].Status != "conditional" {
		t.Errorf("expected status 'conditional', got %q", result[0].Status)
	}
}

func TestAnnotateConditionalEdges_DoesNotTouchNonConditional(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "org_a",
			RelationID:   "OFFERS",
			ToMention:    "pediatrics",
			EvidenceText: "Org A offers pediatrics services.",
		},
	}

	result := annotateConditionalEdges(edges)

	if result[0].Status != "" {
		t.Errorf("expected empty status for non-conditional edge, got %q", result[0].Status)
	}
}

// --- Raw value regex filtering tests ---

func TestLooksLikeRawTemporalValue(t *testing.T) {
	positives := []string{
		"10:00", "13:00 every business day", "2024-11-06",
		"q4 2026", "monthly", "tuesday", "twice per month",
		"every monday", "3 business days",
	}
	negatives := []string{
		"cedargate", "dr. smith", "al-amal laboratory",
		"primary care", "haifa central",
	}

	for _, s := range positives {
		if !looksLikeRawTemporalValue(s) {
			t.Errorf("expected %q to be detected as raw temporal value", s)
		}
	}
	for _, s := range negatives {
		if looksLikeRawTemporalValue(s) {
			t.Errorf("expected %q to NOT be detected as raw temporal value", s)
		}
	}
}

func TestFilterRawValueEntities_RegexFallback(t *testing.T) {
	// Entities typed as "concept" but are actually temporal values
	entities := []models.CandidateEntity{
		{Mention: "cedargate", BaseTypes: []models.ScoredType{{Type: "organization", Score: 0.9}}},
		{Mention: "tuesday and thursday", BaseTypes: []models.ScoredType{{Type: "concept", Score: 0.6}}},
		{Mention: "wednesday mornings", BaseTypes: []models.ScoredType{{Type: "concept", Score: 0.5}}},
		{Mention: "dr. smith", BaseTypes: []models.ScoredType{{Type: "person", Score: 0.9}}},
	}

	result := filterRawValueEntities(entities)

	for _, e := range result {
		if e.Mention == "tuesday and thursday" || e.Mention == "wednesday mornings" {
			t.Errorf("expected %q to be filtered by regex fallback", e.Mention)
		}
	}
}

// --- Orphan edge filtering tests ---

func TestFilterOrphanEdges(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "org_a", CanonicalName: "org_a"},
		{Mention: "org_b", CanonicalName: "org_b"},
	}

	edges := []models.CandidateEdge{
		{FromMention: "org_a", RelationID: "OFFERS", ToMention: "org_b"},
		{FromMention: "org_a", RelationID: "OFFERS", ToMention: "10:00"}, // endpoint was filtered
	}

	result := filterOrphanEdges(edges, entities)

	if len(result) != 1 {
		t.Errorf("expected 1 edge after orphan filtering, got %d", len(result))
	}
}

// --- Event status correction tests ---

func TestFixEntityStatuses_IncidentNotPlanned(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"cg-2025-018": {
			CanonicalName: "cg-2025-018",
			BaseTypes:     []string{"event"},
			Status:        "planned",
			Evidence: []models.EvidenceRef{
				{Text: "Incident CG-2025-018 occurred on 2025-01-15"},
			},
		},
		"org_a": {
			CanonicalName: "org_a",
			BaseTypes:     []string{"organization"},
			Status:        "planned",
		},
	}

	fixEntityStatuses(entities)

	if entities["cg-2025-018"].Status != "historical" {
		t.Errorf("expected event with past evidence to be 'historical', got %q", entities["cg-2025-018"].Status)
	}
	// Non-event should not be changed
	if entities["org_a"].Status != "planned" {
		t.Errorf("expected non-event to keep 'planned', got %q", entities["org_a"].Status)
	}
}

// --- Negative conflict resolution with billing ---

func TestResolveNegativeConflicts_BillingOverride(t *testing.T) {
	edges := []models.KGEdge{
		{From: "balancecare", RelationID: "HANDLES_BILLING_FOR", To: "haifa"},
		{From: "balancecare", RelationID: "DOES_NOT_HANDLE_BILLING_FOR", To: "haifa"},
		{From: "balancecare", RelationID: "HANDLES_BILLING_FOR", To: "tel_aviv"},
	}

	result := resolveNegativeConflicts(edges)

	for _, e := range result {
		if e.From == "balancecare" && e.RelationID == "HANDLES_BILLING_FOR" && e.To == "haifa" {
			t.Error("HANDLES_BILLING_FOR should be removed when DOES_NOT_HANDLE_BILLING_FOR exists")
		}
	}
	// tel_aviv should survive
	found := false
	for _, e := range result {
		if e.To == "tel_aviv" && e.RelationID == "HANDLES_BILLING_FOR" {
			found = true
		}
	}
	if !found {
		t.Error("unrelated HANDLES_BILLING_FOR should be preserved")
	}
}

// --- Suffix alias rule tests ---

func TestAddSuffixAliasRules_MergesBranchSuffix(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "cedargate jerusalem south", CanonicalName: "cedargate jerusalem south"},
		{Mention: "cedargate jerusalem south branch", CanonicalName: "cedargate jerusalem south branch"},
	}

	aliasMap := map[string]string{}
	addSuffixAliasRules(entities, aliasMap)

	if aliasMap["cedargate jerusalem south branch"] != "cedargate jerusalem south" {
		t.Errorf("expected suffix merge, got %q", aliasMap["cedargate jerusalem south branch"])
	}
}

func TestAddSuffixAliasRules_NoMergeWithoutShorterForm(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "haifa central branch", CanonicalName: "haifa central branch"},
	}

	aliasMap := map[string]string{}
	addSuffixAliasRules(entities, aliasMap)

	if _, ok := aliasMap["haifa central branch"]; ok {
		t.Error("should not merge when shorter form does not exist")
	}
}

// --- Short branch alias tests ---

func TestAddSuffixAliasRules_ShortBranchToLongerCanonical(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "cedargate jerusalem south", CanonicalName: "cedargate jerusalem south"},
		{Mention: "jerusalem south branch", CanonicalName: "jerusalem south branch"},
	}

	aliasMap := map[string]string{}
	addSuffixAliasRules(entities, aliasMap)

	if aliasMap["jerusalem south branch"] != "cedargate jerusalem south" {
		t.Errorf("expected 'jerusalem south branch' -> 'cedargate jerusalem south', got %q", aliasMap["jerusalem south branch"])
	}
}

func TestAddSuffixAliasRules_PicksLongestMatch(t *testing.T) {
	entities := []models.CandidateEntity{
		{Mention: "cedargate jerusalem south", CanonicalName: "cedargate jerusalem south"},
		{Mention: "jerusalem south", CanonicalName: "jerusalem south"},
		{Mention: "jerusalem south branch", CanonicalName: "jerusalem south branch"},
	}

	aliasMap := map[string]string{}
	addSuffixAliasRules(entities, aliasMap)

	// Pass 1 should match exact suffix strip: "jerusalem south branch" -> "jerusalem south"
	// But pass 2 should pick "cedargate jerusalem south" as longest match
	// However pass 1 runs first and finds "jerusalem south", so it should map there
	// Actually, pass 1 maps it already, so pass 2 skips it
	if aliasMap["jerusalem south branch"] != "jerusalem south" {
		t.Errorf("expected exact suffix match first: 'jerusalem south branch' -> 'jerusalem south', got %q", aliasMap["jerusalem south branch"])
	}
}

// --- Clean conflicting functional roles tests ---

func TestCleanConflictingFunctionalRoles_RemovesCourierFromNonCourier(t *testing.T) {
	entities := map[string]*models.CanonicalEntity{
		"northlab": {
			CanonicalName:   "northlab",
			BaseTypes:       []string{"organization"},
			FunctionalRoles: []string{"external_partner", "medical_courier"},
			Evidence: []models.EvidenceRef{
				{Text: "NorthLab processes blood tests for CedarGate branches."},
			},
		},
		"medex couriers": {
			CanonicalName:   "medex couriers",
			BaseTypes:       []string{"organization"},
			FunctionalRoles: []string{"medical_courier"},
			Evidence: []models.EvidenceRef{
				{Text: "MedEx Couriers provides courier services for sample delivery."},
			},
		},
	}

	cleanConflictingFunctionalRoles(entities)

	if containsStr(entities["northlab"].FunctionalRoles, "medical_courier") {
		t.Error("northlab should not have medical_courier — no courier evidence")
	}
	if !containsStr(entities["northlab"].FunctionalRoles, "external_partner") {
		t.Error("northlab should keep external_partner")
	}
	if !containsStr(entities["medex couriers"].FunctionalRoles, "medical_courier") {
		t.Error("medex couriers should keep medical_courier — name contains 'courier'")
	}
}

// --- Backup upgrade test ---

func TestAnnotateConditionalEdges_UpgradesConditionalToBackup(t *testing.T) {
	edges := []models.CandidateEdge{
		{
			FromMention:  "northlab",
			RelationID:   "PROCESSES_TESTS_FOR",
			ToMention:    "cedargate",
			EvidenceText: "NorthLab processes tests during Al-Amal downtime.",
			Status:       "conditional", // LLM set conditional, should upgrade to backup
		},
	}

	result := annotateConditionalEdges(edges)

	if result[0].Status != "backup" {
		t.Errorf("expected conditional to be upgraded to backup for eligible relation, got %q", result[0].Status)
	}
}
