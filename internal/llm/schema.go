package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// RefineSchemaPrompt asks the LLM to review discovered types/relations and provide descriptions + constraints.
const RefineSchemaPrompt = `You are a knowledge graph schema architect. You are given entity types and relation types that were automatically discovered from documents. Your job is to:

1. Provide clear descriptions for each type
2. Identify parent types (type hierarchy) where appropriate
3. Define direction constraints for relations (which entity types can be source vs target)
4. Identify symmetric relations (where direction doesn't matter)
5. Suggest merging any duplicate/redundant types

## Input
You will receive:
- A list of discovered entity types
- A list of discovered relation types with their observed source/target type pairs

## Output format (JSON)
{
  "entity_types": [
    {
      "name": "type_name",
      "description": "what this type represents",
      "parent_type": "optional parent type for hierarchy (e.g. 'hospital' parent is 'organization')"
    }
  ],
  "relation_types": [
    {
      "name": "RELATION_NAME",
      "description": "what this relation means",
      "source_types": ["allowed_source_type_1", "allowed_source_type_2"],
      "target_types": ["allowed_target_type_1"],
      "symmetric": false
    }
  ],
  "merge_types": {
    "duplicate_type": "canonical_type"
  }
}`

// ClassifyEntitiesWithSchemaPrompt asks the LLM to classify entities using the current schema.
const ClassifyEntitiesWithSchemaPrompt = `You are an entity type classifier for a knowledge graph. Given a list of entities and the graph's current type schema, classify each entity into the most appropriate type.

## Current schema types:
%s

## Rules
- Use existing types from the schema when they fit.
- If an entity clearly doesn't fit any existing type, you may propose a NEW type — but only when truly necessary.
- When proposing a new type, include a description.
- Use the relationship context provided to help determine types (e.g., if entity appears as target of EMPLOYS, it's likely a person).
- Be consistent: similar entities should get the same type.

## Output format (JSON)
{
  "classifications": {
    "entity name": "type_name"
  },
  "new_types": [
    {
      "name": "new_type_name",
      "description": "what this type represents",
      "parent_type": "optional parent"
    }
  ]
}`

// ValidateTriplesWithSchemaPrompt asks the LLM to validate triples against the schema.
const ValidateTriplesWithSchemaPrompt = `You are a knowledge graph validator. Given a set of triples and the graph's schema (entity types and relation definitions), validate each triple and suggest corrections.

## Current schema:
### Entity types:
%s

### Relation types (with direction constraints):
%s

## Your tasks:
1. Check if each triple's entity types match the relation's source/target constraints
2. If a triple's direction is wrong (source/target swapped), mark it for flipping
3. If a relation name is non-standard but maps to an existing schema relation, normalize it
4. If a relation doesn't exist in the schema, either map it to an existing one or mark it as a new relation to add
5. Remove triples that are clearly wrong or nonsensical

## Output format (JSON)
{
  "valid": [
    {"node_1": "...", "node_2": "...", "edge": "...", "node_1_type": "...", "node_2_type": "..."}
  ],
  "flip": [
    {"node_1": "...", "node_2": "...", "edge": "...", "reason": "..."}
  ],
  "normalize": [
    {"node_1": "...", "node_2": "...", "old_edge": "...", "new_edge": "...", "reason": "..."}
  ],
  "remove": [
    {"node_1": "...", "node_2": "...", "edge": "...", "reason": "..."}
  ],
  "new_relations": [
    {
      "name": "RELATION_NAME",
      "description": "what this relation means",
      "source_types": ["type1"],
      "target_types": ["type2"],
      "symmetric": false
    }
  ]
}`

// InferEntityTypesPrompt asks the LLM to infer types for entities that have no type.
const InferEntityTypesPrompt = `You are an entity type inference engine. Given entities with missing or uncertain types, infer the correct type based on:
1. The entity name itself
2. How it's used in relationships (context)
3. The current schema's type definitions

## Current schema types:
%s

## Rules
- Classify into existing schema types when possible
- If a name looks like a person's name (first + last name in any language), classify as person
- If a name contains organizational keywords, classify accordingly
- You may propose new types ONLY when no existing type fits at all
- Be decisive — pick the single best type for each entity

## Output format (JSON)
{
  "types": {
    "entity name": "type_name"
  },
  "new_types": [
    {"name": "new_type", "description": "...", "parent_type": "optional"}
  ]
}`

// RefineSchema asks the LLM to review and improve the automatically-discovered schema.
// Limits input to avoid timeout on large schemas.
func RefineSchema(client *Client, s *schema.Schema) ([]schema.EntityType, []schema.RelationType, map[string]string, error) {
	entityTypes := s.EntityTypeNames()
	relationTypes := s.RelationTypeNames()

	if len(entityTypes) == 0 && len(relationTypes) == 0 {
		return nil, nil, nil, nil
	}

	// Build input for the LLM, limiting to avoid timeout
	var sb strings.Builder
	sb.WriteString("## Discovered entity types:\n")
	for i, name := range entityTypes {
		if i >= 50 {
			sb.WriteString(fmt.Sprintf("... and %d more types\n", len(entityTypes)-50))
			break
		}
		et := s.GetEntityType(name)
		if et.Description != "" {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", name, et.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s\n", name))
		}
	}
	sb.WriteString("\n## Discovered relation types:\n")
	for i, name := range relationTypes {
		if i >= 40 {
			sb.WriteString(fmt.Sprintf("... and %d more relations\n", len(relationTypes)-40))
			break
		}
		rt := s.GetRelationType(name)
		src := strings.Join(rt.SourceTypes, ", ")
		tgt := strings.Join(rt.TargetTypes, ", ")
		sb.WriteString(fmt.Sprintf("- %s: observed sources=[%s], targets=[%s]\n", name, src, tgt))
	}

	response, err := client.Complete(RefineSchemaPrompt, sb.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("schema refinement failed: %w", err)
	}

	var result struct {
		EntityTypes   []schema.EntityType   `json:"entity_types"`
		RelationTypes []schema.RelationType `json:"relation_types"`
		MergeTypes    map[string]string     `json:"merge_types"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse schema refinement response: %v", err)
		return nil, nil, nil, nil
	}

	return result.EntityTypes, result.RelationTypes, result.MergeTypes, nil
}

// ClassifyEntitiesWithSchema asks the LLM to classify entities using the current schema.
func ClassifyEntitiesWithSchema(client *Client, s *schema.Schema, entities map[string]string, triples []models.Triple) (map[string]string, []schema.EntityType, error) {
	if len(entities) == 0 {
		return nil, nil, nil
	}

	typeSummary := s.EntityTypeSummary()
	systemPrompt := fmt.Sprintf(ClassifyEntitiesWithSchemaPrompt, typeSummary)

	// Build relation context
	relContext := map[string][]string{}
	for _, t := range triples {
		if _, ok := entities[t.Node1]; ok {
			relContext[t.Node1] = append(relContext[t.Node1], fmt.Sprintf("-[%s]-> %s", t.Edge, t.Node2))
		}
		if _, ok := entities[t.Node2]; ok {
			relContext[t.Node2] = append(relContext[t.Node2], fmt.Sprintf("%s -[%s]->", t.Node1, t.Edge))
		}
	}

	lines := make([]string, 0, len(entities))
	for name := range entities {
		if rels, ok := relContext[name]; ok && len(rels) > 0 {
			if len(rels) > 5 {
				rels = rels[:5]
			}
			lines = append(lines, fmt.Sprintf("- %s (relations: %s)", name, strings.Join(rels, "; ")))
		} else {
			lines = append(lines, fmt.Sprintf("- %s", name))
		}
	}
	userPrompt := fmt.Sprintf("Classify these entities:\n%s", strings.Join(lines, "\n"))

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("entity classification failed: %w", err)
	}

	var result struct {
		Classifications map[string]string   `json:"classifications"`
		NewTypes        []schema.EntityType `json:"new_types"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse classification response: %v", err)
		return nil, nil, nil
	}

	return result.Classifications, result.NewTypes, nil
}

// InferEntityTypes asks the LLM to infer types for untyped entities.
func InferEntityTypes(client *Client, s *schema.Schema, entities []string, triples []models.Triple) (map[string]string, []schema.EntityType, error) {
	if len(entities) == 0 {
		return nil, nil, nil
	}

	typeSummary := s.EntityTypeSummary()
	systemPrompt := fmt.Sprintf(InferEntityTypesPrompt, typeSummary)

	// Build context from triples
	relContext := map[string][]string{}
	entitySet := map[string]bool{}
	for _, e := range entities {
		entitySet[e] = true
	}
	for _, t := range triples {
		if entitySet[t.Node1] {
			relContext[t.Node1] = append(relContext[t.Node1], fmt.Sprintf("-[%s]-> %s (%s)", t.Edge, t.Node2, t.Node2Type))
		}
		if entitySet[t.Node2] {
			relContext[t.Node2] = append(relContext[t.Node2], fmt.Sprintf("%s (%s) -[%s]->", t.Node1, t.Node1Type, t.Edge))
		}
	}

	lines := make([]string, 0, len(entities))
	for _, name := range entities {
		if rels, ok := relContext[name]; ok && len(rels) > 0 {
			if len(rels) > 5 {
				rels = rels[:5]
			}
			lines = append(lines, fmt.Sprintf("- %s (context: %s)", name, strings.Join(rels, "; ")))
		} else {
			lines = append(lines, fmt.Sprintf("- %s", name))
		}
	}
	userPrompt := fmt.Sprintf("Infer types for these entities:\n%s", strings.Join(lines, "\n"))

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("type inference failed: %w", err)
	}

	var result struct {
		Types    map[string]string   `json:"types"`
		NewTypes []schema.EntityType `json:"new_types"`
	}
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse type inference response: %v", err)
		return nil, nil, nil
	}

	return result.Types, result.NewTypes, nil
}

// ValidateTriplesWithSchema asks the LLM to validate triples against the schema.
func ValidateTriplesWithSchema(client *Client, s *schema.Schema, triples []models.Triple) (*ValidationResult, error) {
	if len(triples) == 0 {
		return &ValidationResult{}, nil
	}

	typeSummary := s.EntityTypeSummary()
	relSummary := s.RelationTypeSummary()
	systemPrompt := fmt.Sprintf(ValidateTriplesWithSchemaPrompt, typeSummary, relSummary)

	// Build triple list
	lines := make([]string, 0, len(triples))
	for _, t := range triples {
		lines = append(lines, fmt.Sprintf("- %s (%s) -[%s]-> %s (%s)",
			t.Node1, t.Node1Type, t.Edge, t.Node2, t.Node2Type))
	}
	userPrompt := fmt.Sprintf("Validate these triples:\n%s", strings.Join(lines, "\n"))

	response, err := client.Complete(systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("triple validation failed: %w", err)
	}

	var result ValidationResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		log.Printf("Warning: failed to parse validation response: %v", err)
		return nil, nil
	}

	return &result, nil
}

// ValidationResult holds the LLM's triple validation output.
type ValidationResult struct {
	Valid []struct {
		Node1     string `json:"node_1"`
		Node2     string `json:"node_2"`
		Edge      string `json:"edge"`
		Node1Type string `json:"node_1_type"`
		Node2Type string `json:"node_2_type"`
	} `json:"valid"`
	Flip []struct {
		Node1  string `json:"node_1"`
		Node2  string `json:"node_2"`
		Edge   string `json:"edge"`
		Reason string `json:"reason"`
	} `json:"flip"`
	Normalize []struct {
		Node1   string `json:"node_1"`
		Node2   string `json:"node_2"`
		OldEdge string `json:"old_edge"`
		NewEdge string `json:"new_edge"`
		Reason  string `json:"reason"`
	} `json:"normalize"`
	Remove []struct {
		Node1  string `json:"node_1"`
		Node2  string `json:"node_2"`
		Edge   string `json:"edge"`
		Reason string `json:"reason"`
	} `json:"remove"`
	NewRelations []schema.RelationType `json:"new_relations"`
}
