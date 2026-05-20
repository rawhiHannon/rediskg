package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// analyticalCues are phrases that signal the question wants an aggregation,
// ranking, or count — not facts about a specific named entity. Detecting one
// of these routes the question through the LLM-to-Cypher path instead of the
// entity-centric one (which can only see one entity's neighborhood).
var analyticalCues = []string{
	"how many", "count of", "number of", "no. of",
	"most ", "least ", "highest", "lowest", "biggest", "largest", "smallest",
	"top ", " best ", " worst ", "fewest", "greatest",
	"average", "median", "total ",
	"which has", "which one has", "rank", "ranking", "ordered by",
}

func isAnalyticalQuestion(q string) bool {
	lower := " " + strings.ToLower(q) + " "
	for _, c := range analyticalCues {
		if strings.Contains(lower, c) {
			return true
		}
	}
	return false
}

const cypherSystemPromptTemplate = `You translate a natural-language question into ONE FalkorDB Cypher query (a JSON response is required).

GRAPH SCHEMA
- Every node has label :Concept plus a typed label such as :Person, :Organization, :Service, :Location, :Role, :Event, :Technology.
- Node properties:
  - name (string, ALREADY LOWERCASE) — primary identifier
  - type (string) — base type ("person", "organization", "service", "location", "role", "event", "technology", "concept", "address")
  - status (string, optional) — "active", "planned", "historical", "former", "prospective", "unknown"
  - domain_type (string, optional) — fine-grained subtype, e.g. "branch_clinic", "pharmacy"
  - functional_roles (string, optional) — comma-separated, e.g. "branch,operated_unit" or "staff_member"
  - aliases (string, optional) — pipe-separated
- Edges have the relation type as the relationship type (HAS_BRANCH, MANAGES, BASED_AT, OFFERS, …) and properties: weight, status, condition, evidence.

ALLOWED RELATION TYPES (use only these):
%s

RULES
- Return exactly ONE read-only Cypher query. NO CREATE/MERGE/DELETE/SET/REMOVE.
- Names in the graph are stored lowercase. When matching by name use toLower(n.name) = '...' or pass lowercase literals.
- For "most" / "least" / ranking, use count(DISTINCT ...) with ORDER BY DESC/ASC and LIMIT 10.
- Always put the relevant entity name(s) as the FIRST column so they can be visualised as graph nodes.
- Respond as a JSON object with a single key "cypher": {"cypher": "..."}. No commentary, no markdown fences.

EXAMPLES
Q: which branch has the most staff members?
{"cypher":"MATCH (p:Concept)-[:BASED_AT|WORKS_AT|MANAGES]->(b:Concept) WHERE p.functional_roles CONTAINS 'staff_member' RETURN b.name AS branch, count(DISTINCT p) AS staff ORDER BY staff DESC LIMIT 10"}

Q: how many active branches does cedargate health network operate?
{"cypher":"MATCH (n:Concept {name:'cedargate health network'})-[:HAS_BRANCH]->(b:Concept) RETURN n.name, count(DISTINCT b) AS active_branches"}

Q: which clinic offers the most services?
{"cypher":"MATCH (c:Concept)-[:OFFERS]->(s:Concept) WHERE c.type = 'organization' RETURN c.name AS clinic, count(DISTINCT s) AS services ORDER BY services DESC LIMIT 10"}
`

var (
	reCypherFenced = regexp.MustCompile("(?s)```(?:cypher|json)?\\s*(.*?)```")
	reCypherJSON   = regexp.MustCompile(`(?s)\{[^{}]*"cypher"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reCypherMatch  = regexp.MustCompile(`(?is)\b(MATCH|CALL)\s+.+`)
)

// extractCypher pulls a Cypher query out of the LLM response, trying JSON
// first (the expected format), then a fenced code block, then a raw MATCH.
func extractCypher(raw string) string {
	if m := reCypherJSON.FindStringSubmatch(raw); len(m) >= 2 {
		s, err := unquoteJSONString(m[1])
		if err == nil {
			return strings.TrimSpace(s)
		}
	}
	if m := reCypherFenced.FindStringSubmatch(raw); len(m) >= 2 {
		return strings.TrimSpace(m[1])
	}
	if m := reCypherMatch.FindString(raw); m != "" {
		return strings.TrimSpace(m)
	}
	return ""
}

func unquoteJSONString(s string) (string, error) {
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err != nil {
		return "", err
	}
	return out, nil
}

var writeKeywords = []string{"create ", "merge ", "delete ", " set ", "remove ", "drop ", "detach "}

func isReadOnlyCypher(cypher string) bool {
	lower := " " + strings.ToLower(cypher) + " "
	for _, kw := range writeKeywords {
		if strings.Contains(lower, kw) {
			return false
		}
	}
	return true
}

// analyticalQuery handles aggregation/ranking/count questions by asking the
// LLM to write Cypher, running it, and summarising the rows. On any failure
// it returns (nil, err) so the caller can fall back to the entity-centric
// flow without leaving the user with a confusing message.
func (p *Pipeline) analyticalQuery(question string, withHumanAnswer bool) (*models.QueryResult, error) {
	sys := fmt.Sprintf(cypherSystemPromptTemplate, schema.FormatRelationsForPrompt())
	raw, err := p.llmClient.Complete(sys, "Question: "+question)
	if err != nil {
		log.Printf("Warning: Cypher generation failed: %v", err)
		return nil, err
	}
	cypher := extractCypher(raw)
	if cypher == "" {
		return nil, fmt.Errorf("LLM did not return a usable cypher query")
	}
	if !isReadOnlyCypher(cypher) {
		return nil, fmt.Errorf("refusing to execute non-read-only cypher: %s", cypher)
	}

	res, err := p.store.ROQuery(cypher)
	if err != nil {
		log.Printf("Warning: generated cypher failed: %v\n  query: %s", err, cypher)
		return nil, err
	}

	header, rows := parseCypherRows(res)
	if len(rows) == 0 {
		msg := ""
		if withHumanAnswer {
			msg = "I ran a query for that question and got no matching rows."
		}
		return &models.QueryResult{
			Answer: msg,
			Cypher: cypher,
		}, nil
	}

	factsText := formatRowsAsText(header, rows)
	focusEntities := p.collectExistingNodeNames(rows)
	subgraph := p.fetchSubgraph(focusEntities)

	if !withHumanAnswer {
		return &models.QueryResult{
			Graph:  subgraph,
			Facts:  []string{factsText},
			Cypher: cypher,
		}, nil
	}

	user := fmt.Sprintf("Question: %s\n\nQuery results:\n%s", question, factsText)
	answer, err := p.llmClient.Complete(answerPrompt, user)
	if err != nil {
		log.Printf("Warning: analytical answer formatting failed: %v", err)
		return &models.QueryResult{
			Answer: "I ran a query for that question but couldn't generate a summary right now. The result rows are available below.",
			Graph:  subgraph,
			Facts:  []string{factsText},
			Cypher: cypher,
		}, nil
	}
	var ansObj struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal([]byte(answer), &ansObj) == nil && ansObj.Answer != "" {
		answer = ansObj.Answer
	}
	return &models.QueryResult{
		Answer: answer,
		Graph:  subgraph,
		Facts:  []string{factsText},
		Cypher: cypher,
	}, nil
}

// parseCypherRows turns a FalkorDB result into a header + row slice. The first
// element of the raw result is the column-name header, the second is the row
// data, and any later elements are stats we ignore.
func parseCypherRows(res interface{}) ([]string, [][]interface{}) {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil, nil
	}
	var header []string
	if hdr, ok := arr[0].([]interface{}); ok {
		for _, h := range hdr {
			if s, ok := h.(string); ok {
				header = append(header, s)
				continue
			}
			// FalkorDB sometimes returns header cells as [type, name] pairs.
			if pair, ok := h.([]interface{}); ok && len(pair) >= 2 {
				if s, ok := pair[1].(string); ok {
					header = append(header, s)
					continue
				}
			}
			header = append(header, "")
		}
	}
	rowsRaw, ok := arr[1].([]interface{})
	if !ok {
		return header, nil
	}
	var rows [][]interface{}
	for _, r := range rowsRaw {
		cols, ok := r.([]interface{})
		if !ok {
			continue
		}
		rows = append(rows, cols)
	}
	return header, rows
}

func cellToString(c interface{}) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return fmt.Sprintf("%d", v)
	case float64:
		// Show ints cleanly.
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func formatRowsAsText(header []string, rows [][]interface{}) string {
	var b strings.Builder
	if len(header) > 0 {
		b.WriteString(strings.Join(header, " | "))
		b.WriteString("\n")
	}
	for _, row := range rows {
		parts := make([]string, len(row))
		for i, c := range row {
			parts[i] = cellToString(c)
		}
		b.WriteString(strings.Join(parts, " | "))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// collectExistingNodeNames returns the unique string cell values that are
// also names of real nodes in the graph. Used to build a focused subgraph
// from analytical result rows.
func (p *Pipeline) collectExistingNodeNames(rows [][]interface{}) []string {
	candidates := map[string]bool{}
	for _, row := range rows {
		for _, c := range row {
			s, ok := c.(string)
			if !ok {
				continue
			}
			s = strings.ToLower(strings.TrimSpace(s))
			if s == "" || len(s) < 2 {
				continue
			}
			candidates[s] = true
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	allNodes, err := p.store.GetAllNodes()
	if err != nil {
		return nil
	}
	existing := map[string]bool{}
	for _, n := range allNodes {
		if name, ok := n["col_0"].(string); ok {
			existing[strings.ToLower(name)] = true
		}
	}
	var out []string
	for c := range candidates {
		if existing[c] {
			out = append(out, c)
		}
	}
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}
