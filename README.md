# RedisKG

A Go microservice and CLI for building production-quality knowledge graphs from documents using [FalkorDB](https://www.falkordb.com/) and LLMs.

## Key Features

- **14-phase ingestion pipeline** with schema governance, hard constraints, edge conflict resolution, and evidence tracking
- **Multi-path retrieval** with 6 retrieval signals: fulltext, vector, edge-fact vector, MENTIONED_IN traversal, 2-hop expansion
- **Dynamic schema governance** -- three-layer trust model (base types, candidate types, accepted types) with LLM-assisted approval
- **5 pluggable strategy interfaces** -- Chunker, Resolver, Canonicalizer, Extractor, Reranker
- **2 extraction strategies** -- LLM-only (2-pass, highest quality) or Hybrid NER+LLM (built-in Go NER + LLM, 50% fewer API calls, zero setup)
- **4 chunking strategies** -- recursive character, sentence-boundary, structural/heading, contextual (LLM-prefixed)
- **3-tier entity resolution** -- exact match, semantic cosine similarity, LLM verification with Union-Find clustering
- **Typed relationship edges** -- native Cypher pattern matching (`MATCH (a)-[:MANAGES]->(b)`) with per-relation-type vector indexes
- **13 hard constraint rules** -- domain-aware semantic validation before graph materialization
- **Crash-safe incremental updates** -- `__pending__` document pattern with atomic cutover and crash recovery
- **Real-time telemetry** -- SSE streaming of pipeline progress with per-phase timing
- **Circuit breaker** -- exponential backoff with jitter on FalkorDB connections
- **Multi-LLM support** -- OpenAI, Claude, Gemini, Ollama
- **Single binary** -- Go, no Python dependencies, CLI + REST API

## Quick Start

### Prerequisites

- Go 1.21+
- Redis 8.0+ with [FalkorDB](https://github.com/FalkorDB/FalkorDB) module (v4.18.7+)
- An LLM API key (OpenAI, Claude, Gemini) or Ollama running locally

### Install

```bash
git clone https://github.com/your-org/rediskg.git
cd rediskg
go build ./cmd/rediskg
```

### Start FalkorDB

```bash
redis-server --loadmodule /path/to/falkordb.so
```

### Ingest Documents

```bash
# Ingest a single file
export OPENAI_API_KEY=sk-...
./rediskg ingest ./data/report.txt

# Ingest a directory
./rediskg ingest ./data/

# Ingest with custom settings
./rediskg --llm claude --claude-key sk-ant-... --chunk-strategy sentence ingest ./data/
```

### Hybrid NER Extraction (saves ~50% LLM costs)

```bash
# Just add the flag — built-in NER works out of the box, no setup needed
./rediskg --extraction-strategy hybrid ingest ./data/
```

The hybrid strategy uses a built-in Go rule-based NER engine for entity extraction (free, instant), then sends only the verification + relationship extraction to the LLM. No external services, no Python, no model downloads. The web UI also has a dropdown to switch strategies per-ingest.

For higher NER accuracy, you can optionally point to an external NER service (GLiNER, spaCy, etc.):

```bash
pip install flask gliner
python scripts/ner_service.py --port 9000 --backend gliner
./rediskg --extraction-strategy hybrid --ner-url http://localhost:9000 ingest ./data/
```

### Query

```bash
# Structured query (returns entities + graph)
./rediskg query "Who manages the New York office?"

# Interactive multi-turn chat
./rediskg chat
```

### REST API

```bash
# Start the server
./rediskg serve

# Ingest via API
curl -X POST http://localhost:8081/api/ingest \
  -H "Content-Type: application/json" \
  -d '{"text": "Acme Corp was founded by John Smith in 2020.", "source": "example"}'

# Query via API
curl -X POST http://localhost:8081/api/query \
  -H "Content-Type: application/json" \
  -d '{"question": "Who founded Acme Corp?", "human": true}'

# Watch pipeline progress (SSE)
curl -N http://localhost:8081/api/pipeline/events
```

## Architecture

```
Documents ──► Chunking ──► Coreference Resolution ──► LLM Extraction
     │
     ▼
Entity Resolution ──► Canonicalization ──► Edge Rewriting
     │
     ▼
Negation Fix ──► Conditional Annotation ──► Status-Aware Rewriting
     │
     ▼
Alternative Groups ──► Hard Constraints ──► Global Graph Selection
     │
     ▼
Post-Solver Validation ──► Conflict Resolution ──► Inverse Derivation
     │
     ▼
Materialization (batched UNWIND) ──► Embedding Generation
     │
     ▼
FalkorDB (graph + vector + fulltext indexes)
```

## CLI Reference

| Command | Description |
|---------|-------------|
| `rediskg ingest <path>` | Ingest a file or directory |
| `rediskg query "<question>"` | Query the knowledge graph |
| `rediskg chat` | Interactive multi-turn chat |
| `rediskg update <path>` | Update a document incrementally |
| `rediskg delete-doc <id>` | Delete a document |
| `rediskg finalize` | Global dedup + embedding backfill |
| `rediskg serve` | Start HTTP REST server |
| `rediskg stats` | Show graph statistics |

## REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/ingest` | Ingest text or file |
| POST | `/api/query` | Query the graph |
| POST | `/api/chat` | Multi-turn chat |
| PUT | `/api/document` | Update a document |
| DELETE | `/api/document` | Delete a document |
| POST | `/api/finalize` | Finalize (dedup + embeddings) |
| GET | `/api/stats` | Graph statistics |
| GET | `/api/graph` | Graph data for visualization |
| GET | `/api/export` | Export full graph as JSON |
| POST | `/api/cypher` | Execute raw Cypher |
| GET | `/api/pipeline/stats` | Pipeline telemetry snapshot |
| GET | `/api/pipeline/events` | SSE real-time progress stream |
| DELETE | `/api/graph` | Delete entire graph |

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--redis` | `localhost:6379` | Redis/FalkorDB address |
| `--graph` | `knowledge_graph` | Graph name |
| `--llm` | `openai` | LLM provider (openai, claude, gemini, ollama) |
| `--model` | `gpt-4o` | LLM model name |
| `--api-key` | | OpenAI API key |
| `--claude-key` | | Claude API key |
| `--gemini-key` | | Gemini API key |
| `--ollama-url` | `http://localhost:11434` | Ollama URL |
| `--workers` | `8` | Concurrent extraction workers |
| `--chunk-size` | `1500` | Chunk size in characters |
| `--chunk-overlap` | `150` | Overlap between chunks |
| `--chunk-strategy` | `recursive` | Chunking strategy (recursive, sentence, structural, contextual) |
| `--extraction-strategy` | `llm` | Extraction strategy: `llm` (2-pass LLM) or `hybrid` (local NER + LLM) |
| `--ner-url` | `http://localhost:9000` | NER service URL for hybrid extraction |

Environment variables: `OPENAI_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Step-by-step tutorial |
| [Architecture](docs/architecture.md) | System design and data flow |
| [Ingestion Pipeline](docs/ingestion.md) | 18-phase pipeline walkthrough |
| [Extraction](docs/extraction.md) | Entity and relation extraction |
| [Retrieval](docs/retrieval.md) | Multi-path retrieval system |
| [Schema Governance](docs/schema-governance.md) | Three-layer type governance |
| [Configuration](docs/configuration.md) | Full configuration reference |
| [Strategies](docs/strategies.md) | Pluggable strategy interfaces |
| [Storage](docs/storage.md) | FalkorDB storage internals |
| [Graph Schema](docs/graph-schema.md) | Knowledge graph structure |
| [API Reference](docs/api-reference.md) | Complete CLI and REST API reference |
| [Telemetry](docs/telemetry.md) | Pipeline observability and SSE streaming |

## Project Structure

```
cmd/rediskg/          CLI entry point
internal/
  chunker/            4 chunking strategies
  llm/                Multi-provider LLM client
  loader/             Document loaders (txt, pdf, docx, md)
  pipeline/           Core pipeline (ingest, query, chat, update, telemetry)
  schema/             Schema governance (base types, governance, aliases)
  server/             HTTP REST server
  setup/              FalkorDB setup helpers
  solver/             Hard constraints + graph selection
  store/              FalkorDB client + circuit breaker
pkg/
  config/             Configuration
  models/             Data models (Entity, Edge, Chunk, etc.)
```

## License

MIT
