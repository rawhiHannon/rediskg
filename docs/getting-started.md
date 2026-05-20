# Getting Started with RedisKG

RedisKG is a Go microservice and CLI tool that builds and queries knowledge graphs
from your documents. It uses FalkorDB — a Redis module that provides both graph
and vector storage — combined with large language models to extract entities,
relationships, and meaning from unstructured text.

This guide walks you through installation, your first ingestion, querying,
and using the REST API.

---

## Prerequisites

Before you begin, make sure you have the following installed:

| Requirement | Version |
|---|---|
| **Go** | 1.21 or later |
| **Redis** | 8.0 or later |
| **FalkorDB module** | v4.18.7 or later |
| **LLM API key** | OpenAI, Claude, Gemini, or a local Ollama instance |

You can verify your Go installation with:

```bash
go version
# go version go1.21.0 linux/amd64
```

And your Redis version with:

```bash
redis-server --version
# Redis server v=8.0.0 ...
```

## Installation

Clone the repository and build the binary:

```bash
git clone https://github.com/your-org/rediskg.git
cd rediskg
go build ./cmd/rediskg
```

This produces a `rediskg` binary in the current directory. You can move it
to a location on your `PATH` for convenience:

```bash
sudo mv rediskg /usr/local/bin/
```

Verify the build:

```bash
rediskg --help
```

## Starting FalkorDB

RedisKG requires a running Redis instance with the FalkorDB module loaded.
Start Redis with the module:

```bash
redis-server --loadmodule /path/to/falkordb.so
```

If you installed FalkorDB system-wide (e.g., to `/etc/redis/falkordb.so`),
you can add it to your Redis configuration file instead:

```
# /etc/redis/redis.conf
loadmodule /etc/redis/falkordb.so
```

Then start Redis normally:

```bash
redis-server /etc/redis/redis.conf
```

Confirm FalkorDB is loaded by connecting with `redis-cli`:

```bash
redis-cli
127.0.0.1:6379> MODULE LIST
# You should see "graph" in the output
```

### Using Docker (alternative)

If you prefer Docker, FalkorDB provides an official image:

```bash
docker run -d --name falkordb -p 6379:6379 falkordb/falkordb:latest
```

## Configure Your LLM

RedisKG needs an LLM to extract entities and relationships from text. Set your
API key as an environment variable:

**OpenAI:**

```bash
export OPENAI_API_KEY="sk-..."
```

**Google Gemini:**

```bash
export GEMINI_API_KEY="your-gemini-key"
```

**Anthropic Claude:**

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

**Ollama (local, no key needed):**

If you are running Ollama locally, no API key is required. You will pass the
`--llm ollama` and `--ollama-url` flags instead.

## Your First Ingestion

Let's ingest a document and build a knowledge graph. Create a sample text file:

```bash
cat > sample.txt << 'EOF'
Marie Curie was a Polish-French physicist and chemist who conducted
pioneering research on radioactivity. She was the first woman to win
a Nobel Prize, the first person to win a Nobel Prize twice, and the
only person to win a Nobel Prize in two scientific fields. She was
born in Warsaw, Poland, in 1867 and later moved to Paris, France,
where she studied at the University of Paris. Together with her
husband Pierre Curie, she discovered the elements polonium and radium.
EOF
```

Now ingest it:

```bash
rediskg ingest sample.txt \
  --llm openai \
  --model gpt-4o \
  --graph my_first_graph
```

RedisKG will:

1. Chunk the document into manageable segments.
2. Send each chunk to the LLM to extract entities and relationships.
3. Run schema governance to validate and normalize types.
4. Store everything in FalkorDB as a knowledge graph with vector embeddings.

You will see progress output as each pipeline phase completes.

### Ingesting a directory

You can also point RedisKG at an entire directory. It will recursively process
all supported files:

```bash
rediskg ingest ./documents/ \
  --llm openai \
  --model gpt-4o \
  --workers 8
```

The `--workers` flag controls how many chunks are processed concurrently
(default: 8).

## Querying the Knowledge Graph

Once data is ingested, query it with natural language:

```bash
rediskg query "What elements did Marie Curie discover?" \
  --llm openai \
  --model gpt-4o \
  --graph my_first_graph
```

RedisKG combines graph traversal with vector similarity search to find the
most relevant subgraph, then uses the LLM to synthesize an answer grounded
in your data.

### Graph statistics

To see what is in your graph:

```bash
rediskg stats --graph my_first_graph
```

This shows entity counts, relationship counts, and type distributions.

## Multi-Turn Chat

For an interactive session where context carries across questions, use the
chat command:

```bash
rediskg chat \
  --llm openai \
  --model gpt-4o \
  --graph my_first_graph
```

This opens an interactive prompt:

```
RedisKG Chat (type 'exit' to quit)
> Who was Marie Curie?
Marie Curie was a Polish-French physicist and chemist...

> Where did she study?
She studied at the University of Paris in France...

> What did she discover with her husband?
Together with Pierre Curie, she discovered polonium and radium...

> exit
```

Each follow-up question is interpreted in the context of the conversation
so far, so you can use pronouns and references naturally.

## Using the REST API

Start the HTTP server:

```bash
rediskg serve \
  --llm openai \
  --model gpt-4o \
  --graph my_first_graph
```

The server starts on port 8080 by default. All endpoints are under `/api`.

### Ingest via API

```bash
curl -X POST http://localhost:8080/api/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "text": "Albert Einstein developed the theory of general relativity in 1915 while working in Berlin, Germany."
  }'
```

You can also pass a file path on the server's filesystem:

```bash
curl -X POST http://localhost:8080/api/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "path": "/path/to/document.txt"
  }'
```

### Query via API

```bash
curl -X POST http://localhost:8080/api/query \
  -H "Content-Type: application/json" \
  -d '{
    "question": "What did Einstein develop?"
  }'
```

### Multi-turn chat via API

```bash
curl -X POST http://localhost:8080/api/chat \
  -H "Content-Type: application/json" \
  -d '{
    "message": "Tell me about Marie Curie",
    "session_id": "my-session-1"
  }'
```

Pass the same `session_id` to maintain conversation context across requests.

### Update a document

```bash
curl -X PUT http://localhost:8080/api/document \
  -H "Content-Type: application/json" \
  -d '{
    "path": "/path/to/updated-document.txt"
  }'
```

### Delete a document

```bash
curl -X DELETE http://localhost:8080/api/document \
  -H "Content-Type: application/json" \
  -d '{
    "id": "document-id"
  }'
```

### Finalize the graph

Run global deduplication and backfill any missing embeddings:

```bash
curl -X POST http://localhost:8080/api/finalize
```

### Graph statistics

```bash
curl http://localhost:8080/api/stats
```

### Export the full graph

```bash
curl http://localhost:8080/api/export
```

### Real-time pipeline progress

Monitor ingestion progress via Server-Sent Events:

```bash
curl http://localhost:8080/api/pipeline/events
```

### Delete the entire graph

```bash
curl -X DELETE http://localhost:8080/api/graph
```

## Tuning Chunking

RedisKG splits documents into chunks before sending them to the LLM. You can
control this behavior with three flags:

```bash
rediskg ingest large-document.txt \
  --chunk-size 2000 \
  --chunk-overlap 200 \
  --chunk-strategy contextual
```

| Flag | Default | Description |
|---|---|---|
| `--chunk-size` | 1500 | Maximum characters per chunk |
| `--chunk-overlap` | 150 | Overlap between adjacent chunks |
| `--chunk-strategy` | recursive | Splitting strategy |

Available chunk strategies:

- **recursive** — Split on paragraph, then sentence, then word boundaries (default).
- **sentence** — Split strictly on sentence boundaries.
- **structural** — Split on structural markers (headings, sections).
- **contextual** — LLM-assisted splitting that preserves semantic coherence.

## CLI Reference

Here is a summary of all commands and common flags:

### Commands

| Command | Description |
|---|---|
| `rediskg ingest <path>` | Ingest a file or directory |
| `rediskg query "<question>"` | Query the knowledge graph |
| `rediskg chat` | Interactive multi-turn chat |
| `rediskg update <path>` | Incrementally update a document |
| `rediskg delete-doc <id>` | Delete a document and its graph data |
| `rediskg finalize` | Run global dedup and embedding backfill |
| `rediskg serve` | Start the HTTP REST server |
| `rediskg stats` | Show graph statistics |

### Global flags

| Flag | Default | Description |
|---|---|---|
| `--redis` | `localhost:6379` | Redis server address |
| `--graph` | `knowledge_graph` | Graph name in FalkorDB |
| `--llm` | `openai` | LLM provider (openai, gemini, claude, ollama) |
| `--model` | — | Model name (e.g., gpt-4o, gemini-pro) |
| `--api-key` | — | OpenAI API key (or use OPENAI_API_KEY env var) |
| `--gemini-key` | — | Gemini API key (or use GEMINI_API_KEY env var) |
| `--claude-key` | — | Claude API key (or use ANTHROPIC_API_KEY env var) |
| `--ollama-url` | — | Ollama server URL |
| `--workers` | `8` | Concurrent processing workers |
| `--chunk-size` | `1500` | Characters per chunk |
| `--chunk-overlap` | `150` | Overlap between chunks |
| `--chunk-strategy` | `recursive` | Chunking strategy |

## Next Steps

Now that you have a working knowledge graph, here are some things to explore:

- **[Schema Governance](schema-governance.md)** — Learn how RedisKG validates
  and normalizes entity and relationship types through its three-layer trust model.
- **[Pipeline Architecture](pipeline.md)** — Understand the 13-phase pipeline
  that transforms raw text into a verified knowledge graph.
- **[Vector Search](vector-search.md)** — Dive into how FalkorDB vector indexes
  power semantic similarity queries alongside graph traversal.
- **[Multi-LLM Configuration](llm-config.md)** — Configure different LLM providers
  and models for different pipeline phases.
- **[Production Deployment](deployment.md)** — Run RedisKG as a service with
  the gRPC and REST interfaces.
