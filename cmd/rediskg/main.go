package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"rediskg/internal/llm"
	"rediskg/internal/pipeline"
	"rediskg/internal/server"
	"rediskg/internal/setup"
	"rediskg/internal/store"
	"rediskg/pkg/config"
)

func main() {
	cfg := config.DefaultConfig()

	// Global flags
	flag.StringVar(&cfg.RedisAddr, "redis", cfg.RedisAddr, "Redis address (host:port)")
	flag.StringVar(&cfg.GraphName, "graph", cfg.GraphName, "Graph name in FalkorDB")
	flag.StringVar(&cfg.LLMProvider, "llm", cfg.LLMProvider, "LLM provider (openai, gemini, claude, ollama)")
	flag.StringVar(&cfg.LLMModel, "model", cfg.LLMModel, "LLM model name")
	flag.StringVar(&cfg.APIKey, "api-key", cfg.APIKey, "OpenAI API key")
	flag.StringVar(&cfg.GeminiAPIKey, "gemini-key", cfg.GeminiAPIKey, "Gemini API key")
	flag.StringVar(&cfg.ClaudeAPIKey, "claude-key", cfg.ClaudeAPIKey, "Claude/Anthropic API key")
	flag.StringVar(&cfg.OllamaURL, "ollama-url", cfg.OllamaURL, "Ollama API URL")
	flag.IntVar(&cfg.Workers, "workers", cfg.Workers, "Number of concurrent extraction workers")
	flag.IntVar(&cfg.ChunkSize, "chunk-size", cfg.ChunkSize, "Text chunk size in characters")
	flag.IntVar(&cfg.ChunkOverlap, "chunk-overlap", cfg.ChunkOverlap, "Overlap between chunks")
	falkorDBPath := flag.String("falkordb-path", "", "Path to falkordb.so module file")

	flag.Parse()

	// Load .env file from current directory
	loadEnvFile(".env")

	// Read API keys: flag > env
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("GPT_API_KEY")
	}
	if cfg.GeminiAPIKey == "" {
		cfg.GeminiAPIKey = os.Getenv("GEMINI_API_KEY")
	}
	if cfg.ClaudeAPIKey == "" {
		cfg.ClaudeAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if cfg.ClaudeAPIKey == "" {
		cfg.ClaudeAPIKey = os.Getenv("CLAUDE_API_KEY")
	}
	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	command := args[0]

	switch command {
	case "setup":
		result, err := setup.Run(cfg.RedisAddr, *falkorDBPath)
		if err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		fmt.Printf("Setup complete! Redis %s, FalkorDB module loaded\n", result.RedisVersion)

	case "ingest":
		requireAPIKey(cfg)
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: rediskg ingest <path>")
			os.Exit(1)
		}
		runIngest(cfg, args[1])

	case "query":
		requireAPIKey(cfg)
		if len(args) < 2 {
			runInteractiveQuery(cfg)
		} else {
			runQuery(cfg, strings.Join(args[1:], " "))
		}

	case "cypher":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: rediskg cypher <query>")
			os.Exit(1)
		}
		runCypher(cfg, strings.Join(args[1:], " "))

	case "stats":
		runStats(cfg)

	case "serve":
		requireAPIKey(cfg)
		srv, err := server.New(cfg)
		if err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
		log.Fatal(srv.Start())

	case "delete":
		runDelete(cfg)

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func requireAPIKey(cfg *config.Config) {
	switch cfg.LLMProvider {
	case "ollama":
		return
	case "gemini":
		if cfg.GeminiAPIKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --gemini-key or GEMINI_API_KEY environment variable required")
			os.Exit(1)
		}
	case "claude":
		if cfg.ClaudeAPIKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --claude-key or ANTHROPIC_API_KEY environment variable required")
			os.Exit(1)
		}
	default: // openai
		if cfg.APIKey == "" {
			fmt.Fprintln(os.Stderr, "Error: --api-key or OPENAI_API_KEY environment variable required")
			os.Exit(1)
		}
	}
}

func printUsage() {
	fmt.Println(`rediskg - Knowledge Graph builder powered by LLMs and FalkorDB

Commands:
  setup              Check and install Redis + FalkorDB dependencies
  serve              Start HTTP server with web UI and REST API
  ingest <path>      Load documents from file/directory and build the graph
  query [question]   Ask a natural language question (interactive mode if no question)
  cypher <query>     Execute a raw Cypher query
  stats              Show graph statistics
  delete             Delete the entire graph

Flags:`)
	flag.PrintDefaults()
}

func createPipeline(cfg *config.Config) *pipeline.Pipeline {
	s, err := store.New(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to FalkorDB: %v", err)
	}

	llmClient := llm.NewClient(cfg)
	return pipeline.New(cfg, s, llmClient)
}

func runIngest(cfg *config.Config, path string) {
	p := createPipeline(cfg)

	info, err := os.Stat(path)
	if err != nil {
		log.Fatalf("Cannot access %s: %v", path, err)
	}

	if info.IsDir() {
		err = p.IngestDir(path)
	} else {
		err = p.IngestRawText(readFile(path), path)
	}

	if err != nil {
		log.Fatalf("Ingestion failed: %v", err)
	}
}

func runQuery(cfg *config.Config, question string) {
	p := createPipeline(cfg)

	result, err := p.Query(question)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	fmt.Println(result.Answer)
	if result.Cypher != "" {
		fmt.Printf("\n[Cypher: %s]\n", result.Cypher)
	}
}

func runInteractiveQuery(cfg *config.Config) {
	p := createPipeline(cfg)
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Println("Interactive query mode. Type 'exit' to quit.")
	for {
		fmt.Print("\nQuestion: ")
		if !scanner.Scan() {
			break
		}
		question := strings.TrimSpace(scanner.Text())
		if question == "" {
			continue
		}
		if question == "exit" || question == "quit" {
			break
		}

		result, err := p.Query(question)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		fmt.Printf("\n%s\n", result.Answer)
		if result.Cypher != "" {
			fmt.Printf("\n[Cypher: %s]\n", result.Cypher)
		}
	}
}

func runCypher(cfg *config.Config, cypher string) {
	p := createPipeline(cfg)

	result, err := p.QueryCypher(cypher)
	if err != nil {
		log.Fatalf("Cypher query failed: %v", err)
	}

	fmt.Printf("%v\n", result)
}

func runStats(cfg *config.Config) {
	s, err := store.New(cfg)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	stats, err := s.GetGraphStats()
	if err != nil {
		log.Fatalf("Failed to get stats: %v", err)
	}

	fmt.Printf("Graph: %s\n", cfg.GraphName)
	fmt.Printf("Nodes: %d\n", stats["nodes"])
	fmt.Printf("Edges: %d\n", stats["edges"])
}

func runDelete(cfg *config.Config) {
	s, err := store.New(cfg)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}

	if err := s.DeleteGraph(); err != nil {
		log.Fatalf("Failed to delete graph: %v", err)
	}
	fmt.Printf("Graph '%s' deleted.\n", cfg.GraphName)
}

func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read file %s: %v", path, err)
	}
	return string(data)
}

// loadEnvFile reads a .env file and sets environment variables (won't overwrite existing).
func loadEnvFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // .env is optional
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// Don't overwrite existing env vars
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
