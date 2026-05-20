package pipeline

import (
	"reflect"
	"testing"
)

func TestDetectLookupName(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"is there any worker called sara ?", []string{"sara"}},
		{"is there anyone called yara haddad", []string{"yara", "haddad"}},
		{"who is yara", []string{"yara"}},
		{"named al-amal laboratory", []string{"amal", "laboratory"}},
		// No lookup pattern → empty.
		{"what services does cedargate offer", nil},
		{"list all branches", nil},
	}
	for _, c := range cases {
		got := detectLookupName(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("detectLookupName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

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
