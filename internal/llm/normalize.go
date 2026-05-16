package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/internal/schema"
)

// GlobalSchemaNormalizationPrompt is a strict prompt that asks the LLM to act as a
// schema compiler — NOT an extractor. It groups and canonicalizes types and relations.
const GlobalSchemaNormalizationPrompt = `You are a schema normalizer for a knowledge graph. You are NOT extracting new facts.

You receive:
- Candidate entity types discovered across the entire document (with counts and examples)
- Candidate relations discovered across the entire document (with counts and examples)
- The current base type scaffold

Your job is to produce a NORMALIZED, CANONICAL schema by:

1. GROUP semantically equivalent entity types → one canonical domain type per group
2. MAP each canonical domain type to one or more base types from the scaffold
3. DETECT role-like types (job titles, professions, positions) — these should map to base_type "role"
   - Entities with role types that are clearly people should remain "person" with the role as domain_type
   - Example: "branch_manager" is a role, "physiotherapist" is a role, "chief_medical_officer" is a role
4. GROUP semantically equivalent relations → one canonical relation per group
5. DEFINE canonical direction for each relation (source_base_type -> target_base_type)
6. IDENTIFY aliases (same direction) and inverse aliases (flipped direction)
7. REJECT vague/noisy relations that add no factual value

## Base type scaffold:
%s

## CRITICAL RULES:
- Be AGGRESSIVE about merging. Similar types/relations MUST be unified.
- clinic, clinic_branch, clinic_site, medical_center → ONE canonical type
- MANAGES, BRANCH_MANAGER, BRANCH_MANAGER_OF → ONE canonical relation
- Role-like types (any job title or profession) MUST have base_type "role"
- Relations with 4+ words are too verbose — map them to shorter canonical forms
- OFFERS/PROVIDES with target=service are the same relation
- Negative relations (DOES_NOT_OFFER, DOES_NOT_PROVIDE) should be preserved as-is
- Direction matters: MANAGED_BY means org->person, MANAGES means person->org (inverse)
- EVERY relation candidate must appear in EXACTLY ONE of: a canonical relation's aliases/inverse_aliases, or rejected_relations. Do NOT leave any candidate unaccounted for.
- Common employment/affiliation patterns: WORKS_AT, EMPLOYED_AT, BASED_AT, STATIONED_AT → pick ONE canonical (e.g. BASED_AT person→organization). Include ALL variants as aliases/inverses.
- Visiting/temporary patterns: VISITS, VISITING, CONSULTS_AT → pick ONE canonical (e.g. VISITS person→organization)
- ALIAS_OF should ALWAYS be rejected — entity aliases belong in name standardization, not as graph edges.
- HANDLES, PROCESSES, FULFILLS → group into one canonical if they mean the same thing in context
- HAS_ROLE always means person → role. Include it as canonical even if not in candidates.

## Output format (JSON only, no commentary):
{
  "type_normalization": [
    {
      "canonical_domain_type": "clinic_branch",
      "base_types": ["organization"],
      "aliases": ["clinic", "clinic_site", "medical_center", "healthcare_facility"],
      "notes": "A healthcare delivery branch or site."
    }
  ],
  "relation_normalization": [
    {
      "canonical_relation": "MANAGED_BY",
      "direction": "organization -> person",
      "source_base_types": ["organization"],
      "target_base_types": ["person"],
      "symmetric": false,
      "aliases": ["HAS_MANAGER"],
      "inverse_aliases": ["MANAGES", "BRANCH_MANAGER_OF"],
      "reject_aliases": []
    }
  ],
  "rejected_relations": [
    {
      "relation": "ASSOCIATED_WITH",
      "reason": "Too vague for factual KG"
    }
  ]
}`

// NormalizeSchema performs the global schema normalization pass.
// It collects all type and relation candidates, sends them to the LLM for normalization,
// and returns the structured normalization result.
func NormalizeSchema(client *Client, baseSummary string, typeCandidates []schema.TypeCandidate, relationCandidates []schema.RelationCandidate) (*schema.SchemaNormalization, error) {
	if len(typeCandidates) == 0 && len(relationCandidates) == 0 {
		return nil, nil
	}

	systemPrompt := fmt.Sprintf(GlobalSchemaNormalizationPrompt, baseSummary)

	// Build user prompt with candidates + examples
	var sb strings.Builder

	// Type candidates
	sb.WriteString("## Entity type candidates:\n")
	for i, tc := range typeCandidates {
		if i >= 80 { // cap to avoid prompt overflow
			sb.WriteString(fmt.Sprintf("... and %d more type candidates\n", len(typeCandidates)-80))
			break
		}
		sb.WriteString(fmt.Sprintf("- %s (count: %d)\n", tc.Name, tc.Count))
		for j, ex := range tc.Examples {
			if j >= 3 {
				break
			}
			line := fmt.Sprintf("    entity: %s", ex.EntityName)
			if ex.Description != "" {
				line += fmt.Sprintf(" | desc: %s", truncateStr(ex.Description, 80))
			}
			if ex.Evidence != "" {
				line += fmt.Sprintf(" | evidence: %s", truncateStr(ex.Evidence, 100))
			}
			sb.WriteString(line + "\n")
		}
	}

	// Relation candidates
	sb.WriteString("\n## Relation candidates:\n")
	for i, rc := range relationCandidates {
		if i >= 80 {
			sb.WriteString(fmt.Sprintf("... and %d more relation candidates\n", len(relationCandidates)-80))
			break
		}
		sb.WriteString(fmt.Sprintf("- %s (count: %d)\n", rc.Name, rc.Count))
		for j, ex := range rc.Examples {
			if j >= 3 {
				break
			}
			line := fmt.Sprintf("    %s (%s) -[%s]-> %s (%s)", ex.From, ex.FromType, ex.Relation, ex.To, ex.ToType)
			if ex.Evidence != "" {
				line += fmt.Sprintf(" | evidence: %s", truncateStr(ex.Evidence, 100))
			}
			sb.WriteString(line + "\n")
		}
	}

	response, err := client.Complete(systemPrompt, sb.String())
	if err != nil {
		return nil, fmt.Errorf("global schema normalization failed: %w", err)
	}

	var result schema.SchemaNormalization
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse schema normalization response: %v\nResponse prefix: %.500s", err, response)
		return nil, fmt.Errorf("parse failed: %w", err)
	}

	return &result, nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
