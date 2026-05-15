package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

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
		Cypher:   strings.Join(cypherQueries, "\n"),
		Entities: entityMaps,
	}, nil
}

// findRelevantEntities searches for entity names in the graph that match the question.
func (p *Pipeline) findRelevantEntities(question string) []string {
	q := strings.ToLower(question)

	// Get all node names and find matches
	nodes, err := p.store.GetAllNodes()
	if err != nil {
		log.Printf("Warning: failed to get nodes for query: %v", err)
		return nil
	}

	var matches []string
	for _, node := range nodes {
		name, ok := node["col_0"].(string)
		if !ok || name == "" {
			continue
		}
		// Check if the entity name appears in the question
		if strings.Contains(q, strings.ToLower(name)) {
			matches = append(matches, name)
		}
	}

	// If no substring match, try fuzzy: check if any word in the question matches
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
