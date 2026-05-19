package pipeline

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Stopwords + short words dropped; "yara" survives as the selector.
		{"is there any one called yara ?", []string{"yara"}},
		{"who is yara haddad", []string{"yara", "haddad"}},
		{"al-amal laboratory", []string{"amal", "laboratory"}},
		{"the and for", nil},
	}
	for _, c := range cases {
		got := tokenize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
