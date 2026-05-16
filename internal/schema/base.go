package schema

// BaseTypeConfig defines a single upper-ontology type used as scaffolding.
// The base type layer is configurable — not a fixed ontology.
type BaseTypeConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DefaultBaseTypes provides a reasonable default upper ontology scaffold.
// This is NOT a fixed list — it can be extended or replaced via configuration.
func DefaultBaseTypes() []BaseTypeConfig {
	return []BaseTypeConfig{
		{Name: "person", Description: "A named individual (any profession, role, or identity)"},
		{Name: "organization", Description: "A company, institution, agency, network, or formal group"},
		{Name: "location", Description: "A geographic place: city, region, country, neighborhood"},
		{Name: "address", Description: "A specific street address or postal location"},
		{Name: "physical_object", Description: "A tangible object, device, tool, or material"},
		{Name: "product", Description: "Something manufactured, sold, or traded"},
		{Name: "service", Description: "Something offered, provided, performed, or sold"},
		{Name: "event", Description: "A named incident, meeting, occurrence, or time-bound happening"},
		{Name: "technology", Description: "Software, system, platform, portal, or digital tool"},
		{Name: "document", Description: "A named document, policy, agreement, report, or record"},
		{Name: "role", Description: "A job title, function, or position held by a person"},
		{Name: "quantity", Description: "A measurable amount, metric, or numeric value with units"},
		{Name: "date_time", Description: "A specific date, time, duration, or temporal reference"},
		{Name: "law_or_policy", Description: "A regulation, law, legal rule, or official policy"},
		{Name: "concept", Description: "An abstract topic, rule, category, or principle (fallback)"},
	}
}

// BaseTypeNames returns just the names from a base type config list.
func BaseTypeNames(types []BaseTypeConfig) []string {
	names := make([]string, len(types))
	for i, bt := range types {
		names[i] = bt.Name
	}
	return names
}

// IsBaseType returns true if the given type is in the schema's base type layer.
func (s *Schema) IsBaseType(t string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.baseTypes[t]
	return ok
}

// BaseTypeList returns all configured base type names.
func (s *Schema) BaseTypeList() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.baseTypes))
	for n := range s.baseTypes {
		names = append(names, n)
	}
	return names
}

// BaseTypeSummary returns a formatted description of all base types for LLM prompts.
func (s *Schema) BaseTypeSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var sb []string
	for name, desc := range s.baseTypes {
		sb = append(sb, "- "+name+": "+desc)
	}
	return joinLines(sb)
}

// InitWithBaseTypes seeds the schema with the configured base types.
// If no custom base types were set via SetBaseTypes, uses DefaultBaseTypes.
func (s *Schema) InitWithBaseTypes() {
	s.mu.Lock()
	if len(s.baseTypes) == 0 {
		for _, bt := range DefaultBaseTypes() {
			s.baseTypes[bt.Name] = bt.Description
		}
	}
	s.mu.Unlock()

	for name, desc := range s.baseTypes {
		s.AddEntityType(EntityType{
			Name:        name,
			Description: desc,
			ParentType:  "",
			DomainType:  false,
		})
	}
}

// SetBaseTypes replaces the base type scaffold with a custom set.
// Must be called before InitWithBaseTypes.
func (s *Schema) SetBaseTypes(types []BaseTypeConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseTypes = map[string]string{}
	for _, bt := range types {
		s.baseTypes[bt.Name] = bt.Description
	}
}

// ResolveBaseType determines which base type a domain type belongs to.
// Walks up the parent chain. Returns "concept" as fallback.
func (s *Schema) ResolveBaseType(domainType string) string {
	if s.IsBaseType(domainType) {
		return domainType
	}

	visited := map[string]bool{}
	current := domainType
	for {
		if visited[current] {
			break
		}
		visited[current] = true

		et := s.GetEntityType(current)
		if et == nil || et.ParentType == "" {
			break
		}
		if s.IsBaseType(et.ParentType) {
			return et.ParentType
		}
		current = et.ParentType
	}

	return "concept"
}

// RegisterDomainType registers a new accepted domain-specific type under base type(s).
func (s *Schema) RegisterDomainType(name, description, baseType string) {
	if !s.IsBaseType(baseType) {
		baseType = "concept"
	}
	s.AddEntityType(EntityType{
		Name:        name,
		Description: description,
		ParentType:  baseType,
		BaseTypes:   []string{baseType},
		DomainType:  true,
	})
}

// RegisterDomainTypeMultiBase registers a domain type that maps to multiple base types.
func (s *Schema) RegisterDomainTypeMultiBase(name, description string, baseTypes []string) {
	validBases := make([]string, 0, len(baseTypes))
	for _, bt := range baseTypes {
		if s.IsBaseType(bt) {
			validBases = append(validBases, bt)
		}
	}
	if len(validBases) == 0 {
		validBases = []string{"concept"}
	}
	s.AddEntityType(EntityType{
		Name:        name,
		Description: description,
		ParentType:  validBases[0], // primary parent is first
		BaseTypes:   validBases,
		DomainType:  true,
	})
}

func joinLines(lines []string) string {
	result := ""
	for _, l := range lines {
		result += l + "\n"
	}
	return result
}
