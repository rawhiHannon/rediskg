package pipeline

import (
	"math"
	"reflect"
	"testing"
)

func TestDetectQuestionType(t *testing.T) {
	cases := map[string]string{
		"Is Alice a developer?":           "yes/no",
		"Are these branches active?":      "yes/no",
		"Did the project succeed?":        "yes/no",
		"Who manages the haifa clinic?":   "person",
		"Where is Akko located?":          "place",
		"When was the contract signed?":   "time",
		"How many workers are there?":     "quantity",
		"How much does it cost?":          "quantity",
		"What services does X offer?":     "",
		"List the branches":               "",
	}
	for q, expectKey := range cases {
		hint := detectQuestionType(q)
		switch expectKey {
		case "yes/no":
			if hint == "" || !contains(hint, "yes/no") {
				t.Errorf("%q hint = %q, want yes/no", q, hint)
			}
		case "person":
			if !contains(hint, "person") {
				t.Errorf("%q hint = %q, want person", q, hint)
			}
		case "place":
			if !contains(hint, "place") {
				t.Errorf("%q hint = %q, want place", q, hint)
			}
		case "time":
			if !contains(hint, "time") {
				t.Errorf("%q hint = %q, want time", q, hint)
			}
		case "quantity":
			if !contains(hint, "number") && !contains(hint, "quantity") {
				t.Errorf("%q hint = %q, want quantity", q, hint)
			}
		case "":
			if hint != "" {
				t.Errorf("%q hint = %q, want empty", q, hint)
			}
		}
	}
}

func TestIsEnumerationQuery(t *testing.T) {
	yes := []string{
		"list all branches",
		"name every worker",
		"what is the complete list of services",
		"enumerate the partners",
		"all of the clinics in Haifa",
	}
	no := []string{
		"who works at Akko",
		"is dermatology offered at Carmel West",
		"what services does Haifa Central provide",
	}
	for _, q := range yes {
		if !isEnumerationQuery(q) {
			t.Errorf("expected enumeration: %q", q)
		}
	}
	for _, q := range no {
		if isEnumerationQuery(q) {
			t.Errorf("expected NOT enumeration: %q", q)
		}
	}
}

func TestFilterFactsByRelevance(t *testing.T) {
	in := []scoredFact{
		{Text: "high", Score: 0.9},
		{Text: "above-thresh", Score: 0.5},
		{Text: "below-thresh", Score: 0.1},
		{Text: "barely-min-keep", Score: 0.05},
		{Text: "noise", Score: 0.01},
	}
	got := filterFactsByRelevance(in, 0.25, 12, 3)
	// minKeep=3: top 3 always kept. After that only score >= 0.25.
	want := []string{"high", "above-thresh", "below-thresh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filterFactsByRelevance:\n got: %v\nwant: %v", got, want)
	}
}

func TestFilterFactsByRelevanceEmpty(t *testing.T) {
	if got := filterFactsByRelevance(nil, 0.25, 12, 3); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestCosineSim(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if s := cosineSim(a, b); math.Abs(s-1.0) > 1e-6 {
		t.Errorf("identical vectors: %v", s)
	}
	c := []float32{0, 1, 0}
	if s := cosineSim(a, c); math.Abs(s) > 1e-6 {
		t.Errorf("orthogonal vectors: %v", s)
	}
	d := []float32{1, 1, 0}
	if s := cosineSim(a, d); math.Abs(s-1.0/math.Sqrt(2)) > 1e-6 {
		t.Errorf("45° vectors: %v", s)
	}
	// Empty/zero vectors don't blow up.
	if s := cosineSim(nil, a); s != 0 {
		t.Errorf("nil first: %v", s)
	}
	if s := cosineSim(a, nil); s != 0 {
		t.Errorf("nil second: %v", s)
	}
}

func TestParseKeywordsJSON(t *testing.T) {
	cases := map[string][]string{
		`{"names": ["Alice", "Bob"]}`:                {"Alice", "Bob"},
		`prefix junk {"names": ["X"]} suffix`:        {"X"},
		`{"names": []}`:                              nil,
		`not valid json at all`:                      nil,
		`{"names": ["with 'quotes'", "padded "]}`:    {"with 'quotes'", "padded"},
	}
	for in, want := range cases {
		got := parseKeywordsJSON(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseKeywordsJSON(%q):\n got: %v\nwant: %v", in, got, want)
		}
	}
}

func TestEntityPoolDedup(t *testing.T) {
	p := newEntityPool()
	p.add("Yara Haddad", "manager", "cypher_exact")
	p.add("yara haddad", "(duplicate)", "vector") // same key, must not overwrite
	p.add("Tomer Shalev", "", "vector")
	if len(p.order) != 2 {
		t.Errorf("expected 2 entities after dedup, got %d", len(p.order))
	}
	if p.items["yara haddad"].Source != "cypher_exact" {
		t.Errorf("first-write-wins broken: source = %q", p.items["yara haddad"].Source)
	}
	if got := p.list(10)[0].Name; got != "Yara Haddad" {
		t.Errorf("expected original name preserved, got %q", got)
	}
}

func TestChunkPoolDedup(t *testing.T) {
	p := newChunkPool()
	p.add("c1", "text one", "vector")
	p.add("c1", "text one again", "mentioned_in") // duplicate id
	p.add("c2", "text two", "mentioned_in")
	if len(p.order) != 2 {
		t.Errorf("expected 2 chunks after dedup, got %d", len(p.order))
	}
	if p.items["c1"].Source != "vector" {
		t.Errorf("first-write-wins broken")
	}
}

func TestAssembleSectionsHintComesFirst(t *testing.T) {
	res := assembleSections(
		"Is Alice a manager?",
		[]*retrievedEntity{{Name: "Alice", Description: "engineer"}},
		[]string{"Alice —[WORKS_AT]→ Acme"},
		[]string{"Alice manages a team"},
		[]string{"[Source: a.md]\nAlice is a senior engineer at Acme."},
	)
	if res.HintSection == "" || !contains(res.HintSection, "yes/no") {
		t.Errorf("hint missing or wrong: %q", res.HintSection)
	}
	if res.EntitiesSection == "" || !contains(res.EntitiesSection, "Alice: engineer") {
		t.Errorf("entities section missing or wrong: %q", res.EntitiesSection)
	}
	if !contains(res.RelationshipsSection, "WORKS_AT") {
		t.Errorf("relationships section missing or wrong: %q", res.RelationshipsSection)
	}
	if !contains(res.FactsSection, "Alice manages") {
		t.Errorf("facts section missing or wrong: %q", res.FactsSection)
	}
	if !contains(res.PassagesSection, "[Source: a.md]") {
		t.Errorf("passages section missing or wrong: %q", res.PassagesSection)
	}
	ctx := res.assembledContext()
	// Hint must appear before entities in the joined context.
	if idxOf(ctx, "yes/no") > idxOf(ctx, "## Key Entities") {
		t.Errorf("hint must come before entities section")
	}
}

func contains(s, sub string) bool { return idxOf(s, sub) >= 0 }

func idxOf(s, sub string) int {
	// avoid importing strings just for the test
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
