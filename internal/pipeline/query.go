package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"rediskg/pkg/models"
)

// queryStopwords are words ignored when token-matching a question to entity
// names — they carry no entity-selection signal.
var queryStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "any": true,
	"one": true, "who": true, "what": true, "where": true, "when": true,
	"which": true, "does": true, "did": true, "has": true, "have": true,
	"are": true, "was": true, "were": true, "there": true, "anyone": true,
	"called": true, "named": true, "about": true, "from": true, "this": true,
	"that": true, "into": true, "your": true, "you": true, "tell": true,
	"give": true, "show": true, "list": true, "all": true, "name": true,
}

// tokenize lowercases s and splits it into alphanumeric word tokens of length
// >= 3, dropping query stopwords.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	var toks []string
	for _, f := range fields {
		if len(f) < 3 || queryStopwords[f] {
			continue
		}
		toks = append(toks, f)
	}
	return toks
}

const answerPrompt = `You are a helpful assistant. Answer the user's question based ONLY on the knowledge graph data provided below. Be clear and concise.

Rules:
- Only use facts from the provided graph data. Do not make up information.
- If the data is empty or doesn't contain the answer, say "I don't have enough information in the knowledge graph to answer this."
- Format the answer as readable text. Use bullet points for lists.
- The text may contain names in any language — preserve them as-is.
- Respond in the same language as the question.`

// Query takes a natural language question, fetches relevant subgraph, and returns a formatted answer.
func (p *Pipeline) Query(question string) (*models.QueryResult, error) {
	// Step 1: Extract entity names from the question
	entities := p.findRelevantEntities(question)

	entityMaps := entitiesToMaps(entities)

	if len(entities) == 0 {
		return &models.QueryResult{
			Answer:   "I couldn't find any matching entities in the knowledge graph. Try using the exact name of a person, organization, or service.",
			Entities: entityMaps,
		}, nil
	}

	// Build the focused subgraph from the same neighborhood used to answer.
	subgraph := p.fetchSubgraph(entities)

	// Step 2: Fetch subgraph around those entities (1-2 hops)
	var allFacts []string
	var cypherQueries []string

	for _, entity := range entities {
		facts, cypher := p.fetchEntityFacts(entity)
		allFacts = append(allFacts, facts...)
		cypherQueries = append(cypherQueries, cypher...)
	}

	if len(allFacts) == 0 {
		return &models.QueryResult{
			Answer:   fmt.Sprintf("Found entity '%s' but no relationships in the graph.", strings.Join(entities, "', '")),
			Graph:    subgraph,
			Cypher:   strings.Join(cypherQueries, "\n"),
			Entities: entityMaps,
		}, nil
	}

	// Step 3: Have LLM format the answer
	factsText := strings.Join(allFacts, "\n")
	userPrompt := fmt.Sprintf("Question: %s\n\nKnowledge graph facts:\n%s", question, factsText)

	answer, err := p.llmClient.Complete(answerPrompt, userPrompt)
	if err != nil {
		// Fall back to raw facts
		return &models.QueryResult{
			Answer:   factsText,
			Graph:    subgraph,
			Cypher:   strings.Join(cypherQueries, "\n"),
			Entities: entityMaps,
		}, nil
	}

	// Strip JSON wrapper if the LLM wraps it
	var answerObj struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal([]byte(answer), &answerObj) == nil && answerObj.Answer != "" {
		answer = answerObj.Answer
	}

	return &models.QueryResult{
		Answer:   answer,
		Graph:    subgraph,
		Cypher:   strings.Join(cypherQueries, "\n"),
		Entities: entityMaps,
	}, nil
}

// maxSubgraphNodes caps the focused subgraph so a question that lands on a
// well-connected entity doesn't drag the whole graph into the response.
const maxSubgraphNodes = 60

// fetchSubgraph builds the *focused* subgraph around the matched entities:
// the matched ("focus") nodes plus their direct (1-hop) neighbors and the
// edges incident to the focus nodes. It deliberately does NOT expand a second
// hop — branches and the parent network are huge hubs, so 2 hops from any
// person/service reaches almost the entire graph and stops being "focused".
func (p *Pipeline) fetchSubgraph(entities []string) models.SubGraph {
	sg := models.SubGraph{Nodes: []models.GraphNode{}, Edges: []models.GraphEdge{}}

	nodeSeen := map[string]int{}  // node id -> index in sg.Nodes
	edgeSeen := map[string]bool{} // "from|label|to"

	addNode := func(name, typ string, focus bool) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		if idx, ok := nodeSeen[name]; ok {
			if focus {
				sg.Nodes[idx].Focus = true
			}
			if typ != "" && sg.Nodes[idx].Type == "" {
				sg.Nodes[idx].Type = typ
			}
			return true
		}
		// Always admit focus nodes; cap only the neighborhood.
		if !focus && len(sg.Nodes) >= maxSubgraphNodes {
			return false
		}
		nodeSeen[name] = len(sg.Nodes)
		sg.Nodes = append(sg.Nodes, models.GraphNode{ID: name, Label: name, Type: typ, Focus: focus})
		return true
	}

	for _, entity := range entities {
		addNode(entity, "", true)
	}

	for _, entity := range entities {
		escaped := strings.ReplaceAll(strings.ReplaceAll(entity, `\`, `\\`), `'`, `\'`)

		// 1-hop ego network only: the focus entity and its direct relations,
		// in both directions. Every node shown is directly tied to something
		// the user asked about.
		edgeCypher := fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r]-(m) WHERE m.name IS NOT NULL RETURN DISTINCT startNode(r).name, type(r), endNode(r).name, r.weight, m.name, m.type LIMIT %d`,
			escaped, maxSubgraphNodes,
		)
		res, err := p.store.ROQuery(edgeCypher)
		if err != nil {
			continue
		}
		arr, ok := res.([]interface{})
		if !ok || len(arr) < 2 {
			continue
		}
		rows, ok := arr[1].([]interface{})
		if !ok {
			continue
		}
		for _, row := range rows {
			cols, ok := row.([]interface{})
			if !ok || len(cols) < 5 {
				continue
			}
			from, _ := cols[0].(string)
			label, _ := cols[1].(string)
			to, _ := cols[2].(string)
			weight := 1.0
			if w, ok := cols[3].(float64); ok {
				weight = w
			}
			mname, _ := cols[4].(string)
			mtype := ""
			if len(cols) >= 6 {
				mtype, _ = cols[5].(string)
			}
			if !addNode(mname, mtype, false) {
				continue // neighborhood cap reached; skip this neighbor + edge
			}
			if from == "" || to == "" {
				continue
			}
			key := from + "|" + label + "|" + to
			if edgeSeen[key] {
				continue
			}
			edgeSeen[key] = true
			sg.Edges = append(sg.Edges, models.GraphEdge{From: from, To: to, Label: label, Weight: weight})
		}
	}

	return sg
}

// findRelevantEntities searches for entity names in the graph that match the question.
// Uses a two-phase approach: substring matching first, then vector similarity as fallback.
func (p *Pipeline) findRelevantEntities(question string) []string {
	q := strings.ToLower(question)
	q = p.rewriteQueryWithAliases(q)

	// Get all node names
	nodes, err := p.store.GetAllNodes()
	if err != nil {
		log.Printf("Warning: failed to get nodes for query: %v", err)
		return nil
	}

	// Collect node names and a document-frequency of each name token, so we
	// can tell distinctive tokens ("yara") from corpus-common ones
	// ("cedargate", "clinic", "service").
	names := make([]string, 0, len(nodes))
	tokenDF := map[string]int{}
	for _, node := range nodes {
		name, ok := node["col_0"].(string)
		if !ok || name == "" {
			continue
		}
		nl := strings.ToLower(name)
		names = append(names, nl)
		seenTok := map[string]bool{}
		for _, tok := range tokenize(nl) {
			if !seenTok[tok] {
				seenTok[tok] = true
				tokenDF[tok]++
			}
		}
	}

	// Phase 1: Exact substring matching (fast, precise)
	var matches []string
	for _, name := range names {
		if strings.Contains(q, name) {
			matches = append(matches, name)
		}
	}

	// Phase 1b: Distinctive-token matching. A question token that appears in
	// only a few node names (e.g. a person's first name) precisely resolves
	// to those nodes, so "is there anyone called yara?" -> "yara haddad"
	// without falling through to broad embedding/fuzzy fallback.
	if len(matches) == 0 {
		qTokens := map[string]bool{}
		for _, t := range tokenize(q) {
			qTokens[t] = true
		}
		type scored struct {
			name  string
			score int
		}
		var cands []scored
		for _, name := range names {
			best := 0
			for _, tok := range tokenize(name) {
				if !qTokens[tok] {
					continue
				}
				df := tokenDF[tok]
				if df == 0 || df > 5 {
					continue // too common to be a precise selector
				}
				// Rarer token => stronger signal.
				s := 6 - df
				if s > best {
					best = s
				}
			}
			if best > 0 {
				cands = append(cands, scored{name, best})
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
		for _, c := range cands {
			matches = append(matches, c.name)
		}
	}

	// Phase 2: If no substring matches, use embedding similarity
	if len(matches) == 0 {
		embedding, err := p.llmClient.Embed(question)
		if err == nil {
			similar, err := p.store.FindSimilarEntities("Concept", "embedding", embedding, 5)
			if err == nil && len(similar) > 0 {
				matches = similar
				log.Printf("Query: found %d entities via embedding similarity", len(matches))
			}
		}
		if err != nil {
			log.Printf("Warning: embedding search failed, falling back to word match: %v", err)
		}
	}

	// Phase 3: If still nothing, try fuzzy word matching
	if len(matches) == 0 {
		words := strings.Fields(q)
		for _, node := range nodes {
			name, ok := node["col_0"].(string)
			if !ok || name == "" || len(name) < 3 {
				continue
			}
			nameLower := strings.ToLower(name)
			for _, word := range words {
				if len(word) >= 3 && strings.Contains(nameLower, word) {
					matches = append(matches, name)
					break
				}
			}
		}
	}

	// Deduplicate and limit
	seen := map[string]bool{}
	var unique []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
		if len(unique) >= 5 {
			break
		}
	}

	return unique
}

func (p *Pipeline) rewriteQueryWithAliases(question string) string {
	cypher := `MATCH (a:Concept)-[r:ALIAS_OF]->(c:Concept) RETURN a.name, c.name LIMIT 500`
	res, err := p.store.ROQuery(cypher)
	if err != nil {
		return question
	}
	rewritten := question
	if arr, ok := res.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				cols, ok := row.([]interface{})
				if !ok || len(cols) < 2 {
					continue
				}
				alias, _ := cols[0].(string)
				canonical, _ := cols[1].(string)
				alias = strings.ToLower(strings.TrimSpace(alias))
				canonical = strings.ToLower(strings.TrimSpace(canonical))
				if alias == "" || canonical == "" {
					continue
				}
				if strings.Contains(rewritten, alias) {
					rewritten = strings.ReplaceAll(rewritten, alias, canonical)
				}
			}
		}
	}
	return rewritten
}

// fetchEntityFacts runs deterministic Cypher queries to get all facts about an entity.
func (p *Pipeline) fetchEntityFacts(entity string) ([]string, []string) {
	escaped := strings.ReplaceAll(strings.ReplaceAll(entity, `\`, `\\`), `'`, `\'`)
	var facts []string
	var queries []string

	// 1-hop: direct relationships
	cypher1 := fmt.Sprintf(
		`MATCH (a:Concept {name: '%s'})-[r]->(b:Concept) RETURN a.name, type(r), r.description, b.name, b.type LIMIT 50`,
		escaped,
	)
	queries = append(queries, cypher1)
	if result, err := p.store.ROQuery(cypher1); err == nil {
		facts = append(facts, parseFactsOutgoing(result)...)
	}

	// 1-hop: incoming relationships
	cypher2 := fmt.Sprintf(
		`MATCH (a:Concept)-[r]->(b:Concept {name: '%s'}) RETURN a.name, a.type, type(r), r.description, b.name LIMIT 50`,
		escaped,
	)
	queries = append(queries, cypher2)
	if result, err := p.store.ROQuery(cypher2); err == nil {
		facts = append(facts, parseFactsIncoming(result)...)
	}

	// 2-hop: one more hop out
	cypher3 := fmt.Sprintf(
		`MATCH (a:Concept {name: '%s'})-[r1]->(b:Concept)-[r2]->(c:Concept) WHERE a <> c RETURN a.name, type(r1), b.name, type(r2), c.name LIMIT 30`,
		escaped,
	)
	queries = append(queries, cypher3)
	if result, err := p.store.ROQuery(cypher3); err == nil {
		facts = append(facts, parseFacts2Hop(result)...)
	}

	return facts, queries
}

func parseFactsOutgoing(result interface{}) []string {
	var facts []string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 4 {
					from, _ := cols[0].(string)
					relType, _ := cols[1].(string)
					desc, _ := cols[2].(string)
					to, _ := cols[3].(string)
					toType := ""
					if len(cols) >= 5 {
						toType, _ = cols[4].(string)
					}
					fact := fmt.Sprintf("%s -[%s]-> %s", from, relType, to)
					if desc != "" && desc != relType {
						fact += fmt.Sprintf(" (description: %s)", desc)
					}
					if toType != "" {
						fact += fmt.Sprintf(" [%s]", toType)
					}
					facts = append(facts, fact)
				}
			}
		}
	}
	return facts
}

func parseFactsIncoming(result interface{}) []string {
	var facts []string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 5 {
					from, _ := cols[0].(string)
					fromType, _ := cols[1].(string)
					relType, _ := cols[2].(string)
					desc, _ := cols[3].(string)
					to, _ := cols[4].(string)
					fact := fmt.Sprintf("%s -[%s]-> %s", from, relType, to)
					if desc != "" && desc != relType {
						fact += fmt.Sprintf(" (description: %s)", desc)
					}
					if fromType != "" {
						fact += fmt.Sprintf(" [%s]", fromType)
					}
					facts = append(facts, fact)
				}
			}
		}
	}
	return facts
}

func parseFacts2Hop(result interface{}) []string {
	var facts []string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 5 {
					a, _ := cols[0].(string)
					r1, _ := cols[1].(string)
					b, _ := cols[2].(string)
					r2, _ := cols[3].(string)
					c, _ := cols[4].(string)
					facts = append(facts, fmt.Sprintf("%s -[%s]-> %s -[%s]-> %s", a, r1, b, r2, c))
				}
			}
		}
	}
	return facts
}

func entitiesToMaps(entities []string) []map[string]interface{} {
	result := make([]map[string]interface{}, len(entities))
	for i, e := range entities {
		result[i] = map[string]interface{}{"name": e}
	}
	return result
}

// QueryCypher executes a raw Cypher query directly.
func (p *Pipeline) QueryCypher(cypher string) (interface{}, error) {
	return p.store.ROQuery(cypher)
}

// GetStats returns graph statistics.
func (p *Pipeline) GetStats() (map[string]int64, error) {
	return p.store.GetGraphStats()
}
