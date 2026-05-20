package config

type Config struct {
	// Redis / FalkorDB
	RedisAddr string
	GraphName string

	// LLM
	LLMProvider  string // "openai", "claude", "gemini", "ollama"
	LLMModel     string
	APIKey       string // OpenAI API key (also used as fallback)
	GeminiAPIKey string // Gemini API key
	ClaudeAPIKey string // Claude/Anthropic API key
	OllamaURL    string // default http://localhost:11434

	// Embedding
	EmbeddingProvider  string // defaults to LLMProvider
	EmbeddingModel     string
	EmbeddingDimension int

	// Chunking
	ChunkSize     int
	ChunkOverlap  int
	ChunkStrategy string // "recursive" (default), "sentence", "structural", "contextual"

	// Extraction
	Workers            int     // concurrent extraction goroutines
	SemanticWeight     float64 // W1 for LLM-extracted edges
	ProximityMinCount  int     // minimum co-occurrence to keep proximity edge
	ExtractionStrategy string  // "llm" (default) or "hybrid" (local NER + LLM)
	NERServiceURL      string  // URL for local NER service (GLiNER/spaCy), used when strategy=hybrid

	// Schema
	PersistSchema      bool // whether to save/load schema between runs
	ResetSchemaOnIngest bool // if true, start fresh each ingest (ignore persisted)

	// Server
	GRPCPort string
	HTTPPort string
}

func DefaultConfig() *Config {
	return &Config{
		RedisAddr:          "localhost:6379",
		GraphName:          "knowledge_graph",
		LLMProvider:        "openai",
		LLMModel:           "gpt-5.2",
		OllamaURL:          "http://localhost:11434",
		EmbeddingModel:     "text-embedding-3-small",
		EmbeddingDimension: 1536,
		ChunkSize:          1500,
		ChunkOverlap:       150,
		Workers:            8,
		SemanticWeight:     4.0,
		ProximityMinCount:  3,
		PersistSchema:      false, // off by default until quality stabilizes
		ResetSchemaOnIngest: true,
		GRPCPort:           "50051",
		HTTPPort:           "8081",
	}
}
