package models

// Document represents a loaded document before chunking.
type Document struct {
	Content  string
	Source   string
	Metadata map[string]string
}

// Chunk represents a text chunk with its ID and source metadata.
type Chunk struct {
	ID       string
	Text     string
	Source   string
	Index    int
	Metadata map[string]string
}

// Triple represents an extracted relationship: node1 -[edge]-> node2.
type Triple struct {
	Node1     string `json:"node_1"`
	Node1Type string `json:"node_1_type,omitempty"`
	Node2     string `json:"node_2"`
	Node2Type string `json:"node_2_type,omitempty"`
	Edge      string `json:"edge"`
	Evidence  string `json:"evidence,omitempty"`  // source text that supports this triple
	ChunkID   string `json:"chunk_id,omitempty"`
	Weight    float64
}

// Entity represents an extracted entity with its properties.
type Entity struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`                  // resolved type (base_type or domain_type)
	BaseType   string                 `json:"base_type,omitempty"`   // universal scaffold type
	DomainType string                 `json:"domain_type,omitempty"` // domain-specific subtype
	Properties map[string]interface{} `json:"properties,omitempty"`
	Embedding  []float32              `json:"embedding,omitempty"`
}

// EntityProfile holds the global merged view of an entity across all chunks.
type EntityProfile struct {
	Name           string   // canonical name
	CandidateTypes []string // all types proposed across chunks
	Mentions       []string // evidence sentences mentioning this entity
	Description    string   // merged description
	BaseType       string   // resolved base type
	DomainType     string   // resolved domain type
	Confidence     float64  // type confidence
}

// CandidateRelation holds a proposed relation before normalization.
type CandidateRelation struct {
	Name       string   // relation name as extracted
	Aliases    []string // other names that mean the same thing
	SourceBase []string // allowed source base types
	TargetBase []string // allowed target base types
	Direction  string   // "forward", "inverse", or "symmetric"
}

// GraphData holds the complete extraction result from a chunk.
type GraphData struct {
	Triples  []Triple `json:"triples"`
	Entities []Entity `json:"entities,omitempty"`
}

// EdgeRecord is the merged representation of an edge in the final graph.
type EdgeRecord struct {
	Node1     string
	Node1Type string
	Node2     string
	Node2Type string
	Edge      string   // relationship description(s)
	ChunkIDs  []string // source chunk IDs
	Weight    float64  // combined weight
	Inferred  bool     // true if from proximity/inference, false if LLM-extracted
}

// Community represents a detected community/cluster of nodes.
type Community struct {
	ID    int
	Nodes []string
	Color string
}

// QueryResult holds the response to a natural language query.
type QueryResult struct {
	Answer   string                   `json:"answer"`
	Entities []map[string]interface{} `json:"entities"`
	Edges    []map[string]interface{} `json:"edges"`
	Cypher   string                   `json:"cypher"`
}
