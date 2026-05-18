package models

// KGEntity is the final materialized entity in the knowledge graph.
type KGEntity struct {
	ID              string            `json:"id"`
	CanonicalName   string            `json:"canonical_name"`
	BaseTypes       []string          `json:"base_types"`
	DomainTypes     []string          `json:"domain_types"`
	FunctionalRoles []string          `json:"functional_roles,omitempty"`
	Status          string            `json:"status,omitempty"` // active, planned, inactive, former, prospective, unknown
	Labels          map[string]string `json:"labels,omitempty"` // lang -> label
	Aliases         []LangText        `json:"aliases,omitempty"`
	Properties      map[string]interface{} `json:"properties,omitempty"`
	Embedding       []float32         `json:"embedding,omitempty"`
}

// KGEdge is the final materialized edge in the knowledge graph.
type KGEdge struct {
	From       string        `json:"from"`        // canonical entity name
	RelationID string        `json:"relation_id"` // stable internal relation ID
	To         string        `json:"to"`          // canonical entity name
	Evidence   []EvidenceRef `json:"evidence"`
	Weight     float64       `json:"weight"`
	ChunkIDs   []string      `json:"chunk_ids"`
	Status     string        `json:"status,omitempty"`    // active, planned, backup, conditional
	Condition  string        `json:"condition,omitempty"` // conditional context
}

// LangText is a text string with its language.
type LangText struct {
	Text string `json:"text"`
	Lang string `json:"lang"`
}

// EvidenceRef references the source text supporting a fact.
type EvidenceRef struct {
	Text     string `json:"text"`
	Language string `json:"language"`
	ChunkID  string `json:"chunk_id"`
	Source   string `json:"source"`
}

// CandidateEntity is a proposed entity from extraction (before canonicalization).
type CandidateEntity struct {
	Mention         string              `json:"mention"`             // original text mention
	CanonicalName   string              `json:"canonical_candidate"` // proposed canonical name
	BaseTypes       []ScoredType        `json:"base_type_candidates"`
	DomainTypes     []ScoredType        `json:"domain_type_candidates"`
	FunctionalRoles []string            `json:"functional_roles,omitempty"`
	Status          string              `json:"status,omitempty"` // active, planned, inactive, former, prospective, unknown
	Aliases         []LangText          `json:"aliases,omitempty"`
	Evidence        []EvidenceRef       `json:"evidence"`
	ChunkID         string              `json:"chunk_id"`
}

// ScoredType is a type candidate with a confidence score.
type ScoredType struct {
	Type  string  `json:"type"`
	Score float64 `json:"score"`
}

// CandidateEdge is a proposed edge from extraction (before normalization/selection).
type CandidateEdge struct {
	ID                string  `json:"id"`
	FromMention       string  `json:"from_mention"`
	RelationRaw       string  `json:"relation_candidate"`      // LLM-generated name
	RelationID        string  `json:"relation_id_candidate"`   // normalized to internal ID (may be empty)
	ToMention         string  `json:"to_mention"`
	EvidenceText      string  `json:"evidence_text"`
	EvidenceLang      string  `json:"evidence_language"`
	ChunkID           string  `json:"chunk_id"`
	EvidenceScore     float64 `json:"evidence_score"`
	SchemaFitScore    float64 `json:"schema_fit_score"`
	Confidence        float64 `json:"confidence"`
	AlternativeGroup  string  `json:"alternative_group,omitempty"`
	Status            string  `json:"status,omitempty"`    // active, planned, backup, conditional
	Condition         string  `json:"condition,omitempty"` // conditional context (e.g., "during Al-Amal downtime")
}

// CandidateGraph holds all extraction output before canonicalization and solving.
type CandidateGraph struct {
	Entities []CandidateEntity `json:"entities"`
	Edges    []CandidateEdge   `json:"edges"`
}

// CanonicalEntity is an entity after alias resolution — ready for edge selection.
type CanonicalEntity struct {
	ID              string            `json:"id"`
	CanonicalName   string            `json:"canonical_name"`
	BaseTypes       []string          `json:"base_types"`
	DomainTypes     []string          `json:"domain_types"`
	FunctionalRoles []string          `json:"functional_roles,omitempty"`
	Status          string            `json:"status,omitempty"` // active, planned, inactive, former, prospective, unknown
	Labels          map[string]string `json:"labels,omitempty"`
	Aliases         []LangText        `json:"aliases,omitempty"`
	Evidence        []EvidenceRef     `json:"evidence"`
}

// HasRole checks if an entity has a specific functional role.
func (e *CanonicalEntity) HasRole(role string) bool {
	for _, r := range e.FunctionalRoles {
		if r == role {
			return true
		}
	}
	return false
}

// IsPlanned returns true if entity status is planned or has planned_unit role.
func (e *CanonicalEntity) IsPlanned() bool {
	return e.Status == "planned" || e.HasRole("planned_unit")
}

// FinalGraph is the output of the global graph selector — ready for materialization.
type FinalGraph struct {
	Entities []KGEntity `json:"entities"`
	Edges    []KGEdge   `json:"edges"`
}
