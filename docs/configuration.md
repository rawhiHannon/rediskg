# Configuration Reference

RedisKG is configured through CLI flags, environment variables, and the
`Config` struct in `pkg/config/config.go`. This document covers every
configurable option, how precedence works, and provider-specific setup.

---

## Config Struct

The central configuration lives in `pkg/config/config.go`. All CLI flags
and environment variables map directly to fields on this struct.

```go
cfg := config.DefaultConfig()
```

### Complete Field Reference

| Field | Type | Default | CLI Flag | Description |
|---|---|---|---|---|
| `RedisAddr` | `string` | `localhost:6379` | `--redis` | Redis/FalkorDB server address |
| `GraphName` | `string` | `knowledge_graph` | `--graph` | Graph name in FalkorDB |
| `LLMProvider` | `string` | `openai` | `--llm` | LLM provider: `openai`, `claude`, `gemini`, `ollama` |
| `LLMModel` | `string` | `gpt-5.2` | `--model` | Model name for extraction and queries |
| `APIKey` | `string` | `""` | `--api-key` | OpenAI API key |
| `GeminiAPIKey` | `string` | `""` | `--gemini-key` | Google Gemini API key |
| `ClaudeAPIKey` | `string` | `""` | `--claude-key` | Anthropic Claude API key |
| `OllamaURL` | `string` | `http://localhost:11434` | `--ollama-url` | Ollama server URL |
| `EmbeddingProvider` | `string` | *(LLMProvider)* | -- | Embedding provider (defaults to `LLMProvider`) |
| `EmbeddingModel` | `string` | `text-embedding-3-small` | -- | Embedding model name |
| `EmbeddingDimension` | `int` | `1536` | -- | Vector embedding dimension |
| `ChunkSize` | `int` | `1500` | `--chunk-size` | Maximum characters per chunk |
| `ChunkOverlap` | `int` | `150` | `--chunk-overlap` | Character overlap between adjacent chunks |
| `ChunkStrategy` | `string` | `recursive` | `--chunk-strategy` | Chunking strategy (see below) |
| `Workers` | `int` | `8` | `--workers` | Concurrent extraction goroutines |
| `ExtractionStrategy` | `string` | `""` (llm) | `--extraction-strategy` | `llm` (2-pass LLM) or `hybrid` (built-in NER + LLM) |
| `NERServiceURL` | `string` | `""` | `--ner-url` | Optional external NER service URL (if empty, uses built-in Go NER) |
| `SemanticWeight` | `float64` | `4.0` | -- | Weight multiplier for LLM-extracted edges |
| `ProximityMinCount` | `int` | `3` | -- | Minimum co-occurrence count for proximity edges |
| `PersistSchema` | `bool` | `false` | -- | Save/load schema types between runs |
| `ResetSchemaOnIngest` | `bool` | `true` | -- | Start with fresh schema on each ingest |
| `GRPCPort` | `string` | `50051` | -- | gRPC server port |
| `HTTPPort` | `string` | `8081` | -- | HTTP REST server port |

---

## CLI Flags

All flags are passed to the `rediskg` binary before the command:

```bash
rediskg --redis 10.0.0.5:6379 --llm claude --model claude-sonnet-4-20250514 ingest ./docs/
```

### Global Flags

```
--redis          Redis address (default: localhost:6379)
--graph          Graph name in FalkorDB (default: knowledge_graph)
--llm            LLM provider: openai, claude, gemini, ollama (default: openai)
--model          Model name (default: gpt-5.2)
--api-key        OpenAI API key (overrides OPENAI_API_KEY env)
--gemini-key     Gemini API key (overrides GEMINI_API_KEY env)
--claude-key     Claude API key (overrides ANTHROPIC_API_KEY env)
--ollama-url     Ollama server URL (default: http://localhost:11434)
--workers        Concurrent extraction workers (default: 8)
--chunk-size     Characters per chunk (default: 1500)
--chunk-overlap  Overlap between chunks (default: 150)
--chunk-strategy        Chunking strategy (default: recursive)
--extraction-strategy   Extraction strategy: llm or hybrid (default: llm)
--ner-url               NER service URL for hybrid extraction (default: http://localhost:9000)
--falkordb-path         Path to falkordb.so module file (setup only)
```

### Flag Precedence

For API keys, the resolution order is:

1. CLI flag (`--api-key`, `--gemini-key`, `--claude-key`)
2. Primary environment variable (`OPENAI_API_KEY`, `GEMINI_API_KEY`, `ANTHROPIC_API_KEY`)
3. Fallback environment variable (`GPT_API_KEY`, `CLAUDE_API_KEY`)
4. `.env` file in the current directory

If a CLI flag is provided, it always wins. Otherwise, the binary reads
environment variables at startup.

---

## Environment Variables

RedisKG reads API keys from the environment when CLI flags are not set.
You can also use a `.env` file in the working directory -- it is loaded
automatically at startup.

| Variable | Maps To | Description |
|---|---|---|
| `OPENAI_API_KEY` | `Config.APIKey` | OpenAI API key |
| `GPT_API_KEY` | `Config.APIKey` | Fallback for OpenAI key |
| `GEMINI_API_KEY` | `Config.GeminiAPIKey` | Google Gemini API key |
| `ANTHROPIC_API_KEY` | `Config.ClaudeAPIKey` | Anthropic Claude API key |
| `CLAUDE_API_KEY` | `Config.ClaudeAPIKey` | Fallback for Claude key |

Example `.env` file:

```bash
OPENAI_API_KEY=sk-proj-...
GEMINI_API_KEY=AIza...
ANTHROPIC_API_KEY=sk-ant-...
```

---

## LLM Provider Setup

RedisKG supports four LLM providers. Each can be used for both extraction
and embedding, though you can mix providers (e.g., Claude for extraction,
OpenAI for embeddings) by setting `EmbeddingProvider` separately.

### OpenAI

```bash
export OPENAI_API_KEY="sk-proj-..."
rediskg --llm openai --model gpt-5.2 ingest ./docs/
```

OpenAI is the default provider. The embedding model defaults to
`text-embedding-3-small` (1536 dimensions). For higher quality embeddings,
switch to `text-embedding-3-large` and increase `EmbeddingDimension` to 3072.

### Anthropic Claude

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
rediskg --llm claude --model claude-sonnet-4-20250514 ingest ./docs/
```

Claude does not provide its own embedding API, so `EmbeddingProvider`
falls back to OpenAI when using Claude for extraction. Make sure
`OPENAI_API_KEY` is also set, or configure an alternative embedding provider.

### Google Gemini

```bash
export GEMINI_API_KEY="AIza..."
rediskg --llm gemini --model gemini-2.5-pro ingest ./docs/
```

Gemini provides both generation and embedding capabilities. The embedding
model defaults to `text-embedding-004` when the provider is set to `gemini`.

### Ollama (Local)

```bash
ollama pull llama3
rediskg --llm ollama --model llama3 --ollama-url http://localhost:11434 ingest ./docs/
```

No API key is required. Ollama runs models locally, so extraction speed
depends on your hardware. For embedding, Ollama uses the same model unless
`EmbeddingProvider` is set to a cloud provider.

---

## Chunking Configuration

Chunking controls how documents are split before LLM extraction. The three
parameters interact:

| Parameter | Flag | Effect |
|---|---|---|
| `ChunkSize` | `--chunk-size` | Larger chunks give the LLM more context but cost more tokens |
| `ChunkOverlap` | `--chunk-overlap` | Higher overlap reduces the chance of splitting entities across chunks |
| `ChunkStrategy` | `--chunk-strategy` | Controls the splitting algorithm |

### Available Strategies

| Strategy | Value | Best For |
|---|---|---|
| Recursive character | `recursive` | General-purpose text (default) |
| Sentence boundary | `sentence` | Prose-heavy documents |
| Structural (heading) | `structural` | Markdown, technical docs with headings |
| Contextual (LLM) | `contextual` | Maximum retrieval quality, higher cost |

Example with structural chunking for a markdown knowledge base:

```bash
rediskg ingest ./wiki/ \
  --chunk-strategy structural \
  --chunk-size 2000 \
  --chunk-overlap 200
```

See [Strategy Reference](strategies.md) for implementation details on each
chunking strategy.

---

## Extraction Configuration

RedisKG supports two extraction strategies:

| Strategy | Value | LLM Calls/Chunk | Best For |
|----------|-------|-----------------|----------|
| LLM (default) | `llm` | 2 | Highest quality, domain-specific entities |
| Hybrid NER+LLM | `hybrid` | 1 | Cost-sensitive, standard entity types |

### Hybrid Extraction Setup

The hybrid strategy works out of the box with a built-in Go rule-based
NER engine -- no external services, no Python, no model downloads:

```bash
# Just add the flag
rediskg --extraction-strategy hybrid ingest ./data/
```

For higher accuracy on specialized domains, you can optionally point to
an external NER service (GLiNER, spaCy, etc.):

```bash
pip install flask gliner
python scripts/ner_service.py --port 9000 --backend gliner
rediskg --extraction-strategy hybrid --ner-url http://localhost:9000 ingest ./data/
```

When `--ner-url` is not set, the built-in NER is used automatically.
Any HTTP service that implements the `POST /ner` protocol works -- see
[Extraction](extraction.md#hybrid-ner--llm-extraction) for the protocol spec.

The web UI includes a dropdown to switch strategies per-ingest without
restarting the server. The API also accepts `extraction_strategy` and
`ner_service_url` fields in the ingest request body.

---

## Performance Tuning

### Workers

The `--workers` flag controls how many chunks are processed concurrently
during extraction. Each worker makes independent LLM API calls.

```bash
# High-throughput ingestion with 16 concurrent workers
rediskg ingest ./large-corpus/ --workers 16
```

Guidelines:

| Corpus Size | Recommended Workers | Notes |
|---|---|---|
| < 10 files | 4 | Avoid hitting rate limits |
| 10-100 files | 8 | Default, good balance |
| 100+ files | 12-16 | Check your API rate limits first |
| Ollama (local) | 1-2 | Limited by GPU memory |

### Chunk Size

Larger chunks give the LLM more surrounding context for extraction, but
increase token cost and may cause the model to miss fine-grained entities.

| Chunk Size | Trade-off |
|---|---|
| 500-1000 | More chunks, finer granularity, higher API call count |
| 1500 | Default balance between context and cost |
| 2000-3000 | Fewer chunks, broader context, risk of entity dilution |

### Semantic Weight

`SemanticWeight` (default `4.0`) controls how much LLM-extracted edges are
weighted relative to proximity-based edges during graph construction. Higher
values favor precision (LLM-verified relationships); lower values let
statistical co-occurrence edges contribute more.

### Proximity Min Count

`ProximityMinCount` (default `3`) sets the minimum number of times two
entities must co-occur across chunks before a proximity edge is created.
Raise this for noisy corpora to reduce spurious connections.

---

## Schema Persistence

Two boolean flags control whether RedisKG remembers its learned schema
between runs:

| Flag | Default | Effect |
|---|---|---|
| `PersistSchema` | `false` | When `true`, saves accepted entity/relation types to FalkorDB as `__Schema__` nodes |
| `ResetSchemaOnIngest` | `true` | When `true`, ignores persisted schema and starts fresh each ingest |

To build up a schema incrementally across multiple ingestions:

```go
cfg.PersistSchema = true
cfg.ResetSchemaOnIngest = false
```

This is useful when ingesting a corpus in batches where you want the schema
governance layer to accumulate domain knowledge across runs.

---

## Server Configuration

RedisKG exposes both gRPC and HTTP REST interfaces when running in server
mode.

```bash
rediskg serve --llm openai --model gpt-5.2 --graph my_graph
```

| Port | Default | Protocol | Description |
|---|---|---|---|
| `GRPCPort` | `50051` | gRPC | Protobuf-based API for programmatic clients |
| `HTTPPort` | `8081` | HTTP | REST API with JSON payloads |

Both servers start simultaneously when `serve` is invoked. The HTTP server
exposes endpoints under `/api` (see [Getting Started](getting-started.md)
for the full endpoint list).

---

## Embedding Configuration

By default, the embedding provider mirrors the LLM provider. To use a
different provider for embeddings (common when using Claude or Ollama for
extraction but wanting OpenAI embeddings):

```go
cfg.LLMProvider = "claude"
cfg.EmbeddingProvider = "openai"      // Override for embeddings only
cfg.EmbeddingModel = "text-embedding-3-small"
cfg.EmbeddingDimension = 1536
```

The `EmbeddingDimension` must match the model's output dimension. Mismatched
dimensions will cause FalkorDB vector index creation to fail.

| Model | Provider | Dimensions |
|---|---|---|
| `text-embedding-3-small` | OpenAI | 1536 |
| `text-embedding-3-large` | OpenAI | 3072 |
| `text-embedding-004` | Gemini | 768 |
| Model-dependent | Ollama | Varies |

---

## Programmatic Configuration

When using RedisKG as a library rather than a CLI tool, create and modify
the config struct directly:

```go
package main

import (
    "rediskg/internal/pipeline"
    "rediskg/pkg/config"
)

func main() {
    cfg := config.DefaultConfig()
    cfg.LLMProvider = "claude"
    cfg.ClaudeAPIKey = "sk-ant-..."
    cfg.GraphName = "my_project"
    cfg.Workers = 12
    cfg.ChunkStrategy = "structural"
    cfg.PersistSchema = true
    cfg.ResetSchemaOnIngest = false

    p := pipeline.New(cfg)
    p.Ingest("/path/to/documents")
}
```

All fields on `Config` are exported, so any value can be set before passing
the config to `pipeline.New`.
