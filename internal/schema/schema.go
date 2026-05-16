package schema

import (
	"fmt"
	"strings"
	"sync"
)

// EntityType represents an accepted entity type in the schema.
type EntityType struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ParentType  string   `json:"parent_type,omitempty"`  // parent in type hierarchy
	BaseTypes   []string `json:"base_types,omitempty"`   // which base types this maps to (multi-base support)
	Aliases     []string `json:"aliases,omitempty"`      // known synonyms that map to this canonical type
	DomainType  bool     `json:"domain_type,omitempty"`  // true if this is a domain-specific type (not base)
}

// RelationType represents an accepted relation type with its constraints.
type RelationType struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SourceTypes []string `json:"source_types"` // allowed source entity types
	TargetTypes []string `json:"target_types"` // allowed target entity types
	Symmetric   bool     `json:"symmetric"`    // if true, direction doesn't matter
	Aliases     []string `json:"aliases,omitempty"` // known synonyms mapped to this canonical
	InverseOf   string   `json:"inverse_of,omitempty"` // if set, this is the canonical; the inverse maps here
}

// CandidateType represents a proposed type not yet accepted into the schema.
type CandidateType struct {
	ProposedName  string   `json:"proposed_name"`
	ProposedBases []string `json:"proposed_base_types"`
	Evidence      string   `json:"evidence"`
	Decision      string   `json:"decision"` // "new", "synonym", "subtype", "too_vague", "invalid"
	CanonicalName string   `json:"canonical_name,omitempty"` // if synonym/subtype, the canonical it maps to
	Confidence    float64  `json:"confidence"`
}

// CandidateRelation represents a proposed relation not yet accepted into the schema.
type CandidateRelation struct {
	ProposedName       string   `json:"proposed_name"`
	Decision           string   `json:"decision"` // "new", "synonym", "inverse", "too_vague", "invalid"
	CanonicalName      string   `json:"canonical_name,omitempty"`
	CanonicalDirection string   `json:"canonical_direction,omitempty"` // "source -> target" description
	SourceBaseTypes    []string `json:"source_base_types"`
	TargetBaseTypes    []string `json:"target_base_types"`
	Symmetric          bool     `json:"symmetric"`
	Confidence         float64  `json:"confidence"`
}

// Schema holds the dynamically-built knowledge graph schema with governance layers.
type Schema struct {
	mu            sync.RWMutex
	baseTypes     map[string]string       // base type name → description (configurable scaffold)
	EntityTypes   map[string]*EntityType  // accepted type name → definition
	RelationTypes map[string]*RelationType // accepted relation name → definition

	// Alias indexes for fast lookup during normalization
	typeAliases     map[string]string // alias → canonical type name
	relationAliases map[string]string // alias → canonical relation name

	// Candidate queues (proposed but not yet accepted)
	pendingTypes     []CandidateType
	pendingRelations []CandidateRelation
}

// New creates an empty schema.
func New() *Schema {
	return &Schema{
		baseTypes:       map[string]string{},
		EntityTypes:     map[string]*EntityType{},
		RelationTypes:   map[string]*RelationType{},
		typeAliases:     map[string]string{},
		relationAliases: map[string]string{},
	}
}

// AddEntityType registers an accepted entity type (or updates an existing one).
func (s *Schema) AddEntityType(et EntityType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	et.Name = strings.ToLower(strings.TrimSpace(et.Name))
	if et.Name == "" {
		return
	}
	s.EntityTypes[et.Name] = &et
	// Register aliases in the index
	for _, alias := range et.Aliases {
		s.typeAliases[strings.ToLower(alias)] = et.Name
	}
}

// AddRelationType registers an accepted relation type (or updates an existing one).
func (s *Schema) AddRelationType(rt RelationType) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.Name = strings.ToUpper(strings.TrimSpace(rt.Name))
	if rt.Name == "" {
		return
	}
	for i := range rt.SourceTypes {
		rt.SourceTypes[i] = strings.ToLower(rt.SourceTypes[i])
	}
	for i := range rt.TargetTypes {
		rt.TargetTypes[i] = strings.ToLower(rt.TargetTypes[i])
	}
	s.RelationTypes[rt.Name] = &rt
	// Register aliases
	for _, alias := range rt.Aliases {
		s.relationAliases[strings.ToUpper(alias)] = rt.Name
	}
	if rt.InverseOf != "" {
		s.relationAliases[strings.ToUpper(rt.InverseOf)] = rt.Name
	}
}

// HasEntityType checks if a type is in the accepted schema (or is a known alias).
func (s *Schema) HasEntityType(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToLower(name)
	if _, ok := s.EntityTypes[name]; ok {
		return true
	}
	_, ok := s.typeAliases[name]
	return ok
}

// HasRelationType checks if a relation is in the accepted schema (or is a known alias).
func (s *Schema) HasRelationType(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToUpper(name)
	if _, ok := s.RelationTypes[name]; ok {
		return true
	}
	_, ok := s.relationAliases[name]
	return ok
}

// GetRelationType returns a relation type definition or nil.
// Resolves aliases to the canonical relation.
func (s *Schema) GetRelationType(name string) *RelationType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToUpper(name)
	if rt, ok := s.RelationTypes[name]; ok {
		return rt
	}
	if canonical, ok := s.relationAliases[name]; ok {
		return s.RelationTypes[canonical]
	}
	return nil
}

// GetEntityType returns an entity type definition or nil.
// Resolves aliases to the canonical type.
func (s *Schema) GetEntityType(name string) *EntityType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToLower(name)
	if et, ok := s.EntityTypes[name]; ok {
		return et
	}
	if canonical, ok := s.typeAliases[name]; ok {
		return s.EntityTypes[canonical]
	}
	return nil
}

// ResolveTypeName returns the canonical type name for a given name.
// If the name is an alias, returns the canonical. Otherwise returns the name itself.
func (s *Schema) ResolveTypeName(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToLower(name)
	if _, ok := s.EntityTypes[name]; ok {
		return name
	}
	if canonical, ok := s.typeAliases[name]; ok {
		return canonical
	}
	return name
}

// ResolveRelationName returns the canonical relation name for a given name.
// If the name is an alias or inverse, returns the canonical.
func (s *Schema) ResolveRelationName(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToUpper(name)
	if _, ok := s.RelationTypes[name]; ok {
		return name
	}
	if canonical, ok := s.relationAliases[name]; ok {
		return canonical
	}
	return name
}

// IsRelationInverse returns true if the given relation name is an inverse alias
// (meaning the triple direction should be flipped).
func (s *Schema) IsRelationInverse(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	name = strings.ToUpper(name)
	if _, ok := s.RelationTypes[name]; ok {
		return false // it's the canonical, not inverse
	}
	if canonical, ok := s.relationAliases[name]; ok {
		rt := s.RelationTypes[canonical]
		if rt != nil && rt.InverseOf != "" && strings.ToUpper(rt.InverseOf) == name {
			return true
		}
		// Check if the alias is listed as inverse
		// Convention: aliases listed in InverseOf field are inverses
	}
	return false
}

// RegisterTypeAlias maps an alias to an existing canonical type.
func (s *Schema) RegisterTypeAlias(alias, canonical string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alias = strings.ToLower(alias)
	canonical = strings.ToLower(canonical)
	s.typeAliases[alias] = canonical
	// Also add to the canonical type's alias list
	if et, ok := s.EntityTypes[canonical]; ok {
		found := false
		for _, a := range et.Aliases {
			if a == alias {
				found = true
				break
			}
		}
		if !found {
			et.Aliases = append(et.Aliases, alias)
		}
	}
}

// RegisterRelationAlias maps an alias to an existing canonical relation.
func (s *Schema) RegisterRelationAlias(alias, canonical string, isInverse bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alias = strings.ToUpper(alias)
	canonical = strings.ToUpper(canonical)
	s.relationAliases[alias] = canonical
	if rt, ok := s.RelationTypes[canonical]; ok {
		found := false
		for _, a := range rt.Aliases {
			if a == alias {
				found = true
				break
			}
		}
		if !found {
			rt.Aliases = append(rt.Aliases, alias)
		}
		if isInverse && rt.InverseOf == "" {
			rt.InverseOf = alias
		}
	}
}

// EntityTypeNames returns all accepted entity type names.
func (s *Schema) EntityTypeNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.EntityTypes))
	for n := range s.EntityTypes {
		names = append(names, n)
	}
	return names
}

// RelationTypeNames returns all accepted relation type names.
func (s *Schema) RelationTypeNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.RelationTypes))
	for n := range s.RelationTypes {
		names = append(names, n)
	}
	return names
}

// DomainTypeNames returns only domain-specific (non-base) type names.
func (s *Schema) DomainTypeNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0)
	for n, et := range s.EntityTypes {
		if et.DomainType {
			names = append(names, n)
		}
	}
	return names
}

// TypeAliasMap returns a copy of the current type alias index.
func (s *Schema) TypeAliasMap() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]string, len(s.typeAliases))
	for k, v := range s.typeAliases {
		m[k] = v
	}
	return m
}

// RelationAliasMap returns a copy of the current relation alias index.
func (s *Schema) RelationAliasMap() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]string, len(s.relationAliases))
	for k, v := range s.relationAliases {
		m[k] = v
	}
	return m
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
			sb.WriteString(fmt.Sprintf("- %s: %s", et.Name, et.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s", et.Name))
		}
		if et.ParentType != "" {
			sb.WriteString(fmt.Sprintf(" [parent: %s]", et.ParentType))
		}
		if len(et.Aliases) > 0 {
			sb.WriteString(fmt.Sprintf(" (aliases: %s)", strings.Join(et.Aliases, ", ")))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// RelationTypeSummary returns a formatted summary of all relation types for LLM prompts.
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
			sb.WriteString(fmt.Sprintf("- %s (%s → %s): %s", rt.Name, src, tgt, rt.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- %s (%s → %s)", rt.Name, src, tgt))
		}
		if len(rt.Aliases) > 0 {
			sb.WriteString(fmt.Sprintf(" [aliases: %s]", strings.Join(rt.Aliases, ", ")))
		}
		if rt.Symmetric {
			sb.WriteString(" [symmetric]")
		}
		sb.WriteString("\n")
		count++
	}
	return sb.String()
}

// ValidateTripleDirection checks if a triple's entity types match the relation's constraints.
// Returns: "ok", "flip" (should swap source/target), or "invalid".
func (s *Schema) ValidateTripleDirection(relName, sourceType, targetType string) string {
	s.mu.RLock()
	rt := s.RelationTypes[strings.ToUpper(relName)]
	s.mu.RUnlock()

	if rt == nil {
		// Check alias resolution
		rt = s.GetRelationType(relName)
		if rt == nil {
			return "ok" // unknown relation, allow through
		}
	}

	if sourceType == "" || targetType == "" {
		return "ok"
	}

	sourceOK := containsType(rt.SourceTypes, sourceType) || s.IsTypeCompatible(sourceType, rt.SourceTypes)
	targetOK := containsType(rt.TargetTypes, targetType) || s.IsTypeCompatible(targetType, rt.TargetTypes)

	if sourceOK && targetOK {
		return "ok"
	}

	// Check if flipping would fix it
	flipSourceOK := containsType(rt.SourceTypes, targetType) || s.IsTypeCompatible(targetType, rt.SourceTypes)
	flipTargetOK := containsType(rt.TargetTypes, sourceType) || s.IsTypeCompatible(sourceType, rt.TargetTypes)
	if flipSourceOK && flipTargetOK {
		return "flip"
	}

	if sourceOK || targetOK {
		return "ok"
	}

	// Be permissive with few observations
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
	// Check via alias resolution
	if canonical, ok := s.typeAliases[entityType]; ok {
		for _, a := range allowed {
			if a == canonical {
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
