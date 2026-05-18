// Deprecated: This file contains old LLM type governance from the pre-schema pipeline.
// The active pipeline uses predefined schema in schema/ontology.go with
// hard constraints in solver/hard_constraints.go.
package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/internal/schema"
)

// SchemaGovernanceTypePrompt asks the LLM to decide whether proposed types are
// new, synonyms, subtypes, or invalid relative to the existing schema.
const SchemaGovernanceTypePrompt = `You are a schema governance engine. You receive proposed entity types and the current accepted schema. For each proposed type, decide:

1. "new" — it represents a genuinely new concept not covered by any existing type
2. "synonym" — it means the same thing as an existing type (just a naming variant)
3. "subtype" — it is a more specific version of an existing type (parent-child)
4. "too_vague" — it is too generic/vague to be useful as a domain type
5. "invalid" — it is noise, not a real type

## Current accepted entity types:
%s

## Current base types (upper ontology):
%s

## Rules
- If a proposed type has the SAME words rearranged (e.g. "branch_office" vs "office_branch"), it's a synonym.
- If a proposed type adds a redundant modifier (e.g. "corporate_branch_office" when "branch_office" exists), it's a synonym of the shorter form.
- If a proposed type is genuinely more specific (e.g. "legal_office" under "branch_office"), it's a subtype.
- For NEW types, specify which base type(s) it belongs to.
- For SYNONYM, specify the canonical (existing) type it maps to.
- For SUBTYPE, specify the parent type it belongs under.

## Output format (JSON)
{
  "decisions": [
    {
      "proposed": "type_name",
      "decision": "new|synonym|subtype|too_vague|invalid",
      "canonical": "existing_type_name (if synonym or subtype parent)",
      "base_types": ["base_type_1"],
      "description": "what this type represents (for new types)",
      "confidence": 0.95
    }
  ]
}`

// SchemaGovernanceRelationPrompt asks the LLM to decide whether proposed relations are
// new, synonyms, inverses, or invalid relative to the existing schema.
const SchemaGovernanceRelationPrompt = `You are a schema governance engine. You receive proposed relation names and the current accepted relation schema. For each proposed relation, decide:

1. "new" — genuinely new relation not covered by any existing one
2. "synonym" — same meaning as an existing relation (just a naming variant)
3. "inverse" — the inverse/passive form of an existing relation (direction flipped)
4. "too_vague" — too generic or verbose to be useful
5. "invalid" — noise, not a real relation

## Current accepted relations:
%s

## Rules
- MANAGED_BY is the inverse of MANAGES (same relation, flipped direction)
- WORKS_FOR is a synonym of EMPLOYED_BY (same meaning)
- BRANCH_MANAGER_OF is too verbose and specific — it should be a synonym of MANAGES
- Relations with 4+ words are almost always too verbose
- For NEW relations, define source and target base types + whether it's symmetric
- For SYNONYM, specify the canonical relation it maps to
- For INVERSE, specify the canonical relation and note the direction flip
- Be aggressive about merging — a relation should only be NEW if its meaning is truly unique

## Output format (JSON)
{
  "decisions": [
    {
      "proposed": "RELATION_NAME",
      "decision": "new|synonym|inverse|too_vague|invalid",
      "canonical": "EXISTING_RELATION (if synonym or inverse)",
      "source_base_types": ["person"],
      "target_base_types": ["organization"],
      "symmetric": false,
      "description": "what this relation means (for new relations)",
      "confidence": 0.95
    }
  ]
}`

// GovernTypeCandidates sends candidate types to the LLM for governance decisions.
// Returns updated candidates with decisions filled in.
func GovernTypeCandidates(client *Client, s *schema.Schema, candidates []schema.CandidateType) ([]schema.CandidateType, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	typeSummary := s.EntityTypeSummary()
	baseSummary := s.BaseTypeSummary()
	systemPrompt := fmt.Sprintf(SchemaGovernanceTypePrompt, typeSummary, baseSummary)

	// Build user prompt
	var sb strings.Builder
	sb.WriteString("Proposed types to evaluate:\n")
	for _, c := range candidates {
		line := fmt.Sprintf("- %s", c.ProposedName)
		if len(c.ProposedBases) > 0 {
			line += fmt.Sprintf(" (proposed bases: %s)", strings.Join(c.ProposedBases, ", "))
		}
		if c.Evidence != "" {
			line += fmt.Sprintf(" [evidence: %s]", truncate(c.Evidence, 100))
		}
		if c.Decision != "" {
			line += fmt.Sprintf(" [heuristic suggests: %s → %s]", c.Decision, c.CanonicalName)
		}
		sb.WriteString(line + "\n")
	}

	response, err := client.Complete(systemPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("type governance failed: %w", err)
	}

	var result struct {
		Decisions []struct {
			Proposed    string   `json:"proposed"`
			Decision    string   `json:"decision"`
			Canonical   string   `json:"canonical"`
			BaseTypes   []string `json:"base_types"`
			Description string   `json:"description"`
			Confidence  float64  `json:"confidence"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse type governance response: %v", err)
		return nil, nil
	}

	// Map decisions back to candidates
	decisionMap := map[string]struct {
		decision, canonical, description string
		baseTypes                        []string
		confidence                       float64
	}{}
	for _, d := range result.Decisions {
		decisionMap[strings.ToLower(d.Proposed)] = struct {
			decision, canonical, description string
			baseTypes                        []string
			confidence                       float64
		}{d.Decision, strings.ToLower(d.Canonical), d.Description, d.BaseTypes, d.Confidence}
	}

	for i := range candidates {
		if d, ok := decisionMap[strings.ToLower(candidates[i].ProposedName)]; ok {
			candidates[i].Decision = d.decision
			candidates[i].CanonicalName = d.canonical
			candidates[i].Confidence = d.confidence
			if d.description != "" {
				candidates[i].Evidence = d.description
			}
			if len(d.baseTypes) > 0 {
				candidates[i].ProposedBases = d.baseTypes
			}
		}
	}

	return candidates, nil
}

// GovernRelationCandidates sends candidate relations to the LLM for governance decisions.
func GovernRelationCandidates(client *Client, s *schema.Schema, candidates []schema.CandidateRelation) ([]schema.CandidateRelation, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	relSummary := s.RelationTypeSummary()
	systemPrompt := fmt.Sprintf(SchemaGovernanceRelationPrompt, relSummary)

	var sb strings.Builder
	sb.WriteString("Proposed relations to evaluate:\n")
	for _, c := range candidates {
		line := fmt.Sprintf("- %s", c.ProposedName)
		if len(c.SourceBaseTypes) > 0 {
			line += fmt.Sprintf(" (observed: %s → %s)", strings.Join(c.SourceBaseTypes, "|"), strings.Join(c.TargetBaseTypes, "|"))
		}
		if c.Decision != "" {
			line += fmt.Sprintf(" [heuristic suggests: %s → %s]", c.Decision, c.CanonicalName)
		}
		sb.WriteString(line + "\n")
	}

	response, err := client.Complete(systemPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("relation governance failed: %w", err)
	}

	var result struct {
		Decisions []struct {
			Proposed        string   `json:"proposed"`
			Decision        string   `json:"decision"`
			Canonical       string   `json:"canonical"`
			SourceBaseTypes []string `json:"source_base_types"`
			TargetBaseTypes []string `json:"target_base_types"`
			Symmetric       bool     `json:"symmetric"`
			Description     string   `json:"description"`
			Confidence      float64  `json:"confidence"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse relation governance response: %v", err)
		return nil, nil
	}

	decisionMap := map[string]struct {
		decision, canonical, description string
		sourceTypes, targetTypes         []string
		symmetric                        bool
		confidence                       float64
	}{}
	for _, d := range result.Decisions {
		decisionMap[strings.ToUpper(d.Proposed)] = struct {
			decision, canonical, description string
			sourceTypes, targetTypes         []string
			symmetric                        bool
			confidence                       float64
		}{d.Decision, strings.ToUpper(d.Canonical), d.Description, d.SourceBaseTypes, d.TargetBaseTypes, d.Symmetric, d.Confidence}
	}

	for i := range candidates {
		if d, ok := decisionMap[strings.ToUpper(candidates[i].ProposedName)]; ok {
			candidates[i].Decision = d.decision
			candidates[i].CanonicalName = d.canonical
			candidates[i].Confidence = d.confidence
			candidates[i].Symmetric = d.symmetric
			if len(d.sourceTypes) > 0 {
				candidates[i].SourceBaseTypes = d.sourceTypes
			}
			if len(d.targetTypes) > 0 {
				candidates[i].TargetBaseTypes = d.targetTypes
			}
		}
	}

	return candidates, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
