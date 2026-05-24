package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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

// nameLookupCues introduce a literal entity name in the question. The tokens
// that follow them are the name the user is asking about.
var nameLookupCues = []string{
	"called ", "named ", "name is ", "name's ",
	"anyone called ", "anybody called ", "any one called ",
	"anyone named ", "anybody named ", "any one named ",
	"who is ", "who's ", "is there a person called ", "is there someone called ",
}

// detectLookupName returns the asked-for name tokens when the question
// explicitly looks up an entity by name ("called X", "named X", "who is X").
// An empty result means no such pattern — fall back to general matching.
//
// This matters because if the user explicitly asks for "sara" and no node is
// named Sara, the right answer is "no, nobody by that name" — not a list of
// phonetically similar names from embedding similarity.
func detectLookupName(q string) []string {
	lower := strings.ToLower(q)
	for _, cue := range nameLookupCues {
		idx := strings.LastIndex(lower, cue)
		if idx < 0 {
			continue
		}
		rest := lower[idx+len(cue):]
		toks := tokenize(rest)
		if len(toks) == 0 {
			continue
		}
		if len(toks) > 3 {
			toks = toks[:3]
		}
		return toks
	}
	return nil
}

// sanitizeContext escapes closing XML tags in retrieved content to prevent
// prompt injection. If ingested text contains "</context>", it could break
// out of the context block and inject instructions into the LLM prompt.
func sanitizeContext(s string) string {
	s = strings.ReplaceAll(s, "</context>", "&lt;/context&gt;")
	s = strings.ReplaceAll(s, "</Context>", "&lt;/Context&gt;")
	s = strings.ReplaceAll(s, "</CONTEXT>", "&lt;/CONTEXT&gt;")
	return s
}

// answerPrompt explicitly asks for JSON because the shared LLM client is
// configured with response_format=json_object, which OpenAI only accepts when
// the prompt itself contains the word "json".
const answerPrompt = `You are a helpful assistant. Answer the user's question based ONLY on the knowledge graph data provided below.

Respond as a single JSON object with one key, "answer", whose value is the human-readable answer string. Example: {"answer": "Yes, Yara Haddad is a branch manager at CedarGate Karmiel Wellness Hub."}

Rules:
- Only use facts from the provided graph data. Do not make up information.
- If the data is empty or doesn't contain the answer, set "answer" to "I don't have enough information in the knowledge graph to answer this."
- The "answer" value is plain text. You may use newlines and "- " for bullet points inside it.
- The text may contain names in any language — preserve them as-is.
- Respond in the same language as the question.`

// Query takes a natural language question, fetches the relevant subgraph,
// and returns the structured result. When withHumanAnswer is true an LLM
// call is made to produce a natural-language Answer; otherwise the LLM is
// skipped and callers (typically agents) get just the focused Graph and
// raw Facts list — one extra LLM round-trip saved.
func (p *Pipeline) Query(question string, withHumanAnswer bool) (*models.QueryResult, error) {
	// Analytical/aggregation questions ("most/least/how many/count of …")
	// can't be answered from a single entity's neighbourhood — they need a
	// graph-wide query. Route them through the LLM-to-Cypher path first;
	// fall back to multi-path retrieval only if it fails.
	if isAnalyticalQuestion(question) {
		if res, err := p.analyticalQuery(question, withHumanAnswer); err == nil {
			return res, nil
		} else {
			log.Printf("Analytical path failed, falling back to multi-path: %v", err)
		}
	}

	// Multi-path retrieval — the GraphRAG-SDK 9-phase pipeline ported to
	// our schema (typed edges + Concept entities). Pulls candidates from
	// vector + Cypher CONTAINS + graph-walk paths, cosine-reranks, and
	// hands the LLM one structured message.
	mp, err := p.runMultiPath(question)
	if err != nil {
		log.Printf("multi-path retrieval failed: %v", err)
		mp = &multiPathResult{} // graceful degradation: empty sections
	}

	// Visualisation subgraph — use the entities multi-path discovered.
	entityNames := make([]string, 0, len(mp.Entities))
	for _, e := range mp.Entities {
		entityNames = append(entityNames, strings.ToLower(strings.TrimSpace(e.Name)))
		if len(entityNames) >= 8 {
			break
		}
	}
	entityMaps := entitiesToMaps(entityNames)
	var subgraph models.SubGraph
	if len(entityNames) > 0 {
		subgraph = p.fetchSubgraph(entityNames)
	}

	// No-entity case → direct "not found" answer (the multi-path retrieval
	// found nothing). Use the lookup-name detector so name-lookup questions
	// get a focused "no" instead of a generic miss message.
	if len(entityNames) == 0 {
		msg := ""
		if withHumanAnswer {
			if lookup := detectLookupName(question); len(lookup) > 0 {
				msg = fmt.Sprintf("No, I couldn't find anyone or anything named %q in the knowledge graph.", strings.Join(lookup, " "))
			} else {
				msg = "I couldn't find any matching entities in the knowledge graph. Try using the exact name of a person, organization, or service."
			}
		}
		return &models.QueryResult{
			Answer:   msg,
			Entities: entityMaps,
		}, nil
	}

	// Agent path: skip the LLM, return the structured payload directly.
	if !withHumanAnswer {
		return &models.QueryResult{
			Graph:    subgraph,
			Facts:    mp.flatFacts(),
			Entities: entityMaps,
		}, nil
	}

	// Have the LLM compose a human answer from the structured sections.
	context := mp.assembledContext()
	if strings.TrimSpace(context) == "" {
		context = "(no supporting evidence)"
	}
	userPrompt := fmt.Sprintf("Question: %s\n\nKnowledge graph context:\n<context>\n%s\n</context>", question, sanitizeContext(context))

	answer, err := p.llmClient.Complete(answerPrompt, userPrompt)
	if err != nil {
		// Don't dump raw triples at the user. Log the real error so it can
		// be diagnosed, and return a polite message; the facts are still
		// available on the structured result for the UI / agents to use.
		log.Printf("Warning: LLM answer generation failed: %v", err)
		return &models.QueryResult{
			Answer:   "I found relevant information in the graph but couldn't generate a summary right now. The supporting facts and subgraph are available below.",
			Graph:    subgraph,
			Facts:    mp.flatFacts(),
			Entities: entityMaps,
		}, nil
	}

	// Strip JSON wrapper if the LLM wraps it.
	var answerObj struct {
		Answer string `json:"answer"`
	}
	if json.Unmarshal([]byte(answer), &answerObj) == nil && answerObj.Answer != "" {
		answer = answerObj.Answer
	}

	return &models.QueryResult{
		Answer:   answer,
		Graph:    subgraph,
		Facts:    mp.flatFacts(),
		Entities: entityMaps,
	}, nil
}

// maxSubgraphNodes caps the focused subgraph so a question that lands on a
// well-connected entity doesn't drag the whole graph into the response. The
// cap applies only to neighborhood nodes; focus nodes are always kept.
const maxSubgraphNodes = 60

// fetchSubgraph builds the *focused* subgraph around the matched entities:
// the matched ("focus") nodes plus their direct (1-hop) neighbors and the
// edges incident to the focus nodes. It deliberately does NOT expand a second
// hop — branches and the parent network are huge hubs, so 2 hops from any
// person/service reaches almost the entire graph and stops being "focused".
//
// When the candidate 1-hop neighborhood is larger than the cap, edges are
// admitted in descending weight order so the strongest relationships survive
// the truncation. Edges between two focus nodes are always kept.
func (p *Pipeline) fetchSubgraph(entities []string) models.SubGraph {
	sg := models.SubGraph{Nodes: []models.GraphNode{}, Edges: []models.GraphEdge{}}

	focusSet := map[string]bool{}
	for _, e := range entities {
		focusSet[strings.TrimSpace(e)] = true
	}

	nodeSeen := map[string]int{}  // node id -> index in sg.Nodes
	edgeSeen := map[string]bool{} // "from|label|to"

	addNode := func(name, group string, force bool) bool {
		name = strings.TrimSpace(name)
		if name == "" {
			return false
		}
		if idx, ok := nodeSeen[name]; ok {
			// "focus" wins over a plain type label.
			if group == "focus" {
				sg.Nodes[idx].Group = "focus"
			} else if sg.Nodes[idx].Group == "" {
				sg.Nodes[idx].Group = group
			}
			return true
		}
		if !force && len(sg.Nodes) >= maxSubgraphNodes {
			return false
		}
		nodeSeen[name] = len(sg.Nodes)
		sg.Nodes = append(sg.Nodes, models.GraphNode{ID: name, Label: name, Group: group})
		return true
	}

	// Admit focus nodes first (always kept; force past the cap).
	for _, entity := range entities {
		addNode(entity, "focus", true)
	}

	// Gather every 1-hop edge incident to each focus entity, in both
	// directions, so we can rank globally before truncating.
	type cand struct {
		from, label, to string
		weight          float64
		otherName       string // neighbor end relative to the focus entity
		otherType       string
	}
	var cands []cand

	for _, entity := range entities {
		escaped := strings.ReplaceAll(strings.ReplaceAll(entity, `\`, `\\`), `'`, `\'`)
		cypher := fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r]-(m) WHERE m.name IS NOT NULL RETURN DISTINCT startNode(r).name, type(r), endNode(r).name, r.weight, m.name, m.type`,
			escaped,
		)
		res, err := p.store.ROQuery(cypher)
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
			c := cand{}
			c.from, _ = cols[0].(string)
			c.label, _ = cols[1].(string)
			c.to, _ = cols[2].(string)
			if w, ok := cols[3].(float64); ok {
				c.weight = w
			} else {
				c.weight = 1.0
			}
			c.otherName, _ = cols[4].(string)
			if len(cols) >= 6 {
				c.otherType, _ = cols[5].(string)
			}
			if c.from == "" || c.to == "" {
				continue
			}
			cands = append(cands, c)
		}
	}

	// Rank: focus-to-focus first (must keep), then by descending weight,
	// then deterministic tie-break on names so the result is stable.
	sort.SliceStable(cands, func(i, j int) bool {
		fi := focusSet[cands[i].from] && focusSet[cands[i].to]
		fj := focusSet[cands[j].from] && focusSet[cands[j].to]
		if fi != fj {
			return fi
		}
		if cands[i].weight != cands[j].weight {
			return cands[i].weight > cands[j].weight
		}
		if cands[i].from != cands[j].from {
			return cands[i].from < cands[j].from
		}
		return cands[i].to < cands[j].to
	})

	for _, c := range cands {
		focusFocus := focusSet[c.from] && focusSet[c.to]
		if !focusFocus {
			if !addNode(c.otherName, c.otherType, false) {
				continue // neighborhood cap reached; drop this lower-weight edge
			}
		}
		key := c.from + "|" + c.label + "|" + c.to
		if edgeSeen[key] {
			continue
		}
		edgeSeen[key] = true
		sg.Edges = append(sg.Edges, models.GraphEdge{From: c.from, To: c.to, Label: c.label, Weight: c.weight})
	}

	return sg
}

// findRelevantEntities searches for entity names in the graph that match the question.
// Uses a two-phase approach: substring matching first, then vector similarity as fallback.
func (p *Pipeline) findRelevantEntities(question string) []string {
	q := strings.ToLower(question)

	// Detect name lookup from the ORIGINAL phrasing. For "called X" / "named
	// X" the user means literally X — we don't want an alias rewrite to
	// silently swap "sara" for an alias target like "samira darwish".
	lookupTokens := detectLookupName(q)

	// Alias rewrite is a helpful expansion for general questions ("what does
	// CGHN do?" -> cedargate health network), but for name lookups it can
	// substitute the user's literal intent — so we skip it in that case.
	if lookupTokens == nil {
		q = p.rewriteQueryWithAliases(q)
	}

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

	// (lookupTokens was computed above from the original, pre-rewrite query.)
	// When set, we only return EXACT name-token matches and refuse to fall
	// through to embedding similarity — "no exact name match" is the correct
	// answer to "is there anyone called sara?", not 5 phonetic neighbors.

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
		// For a name-lookup ("called X"), match only against the asked-for
		// name tokens — not every distinctive word in the question.
		qTokens := map[string]bool{}
		var probe []string
		if lookupTokens != nil {
			probe = lookupTokens
		} else {
			probe = tokenize(q)
		}
		for _, t := range probe {
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

	// Name-lookup short-circuit: the user named a specific entity. If we
	// didn't find it by exact token, return zero so the caller reports
	// "no such entity" instead of guessing with embeddings/fuzzy matches.
	if lookupTokens != nil {
		return dedupAndCap(matches, 5)
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

	return dedupAndCap(matches, 5)
}

// dedupAndCap returns the first `max` distinct values from matches, in order.
func dedupAndCap(matches []string, max int) []string {
	seen := map[string]bool{}
	var unique []string
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			unique = append(unique, m)
		}
		if len(unique) >= max {
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
				// Word-boundary replacement so an alias like "ed" can't
				// eat into "named" / "called", and "sara" never substitutes
				// inside "sarona".
				pat := `(?i)(^|[^a-z0-9])` + regexp.QuoteMeta(alias) + `($|[^a-z0-9])`
				re, rxErr := regexp.Compile(pat)
				if rxErr != nil {
					continue
				}
				rewritten = re.ReplaceAllString(rewritten, "${1}"+canonical+"${2}")
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
