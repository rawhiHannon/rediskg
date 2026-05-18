package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

// FormatBaseTypeList formats a base type summary for use in extraction prompts.
// If baseTypeSummary is empty, falls back to a minimal default.
func FormatBaseTypeList(baseTypeSummary string) string {
	if baseTypeSummary == "" {
		return `Base types (always use one of these as the primary category):
- person: Named individual
- organization: Company, institution, agency, network, or group
- location: Geographic place
- address: Street address or postal location
- physical_object: Tangible object, device, tool, or material
- product: Something manufactured, sold, or traded
- service: Something offered, provided, or performed
- event: Named incident, meeting, or occurrence
- technology: Software, system, platform, or tool
- document: Named document, policy, or agreement
- role: Job title or function
- quantity: Measurable amount or metric
- date_time: Date, time, or temporal reference
- law_or_policy: Regulation, law, or official policy
- concept: Abstract topic or category (fallback)`
	}
	return "Base types (always use one of these as the primary category):\n" + baseTypeSummary
}

// DynamicEntityExtractionPrompt generates an entity extraction prompt.
// Entities are extracted with evidence text and candidate types.
// baseTypeSummary should come from schema.BaseTypeSummary().
func DynamicEntityExtractionPrompt(domainTypeSummary string, baseTypeSummary ...string) string {
	baseTypeSummaryStr := ""
	if len(baseTypeSummary) > 0 {
		baseTypeSummaryStr = baseTypeSummary[0]
	}

	domainSection := ""
	if domainTypeSummary != "" {
		domainSection = fmt.Sprintf(`
## Domain-specific types already discovered in this graph:
%s
You may use these domain types OR propose new ones. Every domain type must have a parent base type.`, domainTypeSummary)
	}

	return fmt.Sprintf(`You are a named entity extractor. Given text (delimited by triple backticks), extract all important named entities.

IMPORTANT: The text may be in any language. Extract entity names in their ORIGINAL language. Do not translate.

## Rules
- Extract ONLY named or specific entities: proper nouns, named services, named places, specific roles tied to a person.
- DO NOT extract generic words, abstract concepts, pronouns, determiners, or status words.
- DO NOT extract raw values (dates, times, phone numbers). These are properties, not entities.

## Type system

%s
%s
For each entity, provide:
- base_type: one of the base types above
- domain_type: a more specific subtype if applicable (e.g. "branch_office", "warehouse", "retail_store" under organization)
  If no specific domain type applies, leave domain_type empty.

## Evidence
For each entity, copy the EXACT sentence(s) from the text that mention it. This is critical for later verification.

## Output format (JSON)
{
  "entities": [
    {
      "name": "entity name in original language",
      "base_type": "one of the base types",
      "domain_type": "optional specific type",
      "description": "concise 1-sentence description",
      "evidence": "exact sentence from text mentioning this entity",
      "properties": {"key": "value"}
    }
  ]
}`, FormatBaseTypeList(baseTypeSummaryStr), domainSection)
}

// DynamicRelationExtractionPrompt generates a relation extraction prompt.
func DynamicRelationExtractionPrompt(knownRelations string) string {
	relSection := ""
	if knownRelations == "" || strings.Contains(knownRelations, "(no relations defined yet") {
		relSection = `## Relations
Create SHORT, GENERIC relation names in UPPER_SNAKE_CASE (max 2-3 words).

CRITICAL RULES:
- Relations must be GENERIC and REUSABLE — specifics go in the ENTITIES, not the relation name
- WRONG: OFFERS_PEDIATRICS, HANDLES_PRESCRIPTION_FULFILLMENT_FOR
- RIGHT: OFFERS (org → service), HANDLES (org → service), MANAGES (person → org)
- Maximum 2-3 words. If 4+ words, you're encoding entity info in the relation.

Good examples: WORKS_AT, LOCATED_IN, OFFERS, FOUNDED_BY, PART_OF, MANAGES, PARTNERS_WITH, CONTRACTED_WITH, DOES_NOT_OFFER, INVOLVED_IN, SPECIALIZES_IN, ALIAS_OF, REPORTS_TO, HAS_ROLE, USES`
	} else {
		relSection = fmt.Sprintf(`## Known relations (you MUST reuse these when they apply):
%s
CRITICAL: Do NOT create variants of existing relations.
Only create a genuinely new relation if the meaning is fundamentally different from ALL existing ones.`, knownRelations)
	}

	return fmt.Sprintf(`You are a knowledge graph relation extractor. Given text and a list of entities, extract factual relationships.

IMPORTANT: The text may be in any language. Use entity names EXACTLY as provided.

%s

## Direction rules
- Actor/subject is node_1, object/target is node_2
- Employment: person → organization
- Services: organization → service
- Containment: parent → child
- Be consistent with direction for the same relation type

## Evidence
For each triple, copy the EXACT sentence from the text that states this fact. This is critical.

## Important
- Extract ONLY facts explicitly stated in the text. Do not infer.
- Extract negative facts too (e.g. "does not offer X" → DOES_NOT_OFFER)
- Use entity names EXACTLY as given. Do not create new entities.

## Output format (JSON)
{
  "triples": [
    {
      "node_1": "entity name",
      "node_1_type": "base_type of node_1",
      "node_2": "entity name",
      "node_2_type": "base_type of node_2",
      "edge": "RELATION_NAME",
      "evidence": "exact sentence from text supporting this fact"
    }
  ]
}`, relSection)
}

// EntityResolutionPrompt asks the LLM to resolve entity types using global context.
const EntityResolutionPrompt = `You are an entity type resolver. Given an entity with ALL its mentions across a document, determine the correct base_type and domain_type.

## Base types (pick exactly one):
- person, organization, location, address, service, event, technology, document, role, concept

## Rules
- base_type: always one of the 10 base types above
- domain_type: a more specific label UNDER the base type (e.g. "branch_office" under organization, "warehouse" under organization, "consulting_service" under service)
- If the entity is clearly just a base type with no useful specialization, leave domain_type empty
- Use ALL the evidence to decide — a single mention may be misleading
- If candidate_types disagree, use the evidence to pick the correct one

## Output format (JSON)
{
  "entities": [
    {
      "name": "entity name",
      "base_type": "base type",
      "domain_type": "optional domain type",
      "confidence": 0.95
    }
  ]
}`

// RelationInductionPrompt asks the LLM to cluster and normalize relation names.
const RelationInductionPrompt = `You are a relation schema normalizer. Given a list of candidate relation names extracted from documents, group synonyms and define canonical relations.

## Rules
- Group relations that express the SAME meaning into one canonical relation
- Choose the clearest, most generic name as canonical (max 2-3 words)
- Define direction: which base types can be source vs target
- Mark symmetric relations (where direction doesn't matter, e.g. PARTNERS_WITH)
- Relations that are too vague to be useful (like RELATED_TO, HANDLES without context) should be marked as "reject"
- Inverse relations should be collapsed (e.g. MANAGES and MANAGED_BY → one canonical with defined direction)

## Output format (JSON)
{
  "canonical_relations": [
    {
      "name": "CANONICAL_NAME",
      "description": "what this relation means",
      "aliases": ["ALIAS1", "ALIAS2"],
      "source_base_types": ["person", "organization"],
      "target_base_types": ["organization"],
      "symmetric": false
    }
  ],
  "rejected": ["VAGUE_RELATION_1", "NOISE_RELATION_2"]
}`

// RichVerificationPrompt validates triples with full entity profiles and evidence.
const RichVerificationPrompt = `You are a knowledge graph verifier. You receive triples with:
- Entity profiles (all known facts about each entity)
- Evidence text (the exact source sentence)
- Relation schema (what each relation means and its constraints)

## Your tasks:
1. Cross-reference each triple's evidence with entity profiles — does the evidence support the claim?
2. Check direction: does the source/target match the relation's constraints?
3. Check for contradictions: does any entity profile contradict this triple?
4. Check for redundancy: is this triple already implied by another triple?

## Output format (JSON)
{
  "accept": [
    {"node_1": "...", "edge": "...", "node_2": "...", "reason": "evidence supports"}
  ],
  "reject": [
    {"node_1": "...", "edge": "...", "node_2": "...", "reason": "contradicts entity profile"}
  ],
  "modify": [
    {"node_1": "...", "edge": "...", "node_2": "...", "new_edge": "...", "new_node_1": "...", "new_node_2": "...", "reason": "direction wrong"}
  ]
}

Only reject triples with ACTUAL problems. If a triple is correct, accept it.`

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
- Do NOT merge genuinely different entities
- Use descriptions to verify — similar descriptions about the same thing = merge

## Output format (JSON)
{
  "mappings": {
    "variant name": "canonical name"
  }
}

Only include entries that need mapping.`

// ExtractEntitiesFromChunk extracts entities from a text chunk with evidence.
func ExtractEntitiesFromChunk(client *Client, text string, domainTypeSummary string, baseTypeSummary ...string) ([]models.Entity, error) {
	systemPrompt := DynamicEntityExtractionPrompt(domainTypeSummary, baseTypeSummary...)
	userPrompt := fmt.Sprintf("```%s```", text)

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("entity extraction failed: %w", err)
	}

	var result struct {
		Entities []struct {
			Name        string                 `json:"name"`
			BaseType    string                 `json:"base_type"`
			DomainType  string                 `json:"domain_type"`
			Description string                 `json:"description"`
			Evidence    string                 `json:"evidence"`
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
		if e.Evidence != "" {
			props["evidence"] = e.Evidence
		}

		baseType := strings.ToLower(strings.TrimSpace(e.BaseType))
		domainType := strings.ToLower(strings.TrimSpace(e.DomainType))

		// Use domain_type as the display type if present, otherwise base_type
		displayType := baseType
		if domainType != "" {
			displayType = domainType
		}

		entities = append(entities, models.Entity{
			Name:       normalizeNodeName(e.Name),
			Type:       displayType,
			BaseType:   baseType,
			DomainType: domainType,
			Properties: props,
		})
	}

	return entities, nil
}

// ExtractRelationsFromChunk extracts relations with evidence.
func ExtractRelationsFromChunk(client *Client, text string, entities []models.Entity, chunkID string, knownRelations string) ([]models.Triple, error) {
	systemPrompt := DynamicRelationExtractionPrompt(knownRelations)

	entityLines := make([]string, 0, len(entities))
	for _, e := range entities {
		typeStr := e.BaseType
		if e.DomainType != "" {
			typeStr = e.DomainType + " (" + e.BaseType + ")"
		}
		if typeStr == "" {
			typeStr = e.Type
		}
		entityLines = append(entityLines, fmt.Sprintf("- %s [%s]", e.Name, typeStr))
	}
	entityList := strings.Join(entityLines, "\n")

	userPrompt := fmt.Sprintf("Known entities:\n%s\n\nText: ```%s```", entityList, text)

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("relation extraction failed: %w", err)
	}

	var result struct {
		Triples []struct {
			Node1     string `json:"node_1"`
			Node1Type string `json:"node_1_type"`
			Node2     string `json:"node_2"`
			Node2Type string `json:"node_2_type"`
			Edge      string `json:"edge"`
			Evidence  string `json:"evidence"`
		} `json:"triples"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse relation extraction response for chunk %s: %v", chunkID, err)
		return nil, nil
	}

	// Build entity type map
	typeMap := map[string]string{}
	for _, e := range entities {
		typeMap[e.Name] = e.BaseType
	}

	triples := make([]models.Triple, 0, len(result.Triples))
	for _, t := range result.Triples {
		triple := models.Triple{
			Node1:     normalizeNodeName(t.Node1),
			Node2:     normalizeNodeName(t.Node2),
			Edge:      strings.ToUpper(strings.TrimSpace(t.Edge)),
			Evidence:  t.Evidence,
			ChunkID:   chunkID,
			Node1Type: strings.ToLower(strings.TrimSpace(t.Node1Type)),
			Node2Type: strings.ToLower(strings.TrimSpace(t.Node2Type)),
		}
		// Fill from entity map if LLM didn't provide
		if triple.Node1Type == "" {
			triple.Node1Type = typeMap[triple.Node1]
		}
		if triple.Node2Type == "" {
			triple.Node2Type = typeMap[triple.Node2]
		}
		triples = append(triples, triple)
	}

	return triples, nil
}

// ResolveEntityTypes asks the LLM to determine base_type and domain_type using global evidence.
func ResolveEntityTypes(client *Client, profiles []models.EntityProfile) ([]models.EntityProfile, error) {
	if len(profiles) == 0 {
		return nil, nil
	}

	// Build input
	var sb strings.Builder
	for _, p := range profiles {
		sb.WriteString(fmt.Sprintf("### %s\n", p.Name))
		if len(p.CandidateTypes) > 0 {
			sb.WriteString(fmt.Sprintf("Candidate types: %s\n", strings.Join(p.CandidateTypes, ", ")))
		}
		if p.Description != "" {
			sb.WriteString(fmt.Sprintf("Description: %s\n", p.Description))
		}
		if len(p.Mentions) > 0 {
			sb.WriteString("Mentions:\n")
			limit := 5
			if len(p.Mentions) < limit {
				limit = len(p.Mentions)
			}
			for _, m := range p.Mentions[:limit] {
				sb.WriteString(fmt.Sprintf("  - %s\n", m))
			}
		}
		sb.WriteString("\n")
	}

	response, err := client.Complete(EntityResolutionPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("entity resolution failed: %w", err)
	}

	var result struct {
		Entities []struct {
			Name       string  `json:"name"`
			BaseType   string  `json:"base_type"`
			DomainType string  `json:"domain_type"`
			Confidence float64 `json:"confidence"`
		} `json:"entities"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse entity resolution response: %v", err)
		return nil, nil
	}

	// Map results back to profiles
	resolved := map[string]struct {
		base, domain string
		conf         float64
	}{}
	for _, e := range result.Entities {
		resolved[strings.ToLower(e.Name)] = struct {
			base, domain string
			conf         float64
		}{strings.ToLower(e.BaseType), strings.ToLower(e.DomainType), e.Confidence}
	}

	for i := range profiles {
		if r, ok := resolved[strings.ToLower(profiles[i].Name)]; ok {
			profiles[i].BaseType = r.base
			profiles[i].DomainType = r.domain
			profiles[i].Confidence = r.conf
		}
	}

	return profiles, nil
}

// InduceRelationSchema asks the LLM to cluster and normalize relation names.
func InduceRelationSchema(client *Client, candidateRelations []string) ([]models.CandidateRelation, []string, error) {
	if len(candidateRelations) == 0 {
		return nil, nil, nil
	}

	userPrompt := fmt.Sprintf("Candidate relations to normalize:\n%s", strings.Join(candidateRelations, "\n"))

	response, err := client.Complete(RelationInductionPrompt, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("relation induction failed: %w", err)
	}

	var result struct {
		CanonicalRelations []struct {
			Name            string   `json:"name"`
			Description     string   `json:"description"`
			Aliases         []string `json:"aliases"`
			SourceBaseTypes []string `json:"source_base_types"`
			TargetBaseTypes []string `json:"target_base_types"`
			Symmetric       bool     `json:"symmetric"`
		} `json:"canonical_relations"`
		Rejected []string `json:"rejected"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse relation induction response: %v", err)
		return nil, nil, nil
	}

	canonical := make([]models.CandidateRelation, 0, len(result.CanonicalRelations))
	for _, cr := range result.CanonicalRelations {
		dir := "forward"
		if cr.Symmetric {
			dir = "symmetric"
		}
		canonical = append(canonical, models.CandidateRelation{
			Name:       strings.ToUpper(cr.Name),
			Aliases:    cr.Aliases,
			SourceBase: cr.SourceBaseTypes,
			TargetBase: cr.TargetBaseTypes,
			Direction:  dir,
		})
	}

	return canonical, result.Rejected, nil
}

// VerifyTriplesRich validates triples with full entity profiles and evidence.
func VerifyTriplesRich(client *Client, triples []models.Triple, profiles map[string]*models.EntityProfile, relationSchema map[string]string) ([]string, []string, map[string]models.Triple, error) {
	if len(triples) == 0 {
		return nil, nil, nil, nil
	}

	var sb strings.Builder

	// Entity profiles section
	sb.WriteString("## Entity profiles:\n")
	for name, p := range profiles {
		sb.WriteString(fmt.Sprintf("- %s [%s", name, p.BaseType))
		if p.DomainType != "" {
			sb.WriteString("/" + p.DomainType)
		}
		sb.WriteString("]")
		if p.Description != "" {
			sb.WriteString(": " + p.Description)
		}
		sb.WriteString("\n")
	}

	// Relation schema section
	sb.WriteString("\n## Relation schema:\n")
	for name, desc := range relationSchema {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}

	// Triples with evidence
	sb.WriteString(fmt.Sprintf("\n## Triples to verify (%d):\n", len(triples)))
	for _, t := range triples {
		line := fmt.Sprintf("- %s (%s) -[%s]-> %s (%s)", t.Node1, t.Node1Type, t.Edge, t.Node2, t.Node2Type)
		if t.Evidence != "" {
			line += fmt.Sprintf(" [evidence: %s]", t.Evidence)
		}
		sb.WriteString(line + "\n")
	}

	response, err := client.Complete(RichVerificationPrompt, sb.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("rich verification failed: %w", err)
	}

	var result struct {
		Accept []struct {
			Node1  string `json:"node_1"`
			Edge   string `json:"edge"`
			Node2  string `json:"node_2"`
			Reason string `json:"reason"`
		} `json:"accept"`
		Reject []struct {
			Node1  string `json:"node_1"`
			Edge   string `json:"edge"`
			Node2  string `json:"node_2"`
			Reason string `json:"reason"`
		} `json:"reject"`
		Modify []struct {
			Node1    string `json:"node_1"`
			Edge     string `json:"edge"`
			Node2    string `json:"node_2"`
			NewEdge  string `json:"new_edge,omitempty"`
			NewNode1 string `json:"new_node_1,omitempty"`
			NewNode2 string `json:"new_node_2,omitempty"`
			Reason   string `json:"reason"`
		} `json:"modify"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse rich verification response: %v", err)
		return nil, nil, nil, nil
	}

	var acceptKeys, rejectKeys []string
	modifications := map[string]models.Triple{}

	for _, a := range result.Accept {
		acceptKeys = append(acceptKeys, strings.ToLower(a.Node1)+"|"+strings.ToUpper(a.Edge)+"|"+strings.ToLower(a.Node2))
	}
	for _, r := range result.Reject {
		key := strings.ToLower(r.Node1) + "|" + strings.ToUpper(r.Edge) + "|" + strings.ToLower(r.Node2)
		rejectKeys = append(rejectKeys, key)
		log.Printf("  Reject: %s -[%s]-> %s (%s)", r.Node1, r.Edge, r.Node2, r.Reason)
	}
	for _, m := range result.Modify {
		key := strings.ToLower(m.Node1) + "|" + strings.ToUpper(m.Edge) + "|" + strings.ToLower(m.Node2)
		newTriple := models.Triple{
			Node1: strings.ToLower(m.Node1),
			Node2: strings.ToLower(m.Node2),
			Edge:  strings.ToUpper(m.Edge),
		}
		if m.NewNode1 != "" {
			newTriple.Node1 = strings.ToLower(m.NewNode1)
		}
		if m.NewNode2 != "" {
			newTriple.Node2 = strings.ToLower(m.NewNode2)
		}
		if m.NewEdge != "" {
			newTriple.Edge = strings.ToUpper(m.NewEdge)
		}
		modifications[key] = newTriple
		log.Printf("  Modify: %s -[%s]-> %s → %s -[%s]-> %s (%s)",
			m.Node1, m.Edge, m.Node2, newTriple.Node1, newTriple.Edge, newTriple.Node2, m.Reason)
	}

	return acceptKeys, rejectKeys, modifications, nil
}

// StandardizeEntities asks the LLM to find duplicate entity names.
func StandardizeEntities(client *Client, entities []models.Entity) (map[string]string, error) {
	if len(entities) == 0 {
		return nil, nil
	}

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

	response, err := client.Complete(EntityStandardizationPrompt, sb.String())
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

// normalizeNodeName trims whitespace and lowercases.
func normalizeNodeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
