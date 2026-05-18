package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// SchemaConstrainedExtractionPrompt generates the extraction prompt
// constrained to the predefined schema. The LLM MUST use these types and relations.
func SchemaConstrainedExtractionPrompt() string {
	baseTypes := schema.FormatBaseTypesForPrompt()
	relations := schema.FormatRelationsForPrompt()
	roles := schema.FormatFunctionalRolesForPrompt()
	statuses := schema.FormatStatusesForPrompt()

	return fmt.Sprintf(`You are a knowledge graph candidate extractor. Extract entities and relationships from the given text.

IMPORTANT RULES:
1. You are extracting CANDIDATES, not final facts. Include confidence scores.
2. The text may be in ANY language. Preserve original entity names.
3. You MUST use the predefined base types and relation IDs below. Do NOT invent new ones.
4. For entity types: assign one or more base_types from the list, plus optional domain_types (freeform subtypes).
5. For relations: use ONLY the relation IDs listed below. If no relation fits, skip that fact.
6. When the same entity pair could have different relations, include ALL candidates with scores.
7. Copy EXACT evidence text from the source (do not paraphrase).
8. Assign functional_roles from the controlled list when evidence supports them.
9. Assign a status from the controlled list based on evidence. Default to "active" if operating, "unknown" if unclear.
10. NEGATIVE FACTS: When text says "X does NOT work at Y", "X no longer at Y", "does not offer Z", extract the negative relation (DOES_NOT_WORK_AT, DOES_NOT_OFFER, NO_CONTRACT_WITH). Do NOT convert negations into positive relations.
11. BRANCH TYPING: Branch offices, subsidiaries, and operational sites of an organization are base_type "organization", NOT "location". Only use "location" for pure geographic places (cities, countries, neighborhoods).
12. PLANNED vs ACTIVE: If an entity is described as planned, upcoming, or future, set status to "planned" and use PLANNED_SERVICE (not OFFERS) for its services. Add functional_role "planned_unit".

## PREDEFINED BASE TYPES (you MUST use these):
%s

## FUNCTIONAL ROLES (assign when evidence supports):
%s

## ENTITY STATUSES (assign one):
%s

## PREDEFINED RELATIONS (you MUST use these IDs):
%s

## SCORING GUIDELINES:
- 0.90+: Text explicitly and unambiguously states this
- 0.70-0.89: Strong evidence, very likely correct
- 0.50-0.69: Reasonable inference from context
- 0.30-0.49: Weak evidence, uncertain
- Below 0.30: Do not include

## OUTPUT FORMAT (JSON):
{
  "entities": [
    {
      "mention": "entity name as it appears in text",
      "canonical_candidate": "normalized/canonical form of the name",
      "base_type_candidates": [
        {"type": "organization", "score": 0.92},
        {"type": "location", "score": 0.35}
      ],
      "domain_type_candidates": [
        {"type": "branch_office", "score": 0.88}
      ],
      "functional_roles": ["branch", "operated_unit"],
      "status": "active",
      "aliases": [{"text": "short name", "lang": "en"}],
      "evidence": "exact sentence from text mentioning this entity"
    }
  ],
  "edges": [
    {
      "from_mention": "entity name",
      "relation_id": "RELATION_ID_FROM_LIST_ABOVE",
      "to_mention": "entity name",
      "evidence_text": "exact sentence from text supporting this fact",
      "evidence_language": "en",
      "confidence": 0.85,
      "status": "active | planned | backup | conditional | historical | unknown",
      "condition": "empty string unless the fact depends on if/when/during/unless — then describe the condition"
    }
  ]
}`, baseTypes, roles, statuses, relations)
}

// ExtractWithSchema extracts candidates from text using the schema-constrained prompt.
func ExtractWithSchema(client *Client, text string, chunkID string) ([]models.CandidateEntity, []models.CandidateEdge, error) {
	systemPrompt := SchemaConstrainedExtractionPrompt()
	userPrompt := fmt.Sprintf("```%s```", text)

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("schema extraction failed: %w", err)
	}

	var result struct {
		Entities []struct {
			Mention          string `json:"mention"`
			CanonicalCandidate string `json:"canonical_candidate"`
			BaseTypeCandidates []struct {
				Type  string  `json:"type"`
				Score float64 `json:"score"`
			} `json:"base_type_candidates"`
			DomainTypeCandidates []struct {
				Type  string  `json:"type"`
				Score float64 `json:"score"`
			} `json:"domain_type_candidates"`
			FunctionalRoles []string `json:"functional_roles"`
			Status          string   `json:"status"`
			Aliases []struct {
				Text string `json:"text"`
				Lang string `json:"lang"`
			} `json:"aliases"`
			Evidence string `json:"evidence"`
		} `json:"entities"`
		Edges []struct {
			FromMention      string  `json:"from_mention"`
			RelationID       string  `json:"relation_id"`
			ToMention        string  `json:"to_mention"`
			EvidenceText     string  `json:"evidence_text"`
			EvidenceLanguage string  `json:"evidence_language"`
			Confidence       float64 `json:"confidence"`
			Status           string  `json:"status"`
			Condition        string  `json:"condition"`
		} `json:"edges"`
	}

	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse schema extraction response: %v\nResponse: %.200s", err, response)
		return nil, nil, nil
	}

	// Build entities
	var entities []models.CandidateEntity
	for _, e := range result.Entities {
		mention := strings.TrimSpace(e.Mention)
		if mention == "" {
			continue
		}

		ce := models.CandidateEntity{
			Mention:       strings.ToLower(mention),
			CanonicalName: strings.ToLower(strings.TrimSpace(e.CanonicalCandidate)),
			ChunkID:       chunkID,
		}
		if ce.CanonicalName == "" {
			ce.CanonicalName = ce.Mention
		}

		// Validate and set functional roles
		for _, role := range e.FunctionalRoles {
			role = strings.ToLower(strings.TrimSpace(role))
			if role != "" && schema.IsValidFunctionalRole(role) {
				ce.FunctionalRoles = append(ce.FunctionalRoles, role)
			}
		}

		// Validate and set status
		status := strings.ToLower(strings.TrimSpace(e.Status))
		if status != "" && schema.IsValidStatus(status) {
			ce.Status = status
		}

		for _, bt := range e.BaseTypeCandidates {
			t := strings.ToLower(strings.TrimSpace(bt.Type))
			if t != "" && schema.IsValidBaseType(t) {
				ce.BaseTypes = append(ce.BaseTypes, models.ScoredType{Type: t, Score: bt.Score})
			}
		}
		for _, dt := range e.DomainTypeCandidates {
			t := strings.ToLower(strings.TrimSpace(dt.Type))
			if t != "" {
				ce.DomainTypes = append(ce.DomainTypes, models.ScoredType{Type: t, Score: dt.Score})
			}
		}
		for _, a := range e.Aliases {
			if a.Text != "" {
				ce.Aliases = append(ce.Aliases, models.LangText{Text: strings.ToLower(a.Text), Lang: a.Lang})
			}
		}
		if e.Evidence != "" {
			ce.Evidence = append(ce.Evidence, models.EvidenceRef{
				Text:    e.Evidence,
				ChunkID: chunkID,
			})
		}

		entities = append(entities, ce)
	}

	// Build edges
	var edges []models.CandidateEdge
	for i, e := range result.Edges {
		from := strings.ToLower(strings.TrimSpace(e.FromMention))
		to := strings.ToLower(strings.TrimSpace(e.ToMention))
		rawRelation := strings.ToUpper(strings.TrimSpace(e.RelationID))

		if from == "" || to == "" || rawRelation == "" {
			continue
		}

		// Normalize relation to canonical ID
		relationID, known := schema.ResolveRelation(rawRelation)
		if relationID == "" {
			continue // explicitly rejected relation
		}

		schemaFit := 0.0
		if known {
			schemaFit = 1.0
		} else {
			schemaFit = 0.3 // unknown relation penalty
		}

		edge := models.CandidateEdge{
			ID:             fmt.Sprintf("e_%s_%d", chunkID, i),
			FromMention:    from,
			RelationRaw:    rawRelation,
			RelationID:     relationID,
			ToMention:      to,
			EvidenceText:   e.EvidenceText,
			EvidenceLang:   e.EvidenceLanguage,
			ChunkID:        chunkID,
			EvidenceScore:  e.Confidence,
			SchemaFitScore: schemaFit,
			Confidence:     e.Confidence,
		}

		// Parse edge status
		status := strings.ToLower(strings.TrimSpace(e.Status))
		if status != "" && schema.IsValidEdgeStatus(status) {
			edge.Status = status
		}

		// Parse edge condition
		cond := strings.TrimSpace(e.Condition)
		if cond != "" {
			edge.Condition = cond
		}

		edges = append(edges, edge)
	}

	return entities, edges, nil
}
