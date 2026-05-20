package store

import (
	"strings"
	"testing"
)

func TestEncodeCypherStringEscapes(t *testing.T) {
	cases := map[string]string{
		"":               `''`,
		"hello":          `'hello'`,
		"it's":           `'it\'s'`,
		"line\nbreak":    `'line\nbreak'`,
		`back\slash`:     `'back\\slash'`,
	}
	for in, want := range cases {
		if got := EncodeCypherString(in); got != want {
			t.Errorf("EncodeCypherString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeCypherValue(t *testing.T) {
	if got := EncodeCypherValue(nil); got != "null" {
		t.Errorf("nil -> %q", got)
	}
	if got := EncodeCypherValue(true); got != "true" {
		t.Errorf("true -> %q", got)
	}
	if got := EncodeCypherValue(int64(42)); got != "42" {
		t.Errorf("int64(42) -> %q", got)
	}
	if got := EncodeCypherValue([]string{"a", "b"}); got != `['a', 'b']` {
		t.Errorf("[a,b] -> %q", got)
	}
}

func TestEncodeCypherMap(t *testing.T) {
	out := EncodeCypherMap(map[string]interface{}{
		"id":    "alice",
		"count": 3,
	})
	if !strings.HasPrefix(out, "{") || !strings.HasSuffix(out, "}") {
		t.Fatalf("not a map literal: %q", out)
	}
	if !strings.Contains(out, "id: 'alice'") || !strings.Contains(out, "count: 3") {
		t.Errorf("missing expected pairs: %q", out)
	}
}

func TestEncodeCypherParamsPrefix(t *testing.T) {
	p := EncodeCypherParams(map[string]interface{}{
		"batch": []interface{}{
			map[string]interface{}{"id": "a"},
		},
	})
	if !strings.HasPrefix(p, "CYPHER ") || !strings.HasSuffix(p, " ") {
		t.Errorf("bad prefix shape: %q", p)
	}
	if !strings.Contains(p, "batch=[") {
		t.Errorf("missing batch param: %q", p)
	}
}

func TestSanitizeCypherLabelRejectsBadChars(t *testing.T) {
	if _, err := SanitizeCypherLabel("ev:il"); err == nil {
		t.Errorf("colon-bearing label should be rejected")
	}
	if _, err := SanitizeCypherLabel("  "); err == nil {
		t.Errorf("whitespace-only label should be rejected")
	}
	if got, err := SanitizeCypherLabel("`Person`"); err != nil || got != "Person" {
		t.Errorf("backtick strip failed: got %q err %v", got, err)
	}
}

func TestIsCypherIdentifier(t *testing.T) {
	cases := map[string]bool{
		"id":       true,
		"start_id": true,
		"_x":       true,
		"":         false,
		"1abc":     false,
		"foo bar":  false,
	}
	for in, want := range cases {
		if got := IsCypherIdentifier(in); got != want {
			t.Errorf("IsCypherIdentifier(%q) = %v, want %v", in, got, want)
		}
	}
}
