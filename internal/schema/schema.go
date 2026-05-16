package schema

import (
	"fmt"
	"strings"
	"sync"
)

// EntityType represents a discovered entity type in the schema.
type EntityType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ParentType  string `json:"parent_type,omitempty"` // optional hierarchy (e.g. "hospital" → "organization")
}

// RelationType represents a discovered relation type with its constraints.
type RelationType struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceTypes []string `json:"source_types"` // allowed source entity types
	TargetTypes []string `json:"target_types"` // allowed target entity types
	Symmetric   bool     `json:"symmetric"`    // if true, direction doesn't matter
}

// Schema holds the dynamically-built knowledge graph schema.
// It is populated by the LLM during ingestion and persisted in FalkorDB.
type Schema struct {
	mu            sync.RWMutex
	EntityTypes   map[string]*EntityType   // type name → definition
	RelationTypes map[string]*RelationType // relation name → definition
}

// New creates an empty schema.
func New() *Schema {
	return &Schema{
		EntityTypes:   map[string]*EntityType{},
		RelationTypes: map[string]*RelationType{},
	}
}

// AddEntityType registers a new entity type (or updates an existing one).
func (s *Schema) AddEntityType(et EntityType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	et.Name = strings.ToLower(strings.TrimSpace(et.Name))
	if et.Name == "" {
		return
	}
	s.EntityTypes[et.Name] = &et
}

// AddRelationType registers a new relation type (or updates an existing one).
func (s *Schema) AddRelationType(rt RelationType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.Name = strings.ToUpper(strings.TrimSpace(rt.Name))
	if rt.Name == "" {
		return
	}
	// Normalize source/target types to lowercase
	for i := range rt.SourceTypes {
		rt.SourceTypes[i] = strings.ToLower(rt.SourceTypes[i])
	}
	for i := range rt.TargetTypes {
		rt.TargetTypes[i] = strings.ToLower(rt.TargetTypes[i])
	}
	s.RelationTypes[rt.Name] = &rt
}

// HasEntityType checks if a type is known.
func (s *Schema) HasEntityType(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.EntityTypes[strings.ToLower(name)]
	return ok
}

// HasRelationType checks if a relation is known.
func (s *Schema) HasRelationType(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.RelationTypes[strings.ToUpper(name)]
	return ok
}

// GetRelationType returns a relation type definition or nil.
func (s *Schema) GetRelationType(name string) *RelationType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.RelationTypes[strings.ToUpper(name)]
}

// GetEntityType returns an entity type definition or nil.
func (s *Schema) GetEntityType(name string) *EntityType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.EntityTypes[strings.ToLower(name)]
}

// EntityTypeNames returns all known entity type names.
func (s *Schema) EntityTypeNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.EntityTypes))
	for n := range s.EntityTypes {
		names = append(names, n)
	}
	return names
}

// RelationTypeNames returns all known relation type names.
func (s *Schema) RelationTypeNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.RelationTypes))
	for n := range s.RelationTypes {
		names = append(names, n)
	}
	return names
}

// EntityTypeSummary returns a formatted summary of all entity types for use in LLM prompts.
func (s *Schema) EntityTypeSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.EntityTypes) == 0 {
		return "(no types defined yet — you may create new types as needed)"
	}
	var sb strings.Builder
	for _, et := range s.EntityTypes {
		if et.Description != "" {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", et.Name, et.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s\n", et.Name))
		}
	}
	return sb.String()
}

// RelationTypeSummary returns a formatted summary of all relation types for LLM prompts.
// Keeps output concise to avoid prompt bloat.
func (s *Schema) RelationTypeSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.RelationTypes) == 0 {
		return "(no relations defined yet — you may create new relation types as needed)"
	}
	var sb strings.Builder
	count := 0
	for _, rt := range s.RelationTypes {
		if count >= 30 {
			sb.WriteString(fmt.Sprintf("... and %d more\n", len(s.RelationTypes)-30))
			break
		}
		src := strings.Join(rt.SourceTypes, "|")
		tgt := strings.Join(rt.TargetTypes, "|")
		if rt.Description != "" {
			sb.WriteString(fmt.Sprintf("- %s (%s → %s): %s\n", rt.Name, src, tgt, rt.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s (%s → %s)\n", rt.Name, src, tgt))
		}
		count++
	}
	return sb.String()
}

// ValidateTripleDirection checks if a triple's entity types match the relation's constraints.
// Returns: "ok", "flip" (should swap source/target), or "invalid".
// Note: This is intentionally permissive — it only rejects when flipping would perfectly
// fix the direction. Unknown type combinations are allowed through (the LLM verification
// phase handles complex cases).
func (s *Schema) ValidateTripleDirection(relName, sourceType, targetType string) string {
	s.mu.RLock()
	rt := s.RelationTypes[strings.ToUpper(relName)]
	s.mu.RUnlock()

	if rt == nil {
		return "ok" // unknown relation, allow through
	}

	// If types are missing, allow through
	if sourceType == "" || targetType == "" {
		return "ok"
	}

	sourceOK := containsType(rt.SourceTypes, sourceType) || s.IsTypeCompatible(sourceType, rt.SourceTypes)
	targetOK := containsType(rt.TargetTypes, targetType) || s.IsTypeCompatible(targetType, rt.TargetTypes)

	if sourceOK && targetOK {
		return "ok"
	}

	// Check if flipping would fix it (both source and target match when swapped)
	flipSourceOK := containsType(rt.SourceTypes, targetType) || s.IsTypeCompatible(targetType, rt.SourceTypes)
	flipTargetOK := containsType(rt.TargetTypes, sourceType) || s.IsTypeCompatible(sourceType, rt.TargetTypes)
	if flipSourceOK && flipTargetOK {
		return "flip"
	}

	// If at least one side matches, allow through — let the LLM verification handle it
	if sourceOK || targetOK {
		return "ok"
	}

	// If the relation type has very few observed types (< 3 each), be permissive
	// since we haven't seen enough data to enforce strict constraints
	if len(rt.SourceTypes) < 3 || len(rt.TargetTypes) < 3 {
		return "ok"
	}

	return "invalid"
}

// IsTypeCompatible checks if entityType is compatible with one of the allowed types,
// considering type hierarchy (parent types).
func (s *Schema) IsTypeCompatible(entityType string, allowed []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entityType = strings.ToLower(entityType)
	for _, a := range allowed {
		if a == entityType {
			return true
		}
	}
	// Check parent type
	if et, ok := s.EntityTypes[entityType]; ok && et.ParentType != "" {
		for _, a := range allowed {
			if a == et.ParentType {
				return true
			}
		}
	}
	return false
}

func containsType(allowed []string, entityType string) bool {
	entityType = strings.ToLower(entityType)
	for _, a := range allowed {
		if strings.ToLower(a) == entityType {
			return true
		}
	}
	return false
}
