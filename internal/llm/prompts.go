package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

// DynamicEntityExtractionPrompt generates an entity extraction prompt using the current schema.
// If the schema is empty (first run), it instructs the LLM to freely discover types.
func DynamicEntityExtractionPrompt(typeSummary string) string {
	typeSection := ""
	if strings.Contains(typeSummary, "(no types defined yet") {
		typeSection = `## Type system
You are building the type system from scratch. Assign each entity a concise, lowercase type name that describes what it is.
Good types: person, organization, location, service, product, event, technology, document, role, concept
You may create ANY type that fits the data — there is no fixed list. Be consistent: similar entities should get the same type.`
	} else {
		typeSection = fmt.Sprintf(`## Type system (use existing types when possible, create new ones only when necessary)
Known types in this graph:
%s
You may create new types if none of the above fit, but prefer reusing existing types for consistency.`, typeSummary)
	}

	return fmt.Sprintf(`You are a named entity extractor. Given text (delimited by triple backticks), extract all important named entities with their descriptions and properties.

IMPORTANT: The text may be in any language (Arabic, English, Chinese, mixed, etc.). Extract entity names in their ORIGINAL language. Do not translate.

## Rules
- Extract ONLY named or specific entities: proper nouns, named services, named places, specific roles tied to a person.
- An entity must be something you can point to: a specific person, a specific organization, a specific place, a specific named service.
- DO NOT extract generic words or abstract operational concepts.
- DO NOT extract pronouns, determiners, or status words.
- DO NOT extract raw values: dates, times, phone numbers, IDs. These are properties, not entities.

%s

## Description
For each entity, write a concise 1-sentence description summarizing what it is and its key attributes based on what the text says.

## Properties
Extract factual attributes as simple key-value properties. Use concise lowercase keys (e.g. "status", "phone", "email", "website", "founded", "role", "specialty", "hours").
Only include properties explicitly stated in the text.

## Output format (JSON)
{
  "entities": [
    {
      "name": "entity name in original language",
      "type": "type_name",
      "description": "concise 1-sentence description",
      "properties": {"key": "value"}
    }
  ]
}`, typeSection)
}

// DynamicRelationExtractionPrompt generates a relation extraction prompt using the current schema.
func DynamicRelationExtractionPrompt(relationSummary string) string {
	relSection := ""
	if strings.Contains(relationSummary, "(no relations defined yet") {
		relSection = `## Relations
You are building the relation schema from scratch. Create SHORT, GENERIC relation names in UPPER_SNAKE_CASE (max 2-3 words).

CRITICAL RULES for naming relations:
- Relations must be GENERIC and REUSABLE — the specifics go in the ENTITIES, not the relation name
- WRONG: OFFERS_PEDIATRICS, HANDLES_PRESCRIPTION_FULFILLMENT_FOR, REQUIRES_PHONE_BOOKING
- RIGHT: OFFERS (org → service), HANDLES (org → service), REQUIRES (entity → entity)
- The relation name should work for MANY different entity pairs, not just one specific case
- Maximum 2-3 words. If your relation name has 4+ words, you're encoding entity info in the relation.

Good generic relations: WORKS_AT, LOCATED_IN, OFFERS, PROVIDES, FOUNDED_BY, PART_OF, MANAGES, PARTNERS_WITH, USES, SERVES, REPORTS_TO, HAS_ROLE, CONTRACTED_WITH, DOES_NOT_OFFER, INVOLVED_IN, SPECIALIZES_IN, ALIAS_OF`
	} else {
		relSection = fmt.Sprintf(`## Known relations (you MUST use these when they apply — do NOT create variants):
%s
CRITICAL: Do NOT create new relations that are just longer/more specific versions of existing ones.
- If OFFERS exists, use it for ALL "offers/provides/has service" cases — do NOT create OFFERS_PEDIATRICS, OFFERS_BLOOD_TESTS, etc.
- If LOCATED_IN exists, use it — do NOT create HEADQUARTERED_IN, BASED_IN, etc.
- Only create a genuinely new relation if the semantic meaning is fundamentally different from ALL existing relations.`, relationSummary)
	}

	return fmt.Sprintf(`You are a knowledge graph relation extractor. Given text and a list of known entities, extract factual relationships between them.

IMPORTANT: The text may be in any language. Use entity names exactly as provided in the entity list.

%s

## Direction rules
- Always put the "actor" or "subject" as node_1 and the "object" or "target" as node_2
- For employment: person is node_1, organization is node_2
- For services: organization is node_1, service is node_2
- For containment: container/parent is node_1, contained/child is node_2
- For ownership: owner is node_1, owned is node_2
- Be consistent with direction for the same relation type

## Important
- Extract ONLY facts explicitly stated in the text. Do not infer or guess.
- Extract negative facts too (e.g. "X does not offer Y" → DOES_NOT_OFFER)
- Use entity names EXACTLY as given in the entity list. Do not create new entities.
- Keep relation names SHORT and GENERIC (2-3 words max). Put specifics in the entities.

## Output format (JSON)
{
  "triples": [
    {
      "node_1": "entity name from the list",
      "node_1_type": "type of node_1",
      "node_2": "entity name from the list",
      "node_2_type": "type of node_2",
      "edge": "RELATION_NAME"
    }
  ]
}`, relSection)
}

// EntityStandardizationPrompt asks the LLM to deduplicate entity names.
const EntityStandardizationPrompt = `You are an AI that standardizes entity names. You are given entities grouped by type, with descriptions from the source text. Identify groups that refer to the same real-world entity and map variants to the canonical (preferred) name.

## Merging rules:
1. Person names with/without title prefix → map bare name to titled version
2. Organization short name → full name (canonical = longest, most specific)
3. Abbreviations → full name
4. Service singular/plural and verbose variants → simplest canonical form
5. Spelling/punctuation variants → normalized form
6. Cross-language duplicates → most common form

## IMPORTANT:
- Do NOT merge entities that are genuinely different (e.g. different branches, different people).
- Use the entity descriptions to verify — if two names have similar descriptions about the same real-world thing, they should merge.

## Output format (JSON)
{
  "mappings": {
    "variant name": "canonical name",
    "another variant": "canonical name"
  }
}

Only include entries that need mapping. If a name is already canonical, do not include it.`

// GraphVerificationPrompt asks the LLM to review and correct a knowledge graph.
const GraphVerificationPrompt = `You are a knowledge graph verifier. You are given:
1. A list of EDGES (triples) from a knowledge graph.
2. ENTITY DESCRIPTIONS extracted from the source documents — these contain the ground truth.

Your job: cross-reference edges against entity descriptions and flag errors.

## Checks:
1. Contradictions with negative facts — if a description says "no contract", "not a partner", etc., remove contradicting edges
2. Status conflicts — if something is "planned" or "not yet active", remove edges that imply it's active
3. Semantic accuracy — verify the relation makes sense given entity descriptions
4. Direction errors — source/target should match the relation's semantics

## Output format (JSON)
{
  "remove": [
    {"node_1": "...", "node_2": "...", "edge": "...", "reason": "brief explanation"}
  ],
  "modify": [
    {"node_1": "...", "node_2": "...", "edge": "...", "new_edge": "...", "new_node_1": "...", "new_node_2": "...", "reason": "brief explanation"}
  ]
}

Rules:
- "remove" lists edges to delete entirely.
- "modify" lists edges to change. Include new_edge if relation type changes, new_node_1/new_node_2 if direction flips.
- Only include edges with ACTUAL problems.
- If clean, return {"remove": [], "modify": []}.`

// ExtractEntitiesFromChunk extracts entities with types from a text chunk (Phase 1).
func ExtractEntitiesFromChunk(client *Client, text string, typeSummary string) ([]models.Entity, error) {
	systemPrompt := DynamicEntityExtractionPrompt(typeSummary)
	userPrompt := fmt.Sprintf("```%s```", text)

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("entity extraction failed: %w", err)
	}

	var result struct {
		Entities []struct {
			Name        string                 `json:"name"`
			Type        string                 `json:"type"`
			Description string                 `json:"description"`
			Properties  map[string]interface{} `json:"properties"`
		} `json:"entities"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse entity extraction response: %v\nResponse: %s", err, response)
		return nil, nil
	}

	entities := make([]models.Entity, 0, len(result.Entities))
	for _, e := range result.Entities {
		props := e.Properties
		if props == nil {
			props = map[string]interface{}{}
		}
		if e.Description != "" {
			props["description"] = e.Description
		}
		entities = append(entities, models.Entity{
			Name:       normalizeNodeName(e.Name),
			Type:       strings.ToLower(strings.TrimSpace(e.Type)),
			Properties: props,
		})
	}

	return entities, nil
}

// ExtractRelationsFromChunk extracts relations between known entities (Phase 2).
func ExtractRelationsFromChunk(client *Client, text string, entities []models.Entity, chunkID string, relationSummary string) ([]models.Triple, error) {
	systemPrompt := DynamicRelationExtractionPrompt(relationSummary)

	// Build entity list for the prompt
	entityLines := make([]string, 0, len(entities))
	for _, e := range entities {
		entityLines = append(entityLines, fmt.Sprintf("- %s (%s)", e.Name, e.Type))
	}
	entityList := strings.Join(entityLines, "\n")

	userPrompt := fmt.Sprintf("Known entities:\n%s\n\nText: ```%s```", entityList, text)

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("relation extraction failed: %w", err)
	}

	var result struct {
		Triples []models.Triple `json:"triples"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse relation extraction response for chunk %s: %v", chunkID, err)
		return nil, nil
	}

	// Build entity type map for tagging
	typeMap := map[string]string{}
	for _, e := range entities {
		typeMap[e.Name] = e.Type
	}

	for i := range result.Triples {
		result.Triples[i].Node1 = normalizeNodeName(result.Triples[i].Node1)
		result.Triples[i].Node2 = normalizeNodeName(result.Triples[i].Node2)
		result.Triples[i].Edge = strings.ToUpper(strings.TrimSpace(result.Triples[i].Edge))
		result.Triples[i].ChunkID = chunkID
		// Tag with entity types from Phase 1 (fallback if not set by LLM)
		if result.Triples[i].Node1Type == "" {
			if t, ok := typeMap[result.Triples[i].Node1]; ok {
				result.Triples[i].Node1Type = t
			}
		}
		if result.Triples[i].Node2Type == "" {
			if t, ok := typeMap[result.Triples[i].Node2]; ok {
				result.Triples[i].Node2Type = t
			}
		}
	}

	return result.Triples, nil
}

// StandardizeEntities asks the LLM to find duplicate entity names and return a canonical mapping.
func StandardizeEntities(client *Client, entities []models.Entity) (map[string]string, error) {
	if len(entities) == 0 {
		return nil, nil
	}

	// Group entities by type for easier duplicate detection
	byType := map[string][]models.Entity{}
	for _, e := range entities {
		typ := e.Type
		if typ == "" {
			typ = "unknown"
		}
		byType[typ] = append(byType[typ], e)
	}

	var sb strings.Builder
	for typ, ents := range byType {
		sb.WriteString(fmt.Sprintf("\n## %s entities:\n", typ))
		for _, e := range ents {
			desc, _ := e.Properties["description"].(string)
			if desc != "" {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", e.Name, desc))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n", e.Name))
			}
		}
	}

	userPrompt := sb.String()

	response, err := client.Complete(EntityStandardizationPrompt, userPrompt)
	if err != nil {
		return nil, err
	}

	var result struct {
		Mappings map[string]string `json:"mappings"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse standardization response: %v", err)
		return nil, nil
	}

	return result.Mappings, nil
}

// VerifyGraph asks the LLM to review the full edge list with entity context and return corrections.
func VerifyGraph(client *Client, triples []models.Triple, entities []models.Entity) ([]VerifyRemoval, []VerifyModification, error) {
	if len(triples) == 0 {
		return nil, nil, nil
	}

	// Build entity description section
	descLines := make([]string, 0, len(entities))
	for _, e := range entities {
		desc, _ := e.Properties["description"].(string)
		if desc != "" {
			descLines = append(descLines, fmt.Sprintf("- %s (%s): %s", e.Name, e.Type, desc))
		}
	}

	// Build edge list
	edgeLines := make([]string, 0, len(triples))
	for _, t := range triples {
		edgeLines = append(edgeLines, fmt.Sprintf("- %s (%s) -[%s]-> %s (%s)",
			t.Node1, t.Node1Type, t.Edge, t.Node2, t.Node2Type))
	}

	userPrompt := fmt.Sprintf("## Entity descriptions (ground truth from source documents):\n%s\n\n## Edges to verify (%d total):\n%s",
		strings.Join(descLines, "\n"), len(triples), strings.Join(edgeLines, "\n"))

	response, err := client.Complete(GraphVerificationPrompt, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("graph verification failed: %w", err)
	}

	var result struct {
		Remove []VerifyRemoval      `json:"remove"`
		Modify []VerifyModification `json:"modify"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse verification response: %v", err)
		return nil, nil, nil
	}

	return result.Remove, result.Modify, nil
}

// VerifyRemoval represents an edge the LLM wants to remove.
type VerifyRemoval struct {
	Node1  string `json:"node_1"`
	Node2  string `json:"node_2"`
	Edge   string `json:"edge"`
	Reason string `json:"reason"`
}

// VerifyModification represents an edge the LLM wants to modify.
type VerifyModification struct {
	Node1    string `json:"node_1"`
	Node2    string `json:"node_2"`
	Edge     string `json:"edge"`
	NewEdge  string `json:"new_edge,omitempty"`
	NewNode1 string `json:"new_node_1,omitempty"`
	NewNode2 string `json:"new_node_2,omitempty"`
	Reason   string `json:"reason"`
}

// normalizeNodeName trims whitespace and lowercases Latin characters.
func normalizeNodeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
