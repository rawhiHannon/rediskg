package schema

import (
	"strings"
)

// GovernanceResult holds the outcome of a schema governance check.
type GovernanceResult struct {
	Decision      string  // "accept_new", "synonym", "subtype", "inverse", "too_vague", "invalid"
	CanonicalName string  // the canonical name to use (if synonym/subtype/inverse)
	Confidence    float64 // how certain the decision is
	NeedsLLM     bool    // true if the governance layer cannot decide without LLM help
}

// CheckProposedType checks whether a proposed entity type should be accepted,
// mapped to an existing type, or flagged for LLM review.
// Returns a governance decision without calling the LLM.
func (s *Schema) CheckProposedType(proposed string) GovernanceResult {
	proposed = strings.ToLower(strings.TrimSpace(proposed))
	if proposed == "" {
		return GovernanceResult{Decision: "invalid", Confidence: 1.0}
	}

	// Exact match — already exists
	if s.HasEntityType(proposed) {
		return GovernanceResult{Decision: "synonym", CanonicalName: s.ResolveTypeName(proposed), Confidence: 1.0}
	}

	// Check alias index
	s.mu.RLock()
	if canonical, ok := s.typeAliases[proposed]; ok {
		s.mu.RUnlock()
		return GovernanceResult{Decision: "synonym", CanonicalName: canonical, Confidence: 1.0}
	}
	s.mu.RUnlock()

	// Heuristic: check if it's a word-order variation of an existing type
	if canonical := s.findWordOrderVariant(proposed); canonical != "" {
		return GovernanceResult{Decision: "synonym", CanonicalName: canonical, Confidence: 0.85, NeedsLLM: true}
	}

	// Heuristic: check if it shares a significant token overlap with an existing type
	if canonical, score := s.findTokenOverlap(proposed); canonical != "" && score >= 0.7 {
		return GovernanceResult{Decision: "synonym", CanonicalName: canonical, Confidence: score, NeedsLLM: true}
	}

	// Too vague checks
	if isTooVague(proposed) {
		return GovernanceResult{Decision: "too_vague", Confidence: 0.8, NeedsLLM: true}
	}

	// Cannot determine without LLM
	return GovernanceResult{Decision: "accept_new", Confidence: 0.5, NeedsLLM: true}
}

// CheckProposedRelation checks whether a proposed relation should be accepted,
// mapped to an existing relation, or flagged for LLM review.
func (s *Schema) CheckProposedRelation(proposed string) GovernanceResult {
	proposed = strings.ToUpper(strings.TrimSpace(proposed))
	if proposed == "" {
		return GovernanceResult{Decision: "invalid", Confidence: 1.0}
	}

	// Exact match
	if s.HasRelationType(proposed) {
		return GovernanceResult{Decision: "synonym", CanonicalName: s.ResolveRelationName(proposed), Confidence: 1.0}
	}

	// Check alias index
	s.mu.RLock()
	if canonical, ok := s.relationAliases[proposed]; ok {
		s.mu.RUnlock()
		return GovernanceResult{Decision: "synonym", CanonicalName: canonical, Confidence: 1.0}
	}
	s.mu.RUnlock()

	// Check for inverse pattern (e.g., MANAGED_BY → MANAGES)
	if canonical := s.findInverseRelation(proposed); canonical != "" {
		return GovernanceResult{Decision: "inverse", CanonicalName: canonical, Confidence: 0.8, NeedsLLM: true}
	}

	// Check word-order variant
	if canonical := s.findRelationWordOrderVariant(proposed); canonical != "" {
		return GovernanceResult{Decision: "synonym", CanonicalName: canonical, Confidence: 0.8, NeedsLLM: true}
	}

	// Too verbose (4+ words)
	words := strings.Split(proposed, "_")
	if len(words) > 3 {
		return GovernanceResult{Decision: "too_vague", Confidence: 0.7, NeedsLLM: true}
	}

	return GovernanceResult{Decision: "accept_new", Confidence: 0.5, NeedsLLM: true}
}

// ApproveType moves a candidate type into the accepted schema.
func (s *Schema) ApproveType(candidate CandidateType) {
	switch candidate.Decision {
	case "synonym":
		if candidate.CanonicalName != "" {
			s.RegisterTypeAlias(candidate.ProposedName, candidate.CanonicalName)
		}
	case "subtype":
		if candidate.CanonicalName != "" {
			// Register as a domain type under the canonical parent
			s.RegisterDomainType(candidate.ProposedName, candidate.Evidence, candidate.CanonicalName)
		}
	case "new":
		bases := candidate.ProposedBases
		if len(bases) == 0 {
			bases = []string{"concept"}
		}
		if len(bases) == 1 {
			s.RegisterDomainType(candidate.ProposedName, candidate.Evidence, bases[0])
		} else {
			s.RegisterDomainTypeMultiBase(candidate.ProposedName, candidate.Evidence, bases)
		}
	}
}

// ApproveRelation moves a candidate relation into the accepted schema.
func (s *Schema) ApproveRelation(candidate CandidateRelation) {
	switch candidate.Decision {
	case "synonym":
		if candidate.CanonicalName != "" {
			s.RegisterRelationAlias(candidate.ProposedName, candidate.CanonicalName, false)
		}
	case "inverse":
		if candidate.CanonicalName != "" {
			s.RegisterRelationAlias(candidate.ProposedName, candidate.CanonicalName, true)
		}
	case "new":
		s.AddRelationType(RelationType{
			Name:        candidate.ProposedName,
			SourceTypes: candidate.SourceBaseTypes,
			TargetTypes: candidate.TargetBaseTypes,
			Symmetric:   candidate.Symmetric,
		})
	}
}

// NormalizeTripleRelation resolves a relation name through the alias index,
// and indicates if the triple direction should be flipped.
func (s *Schema) NormalizeTripleRelation(relName string) (canonical string, shouldFlip bool) {
	relName = strings.ToUpper(strings.TrimSpace(relName))

	// Check if it's already canonical
	s.mu.RLock()
	if _, ok := s.RelationTypes[relName]; ok {
		s.mu.RUnlock()
		return relName, false
	}

	// Check alias
	if c, ok := s.relationAliases[relName]; ok {
		// Determine if this is an inverse alias
		rt := s.RelationTypes[c]
		s.mu.RUnlock()
		if rt != nil && strings.ToUpper(rt.InverseOf) == relName {
			return c, true
		}
		return c, false
	}
	s.mu.RUnlock()

	return relName, false
}

// NormalizeEntityType resolves an entity type through the alias index.
func (s *Schema) NormalizeEntityType(typeName string) string {
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if typeName == "" {
		return ""
	}
	return s.ResolveTypeName(typeName)
}

// --- Heuristic helpers ---

// findWordOrderVariant checks if the proposed type is a word-order rearrangement
// of an existing type (e.g., "clinic_branch" vs "branch_clinic").
func (s *Schema) findWordOrderVariant(proposed string) string {
	proposedParts := strings.Split(proposed, "_")
	if len(proposedParts) < 2 {
		return ""
	}
	proposedSet := toSet(proposedParts)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for name := range s.EntityTypes {
		parts := strings.Split(name, "_")
		if len(parts) != len(proposedParts) {
			continue
		}
		if setsEqual(proposedSet, toSet(parts)) {
			return name
		}
	}
	return ""
}

// findTokenOverlap checks if the proposed type shares significant tokens with an existing type.
// Returns the best match and a similarity score (0-1).
func (s *Schema) findTokenOverlap(proposed string) (string, float64) {
	proposedTokens := tokenize(proposed)
	if len(proposedTokens) == 0 {
		return "", 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	bestMatch := ""
	bestScore := 0.0

	for name := range s.EntityTypes {
		existingTokens := tokenize(name)
		if len(existingTokens) == 0 {
			continue
		}
		score := jaccardSimilarity(proposedTokens, existingTokens)
		if score > bestScore {
			bestScore = score
			bestMatch = name
		}
	}

	return bestMatch, bestScore
}

// findInverseRelation checks common inverse patterns (e.g., _BY suffix).
func (s *Schema) findInverseRelation(proposed string) string {
	// Check "_BY" suffix → look for base verb
	if strings.HasSuffix(proposed, "_BY") {
		base := strings.TrimSuffix(proposed, "_BY")
		// Try common active forms
		candidates := []string{base + "S", base, base + "ES"}
		s.mu.RLock()
		defer s.mu.RUnlock()
		for _, c := range candidates {
			if _, ok := s.RelationTypes[c]; ok {
				return c
			}
		}
		return ""
	}

	// Check if adding "_BY" gives an existing relation (active → passive)
	withBy := proposed + "_BY"
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.RelationTypes[withBy]; ok {
		return withBy
	}

	return ""
}

// findRelationWordOrderVariant checks if a relation is a word-reordering of existing.
func (s *Schema) findRelationWordOrderVariant(proposed string) string {
	proposedParts := strings.Split(proposed, "_")
	if len(proposedParts) < 2 {
		return ""
	}
	proposedSet := toSet(proposedParts)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for name := range s.RelationTypes {
		parts := strings.Split(name, "_")
		if len(parts) != len(proposedParts) {
			continue
		}
		if setsEqual(proposedSet, toSet(parts)) {
			return name
		}
	}
	return ""
}

// isTooVague returns true for types that are too generic to be useful as domain types.
var vagueSuffixes = []string{"thing", "stuff", "item", "entity", "object", "other", "misc"}

func isTooVague(proposed string) bool {
	parts := strings.Split(proposed, "_")
	if len(parts) == 1 {
		for _, v := range vagueSuffixes {
			if proposed == v {
				return true
			}
		}
	}
	return false
}

// --- String utility helpers ---

func tokenize(s string) []string {
	return strings.Split(strings.ToLower(s), "_")
}

func toSet(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[strings.ToLower(s)] = true
	}
	return m
}

func setsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func jaccardSimilarity(a, b []string) float64 {
	setA := toSet(a)
	setB := toSet(b)
	intersection := 0
	for k := range setA {
		if setB[k] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
