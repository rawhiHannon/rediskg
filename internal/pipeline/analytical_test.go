package pipeline

import "testing"

func TestIsAnalyticalQuestion(t *testing.T) {
	cases := map[string]bool{
		"what clinic have the most workers?":       true,
		"how many active branches does cg have?":   true,
		"top 5 services by usage":                  true,
		"which has the highest weight?":            true,
		"who is yara":                              false,
		"is there a worker called sara?":           false,
		"what services does cedargate haifa offer": false,
	}
	for q, want := range cases {
		if got := isAnalyticalQuestion(q); got != want {
			t.Errorf("isAnalyticalQuestion(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestExtractCypher(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			`{"cypher":"MATCH (n) RETURN n LIMIT 1"}`,
			"MATCH (n) RETURN n LIMIT 1",
		},
		{
			"```cypher\nMATCH (n) RETURN n\n```",
			"MATCH (n) RETURN n",
		},
		{
			"Here you go: MATCH (a)-[r]->(b) RETURN a, b LIMIT 5",
			"MATCH (a)-[r]->(b) RETURN a, b LIMIT 5",
		},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractCypher(c.in); got != c.want {
			t.Errorf("extractCypher(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsReadOnlyCypher(t *testing.T) {
	cases := map[string]bool{
		"MATCH (n) RETURN n":                  true,
		"MATCH (n)-[r]->(m) RETURN n,r,m":     true,
		"CREATE (n:X {name:'foo'})":           false,
		"MATCH (n) DELETE n":                  false,
		"MATCH (n) SET n.x = 1":               false,
		"MERGE (n:X {name:'foo'})":            false,
		"DROP INDEX foo":                      false,
	}
	for c, want := range cases {
		if got := isReadOnlyCypher(c); got != want {
			t.Errorf("isReadOnlyCypher(%q) = %v, want %v", c, got, want)
		}
	}
}
