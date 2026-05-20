// Shared Cypher utilities: parameter binding and literal encoding.
//
// FalkorDB accepts query parameters by prepending a "CYPHER k=v k=v ..."
// header to the query string. The header is parsed server-side and the
// values are bound to ``$k`` placeholders before planning, so the same
// query template can be re-planned cheaply across many calls. That's the
// pattern the batched ingest writers rely on — group by label/type once,
// then re-use the same query template across batches.
package store

import (
	"fmt"
	"strings"
	"unicode"
)

// QueryWithParams runs a parameterised Cypher query. The query may reference
// parameters as ``$name`` and the values are encoded into the FalkorDB
// CYPHER prefix.
//
// Example::
//
//	s.QueryWithParams("UNWIND $batch AS item MERGE (n:Person {id: item.id})", map[string]any{
//	    "batch": []any{
//	        map[string]any{"id": "a"},
//	        map[string]any{"id": "b"},
//	    },
//	})
func (s *FalkorStore) QueryWithParams(cypher string, params map[string]interface{}) (interface{}, error) {
	if len(params) == 0 {
		return s.Query(cypher)
	}
	return s.Query(EncodeCypherParams(params) + cypher)
}

// ROQueryWithParams is the read-only variant of QueryWithParams.
func (s *FalkorStore) ROQueryWithParams(cypher string, params map[string]interface{}) (interface{}, error) {
	if len(params) == 0 {
		return s.ROQuery(cypher)
	}
	return s.ROQuery(EncodeCypherParams(params) + cypher)
}

// EncodeCypherParams builds the "CYPHER k1=v1 k2=v2 " prefix string. The
// trailing space is included so callers can concatenate directly with the
// query body. Non-identifier keys are skipped defensively.
func EncodeCypherParams(params map[string]interface{}) string {
	if len(params) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("CYPHER ")
	first := true
	for k, v := range params {
		if !IsCypherIdentifier(k) {
			continue
		}
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(EncodeCypherValue(v))
	}
	b.WriteByte(' ')
	return b.String()
}

// EncodeCypherValue serialises a Go value as a Cypher literal: strings get
// single-quoted, lists become bracketed, maps become braced.
func EncodeCypherValue(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		return EncodeCypherString(x)
	case int:
		return fmt.Sprintf("%d", x)
	case int32:
		return fmt.Sprintf("%d", x)
	case int64:
		return fmt.Sprintf("%d", x)
	case float32:
		return fmt.Sprintf("%g", x)
	case float64:
		return fmt.Sprintf("%g", x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = EncodeCypherString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []interface{}:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = EncodeCypherValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]interface{}:
		return EncodeCypherMap(x)
	default:
		return EncodeCypherString(fmt.Sprintf("%v", x))
	}
}

// EncodeCypherString quotes a string as a Cypher single-quoted literal with
// the standard escape set.
func EncodeCypherString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// EncodeCypherMap serialises a map as a Cypher map literal. Keys are
// validated as identifiers; non-identifier keys are skipped defensively.
func EncodeCypherMap(m map[string]interface{}) string {
	if len(m) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		if !IsCypherIdentifier(k) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", k, EncodeCypherValue(v)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// IsCypherIdentifier reports whether s is a valid Cypher identifier
// (letter|_ followed by letters/digits/underscores).
func IsCypherIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !(unicode.IsLetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

// SanitizeCypherLabel returns a label safe to interpolate inside
// backtick-quoted identifiers. Only [A-Za-z0-9_] survives.
func SanitizeCypherLabel(label string) (string, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(label), "`", "")
	if cleaned == "" {
		return "", fmt.Errorf("invalid cypher label: empty after cleanup")
	}
	for _, r := range cleaned {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return "", fmt.Errorf("invalid cypher label %q: contains non-identifier character %q", cleaned, r)
		}
	}
	return cleaned, nil
}

// SanitizeControl strips control characters except \t \n \r.
func SanitizeControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
