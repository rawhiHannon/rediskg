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
	ChunkSize    int
	ChunkOverlap int

	// Extraction
	Workers           int     // concurrent extraction goroutines
	SemanticWeight    float64 // W1 for LLM-extracted edges
	ProximityMinCount int     // minimum co-occurrence to keep proximity edge

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
		GRPCPort:           "50051",
		HTTPPort:           "8081",
	}
}
