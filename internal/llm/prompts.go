package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

// Phase 1: Entity extraction prompt — extracts named entities with universal types, descriptions, and properties.
const EntityExtractionSystemPrompt = `You are a named entity extractor. Given text (delimited by triple backticks), extract all important named entities with their descriptions and properties.

IMPORTANT: The text may be in any language (Arabic, English, Chinese, mixed, etc.). Extract entity names in their ORIGINAL language. Do not translate.

## Rules
- Extract ONLY named or specific entities: proper nouns, named services, named places, specific roles tied to a person.
- An entity must be something you can point to: a specific person, a specific organization, a specific place, a specific named service.
- DO NOT extract generic words: "branch", "manager", "partner", "service", "appointment", "phone", "online", "samples", "stock", "booking", "active", "planned".
- DO NOT extract abstract operational concepts: "company strategy", "executive hiring", "billing policy", "vendor payment approvals", "monthly reporting", "clinical quality", "escalation review", "integration roadmap", "knowledge base", "operations team".
- DO NOT extract activity descriptions: "preventive health workshops", "routine blood testing", "chronic care management" UNLESS they are clearly named services offered by an organization.
- DO NOT extract pronouns, determiners, or status words.
- DO NOT extract raw values: dates, times, phone numbers, IDs. These are properties, not entities.
- DO NOT extract document titles unless they have a clear document ID (e.g. "SA-2024-019"). Generic titles like "internal operations knowledge base" are NOT entities.

## Type system
Classify each entity into exactly one of these universal types:
- person: Named individual (e.g. "Dr. Sarah Cohen", "Lina Mansour")
- organization: Company, network, lab, pharmacy, insurer, clinic branch, hospital (e.g. "CedarGate Health Network", "Haifa Central Clinic", "Carmel Pharmacy")
- location: City, region, country (e.g. "Haifa", "Tel Aviv", "Israel")
- address: Street address (e.g. "22 Herzl Street, Haifa")
- service: Something offered, provided, booked, or performed (e.g. "dermatology", "physical therapy", "blood testing")
- role: Job title or function (e.g. "branch manager", "chief medical officer")
- event: Named incident, meeting, or occurrence (e.g. "Incident CG-2025-004")
- document: Named document, policy, or agreement (e.g. "Service Agreement SA-2024-019")
- technology: Software, system, portal (e.g. "MyCedar portal", "LabSync Pro")
- concept: Abstract topic, rule, or category that doesn't fit above (e.g. "billing policy", "triage protocol")

IMPORTANT: Clinic branches and labs are "organization", NOT "person". Named services like "dermatology" are "service", NOT "organization".

## Description
For each entity, write a concise 1-sentence description summarizing what it is and its key attributes based on what the text says.

## Properties
Extract factual attributes as SIMPLE key-value properties. Use ONLY these property keys:
- status: "active", "planned", "closed", "under_evaluation", "suspended"
- phone: phone number
- email: email address
- website: URL
- founded: founding date
- opened: opening date
- role: person's job title
- specialty: professional specialty
- hours: operating hours

IMPORTANT: Property keys must be single lowercase words (e.g. "status", "phone", "role"). Do NOT create compound keys like "agreement_status_with_X" or "contract_details". Only include properties explicitly stated in the text.

## Output format (JSON)
{
  "entities": [
    {
      "name": "entity name in original language",
      "type": "one of the types above",
      "description": "concise 1-sentence description",
      "properties": {"key": "value"}
    }
  ]
}`

// Phase 2: Relation extraction prompt — extracts relations between known entities.
const RelationExtractionSystemPrompt = `You are a knowledge graph relation extractor. Given text and a list of known entities, extract factual relationships between them.

IMPORTANT: The text may be in any language. Use entity names exactly as provided in the entity list.

## Allowed relations (use ONLY these):
- WORKS_AT: person → organization (person is employed at org)
- MANAGED_BY: organization → person (org's BRANCH-LEVEL manager ONLY — not executives/C-suite)
- DEPUTY_MANAGER: organization → person (org's deputy/assistant manager)
- VISITS: person → organization (person visits org regularly)
- REPORTS_TO: person → person (person reports to another — ONLY if explicitly stated)
- SPECIALIZES_IN: person → service (person specializes in service)
- HAS_ROLE: person → role (person holds this job title, e.g. CEO, CFO, branch manager)
- OFFERS_SERVICE: organization → service (org offers this service)
- DOES_NOT_OFFER: organization → service (org explicitly does NOT offer this)
- HAS_PARTNER: organization → organization (org partners with another — ACTIVE partnership only)
- CONTRACTED_WITH: organization → organization (formal contract exists and is ACTIVE)
- NO_CONTRACT: organization → organization (explicitly no contract)
- LOCATED_AT: organization → address (org is at this address)
- LOCATED_IN: organization → location (org is in this city/region)
- PART_OF: organization → organization (child/branch is part of parent org, e.g. branch → network)
- HAS_BRANCH: organization → organization (parent has this ACTIVE branch, e.g. network → branch)
- HAS_PLANNED_BRANCH: organization → organization (parent has a PLANNED/FUTURE branch — not yet operational)
- HAS_HEADQUARTERS: organization → organization (org's main headquarters facility)
- FOUNDED_BY: organization → person (org was founded by person)
- ALIAS_OF: entity → entity (alternate name → canonical name — ONLY true aliases, not HQ/facilities)
- INVOLVED_IN: person → event (person involved in incident/event)
- USES_TECHNOLOGY: organization → technology (org uses this system)

## Direction rules (CRITICAL — follow these EXACTLY)
- WORKS_AT: person is node_1, organization is node_2
- MANAGED_BY: organization is node_1, person is node_2 (ONLY for branch managers, NOT executives)
- DEPUTY_MANAGER: organization is node_1, person is node_2
- VISITS: person is node_1, organization is node_2 (NEVER org → person)
- OFFERS_SERVICE: organization is node_1, service is node_2 (NEVER service → org)
- DOES_NOT_OFFER: organization is node_1, service is node_2
- SPECIALIZES_IN: person is node_1, service is node_2 (NEVER service → person)
- HAS_ROLE: person is node_1, role is node_2
- LOCATED_AT: organization is node_1, address is node_2 (NEVER address → org)
- PART_OF: child is node_1, parent is node_2 (branch → network)
- HAS_BRANCH: parent is node_1, child is node_2 (network → branch)
- HAS_PLANNED_BRANCH: parent is node_1, planned child is node_2
- HAS_HEADQUARTERS: organization is node_1, hq facility is node_2
- ALIAS_OF: alias/variant is node_1, canonical name is node_2

## MANAGED_BY rules (CRITICAL)
- MANAGED_BY is ONLY for branch-level managers (the person who runs a specific branch/clinic)
- C-suite executives (CEO, CFO, COO, CMO, CTO) should use: person → HAS_ROLE → role + person → WORKS_AT → organization
- Do NOT create MANAGED_BY edges for network-level executives
- Do NOT infer REPORTS_TO unless the text explicitly says "X reports to Y"

## Important
- Extract ONLY facts explicitly stated in the text. Do not infer or guess.
- If the text says someone does NOT work somewhere, do not create a WORKS_AT edge.
- Extract negative facts: "Branch X does not offer Y" → DOES_NOT_OFFER
- Distinguish ACTIVE from PLANNED: If a branch is "planned", "under construction", or "not yet open", use HAS_PLANNED_BRANCH not HAS_BRANCH.
- HQ/headquarters is a facility, NOT an alias of the org. Use HAS_HEADQUARTERS.
- Use entity names EXACTLY as given in the entity list. Do not create new entities.

## Output format (JSON)
{
  "triples": [
    {
      "node_1": "entity name from the list",
      "node_2": "entity name from the list",
      "edge": "one of the allowed relations above"
    }
  ]
}`

// EntityStandardizationPrompt asks the LLM to deduplicate entity names.
const EntityStandardizationPrompt = `You are an AI that standardizes entity names. Given a list of entity names extracted from a document, identify groups that refer to the same concept.

This includes:
- Same concept in different forms: "AI", "artificial intelligence", "A.I." -> "artificial intelligence"
- Same concept across languages: "الذكاء الاصطناعي", "artificial intelligence" -> pick the most frequent form
- Short names and full names: "Haifa Central" and "CedarGate Haifa Central Clinic" -> "cedargate haifa central clinic"
- Abbreviations: "CGHN" and "CedarGate Health Network" -> "cedargate health network"

Return a JSON mapping from variant names to the canonical (preferred, longest, most specific) name:
{
  "mappings": {
    "haifa central": "cedargate haifa central clinic",
    "cghn": "cedargate health network",
    "AI": "artificial intelligence"
  }
}

Only include entries that need mapping. If a name is already canonical, do not include it.`

// ExtractEntitiesFromChunk extracts entities with types from a text chunk (Phase 1).
func ExtractEntitiesFromChunk(client *Client, text string) ([]models.Entity, error) {
	userPrompt := fmt.Sprintf("```%s```", text)

	response, err := client.Complete(EntityExtractionSystemPrompt, userPrompt)
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
func ExtractRelationsFromChunk(client *Client, text string, entities []models.Entity, chunkID string) ([]models.Triple, error) {
	// Build entity list for the prompt
	entityLines := make([]string, 0, len(entities))
	for _, e := range entities {
		entityLines = append(entityLines, fmt.Sprintf("- %s (%s)", e.Name, e.Type))
	}
	entityList := strings.Join(entityLines, "\n")

	userPrompt := fmt.Sprintf("Known entities:\n%s\n\nText: ```%s```", entityList, text)

	response, err := client.Complete(RelationExtractionSystemPrompt, userPrompt)
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
		// Tag with entity types from Phase 1
		if t, ok := typeMap[result.Triples[i].Node1]; ok {
			result.Triples[i].Node1Type = t
		}
		if t, ok := typeMap[result.Triples[i].Node2]; ok {
			result.Triples[i].Node2Type = t
		}
	}

	return result.Triples, nil
}

// StandardizeEntities asks the LLM to find duplicate entity names and return a canonical mapping.
func StandardizeEntities(client *Client, entityNames []string) (map[string]string, error) {
	if len(entityNames) == 0 {
		return nil, nil
	}

	namesJSON, _ := json.Marshal(entityNames)
	userPrompt := fmt.Sprintf("Entity names:\n%s", string(namesJSON))

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

// normalizeNodeName trims whitespace and lowercases Latin characters.
func normalizeNodeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
