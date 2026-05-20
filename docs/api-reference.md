# RedisKG API Reference

Complete reference for CLI commands, REST endpoints, and Go package API.

---

## CLI Commands

RedisKG provides a single binary with subcommands for ingestion, querying, and management.

### `rediskg ingest <path>`

Ingest a file or directory into the knowledge graph.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name in FalkorDB |
| `--concurrency` | int | `4` | Parallel extraction workers |
| `--chunk-size` | int | `1500` | Target chunk size in tokens |
| `--overlap` | int | `200` | Chunk overlap in tokens |
| `--provider` | string | `"openai"` | LLM provider (`openai`, `claude`, `gemini`, `ollama`) |
| `--model` | string | `""` | Model name override |

**Examples:**

```bash
rediskg ingest ./docs/report.pdf
rediskg ingest ./corpus/ --concurrency 8 --provider claude
```

---

### `rediskg query "<question>"`

Query the knowledge graph with a natural-language question.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--human` | bool | `false` | Return a human-readable narrative answer |
| `--graph` | string | `"rediskg"` | Graph name |
| `--top-k` | int | `10` | Number of vector-similarity results |

**Examples:**

```bash
rediskg query "Who founded Acme Corp?"
rediskg query "What are the main risks?" --human
```

---

### `rediskg chat`

Start an interactive multi-turn chat session. Maintains conversation history
and rewrites follow-up questions for better graph retrieval.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name |
| `--human` | bool | `true` | Human-readable answers |

---

### `rediskg update <path>`

Incrementally update a previously ingested document. Detects changed content,
re-extracts affected chunks, and merges new triples into the graph.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name |

---

### `rediskg delete-doc <id>`

Delete a document and all entities/edges exclusive to it. Shared entities
(referenced by other documents) are preserved.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name |

---

### `rediskg finalize`

Run global deduplication across all documents and backfill any missing
embeddings. Recommended after batch ingestion.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name |

---

### `rediskg serve`

Start the HTTP REST server.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--addr` | string | `":8080"` | Listen address |
| `--graph` | string | `"rediskg"` | Graph name |

---

### `rediskg stats`

Print node and edge counts for the current graph.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--graph` | string | `"rediskg"` | Graph name |
| `--json` | bool | `false` | Output as JSON |

---

## REST API Endpoints

All endpoints are served by `rediskg serve`. Request and response bodies use JSON.

---

### POST /api/ingest

Ingest text or a file into the knowledge graph.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `text` | string | one of `text` or `path` | Raw text content to ingest |
| `source` | string | no | Source label for the document |
| `path` | string | one of `text` or `path` | Server-local file or directory path |

```json
{"text": "Acme Corp was founded in 1985 by Jane Doe.", "source": "acme-overview"}
```

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"ok"` |
| `nodes` | int | Number of nodes created or merged |
| `edges` | int | Number of edges created or merged |

```json
{"status": "ok", "nodes": 12, "edges": 18}
```

---

### POST /api/query

Query the knowledge graph with a natural-language question.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `question` | string | yes | Natural-language question |
| `human` | bool | no | If `true`, return a narrative answer |

```json
{"question": "Who founded Acme Corp?", "human": true}
```

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `answer` | string | Human-readable answer (when `human` is `true`) |
| `graph` | object | Subgraph of matched nodes and edges |
| `entities` | array | List of matched entity names |
| `cypher` | string | Generated Cypher query |

```json
{
  "answer": "Acme Corp was founded by Jane Doe in 1985.",
  "graph": {"nodes": [...], "edges": [...]},
  "entities": ["Acme Corp", "Jane Doe"],
  "cypher": "MATCH (p:Person)-[:FOUNDED]->(o:Organization) ..."
}
```

---

### POST /api/chat

Multi-turn conversational query. Accepts conversation history and optionally
rewrites the question to resolve coreferences.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `question` | string | yes | Current user question |
| `history` | array | no | Prior messages: `[{"role": "user", "content": "..."}, ...]` |
| `rewrite_question` | bool | no | Rewrite question using history for better retrieval |

```json
{
  "question": "What products do they sell?",
  "history": [
    {"role": "user", "content": "Tell me about Acme Corp"},
    {"role": "assistant", "content": "Acme Corp is a manufacturing company..."}
  ],
  "rewrite_question": true
}
```

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `answer` | string | Narrative answer |
| `graph` | object | Subgraph of matched nodes and edges |
| `facts` | array | Supporting facts extracted from the graph |
| `entities` | array | Matched entity names |

```json
{
  "answer": "Acme Corp sells industrial widgets and safety equipment.",
  "graph": {"nodes": [...], "edges": [...]},
  "facts": ["Acme Corp PRODUCES Widget X", "Acme Corp PRODUCES Safety Helmet"],
  "entities": ["Acme Corp", "Widget X", "Safety Helmet"]
}
```

---

### PUT /api/document

Update a previously ingested document incrementally.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `text` | string | one of `text` or `path` | Updated text content |
| `source` | string | no | Source label |
| `path` | string | one of `text` or `path` | Server-local file path |

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"updated"` |
| `document` | string | Document identifier |

```json
{"status": "updated", "document": "acme-overview"}
```

---

### DELETE /api/document

Delete a document and its exclusive entities from the graph.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `document_id` | string | yes | Document identifier |

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"deleted"` |
| `document` | string | Document identifier |

```json
{"status": "deleted", "document": "acme-overview"}
```

---

### POST /api/finalize

Run global deduplication and embedding backfill across the entire graph.

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"finalized"` |
| `nodes` | int | Total node count after finalization |
| `edges` | int | Total edge count after finalization |

```json
{"status": "finalized", "nodes": 48, "edges": 67}
```

---

### GET /api/stats

Return node and edge counts for the graph.

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `graph` | string | Graph name |
| `nodes` | int | Total node count |
| `edges` | int | Total edge count |

```json
{"graph": "rediskg", "nodes": 48, "edges": 67}
```

---

### GET /api/graph

Retrieve paginated nodes and edges from the graph.

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `limit` | int | `500` | Maximum items to return |
| `offset` | int | `0` | Pagination offset |
| `node` | string | `""` | Filter by node name (substring match) |

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `nodes` | array | Node objects with `id`, `name`, `type`, `properties` |
| `edges` | array | Edge objects with `source`, `target`, `relation`, `properties` |
| `total` | int | Total count before pagination |
| `hasMore` | bool | Whether more results exist beyond this page |

```json
{
  "nodes": [{"id": "n1", "name": "Acme Corp", "type": "Organization"}],
  "edges": [{"source": "n2", "target": "n1", "relation": "FOUNDED"}],
  "total": 120,
  "hasMore": true
}
```

---

### GET /api/export

Export the full graph as JSON with all node and edge properties.

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `download` | int | `0` | If `1`, set `Content-Disposition` header for file download |

**Response:** Full graph JSON containing all nodes and edges with their properties.

---

### POST /api/cypher

Execute a raw Cypher query against FalkorDB.

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `query` | string | yes | Cypher query string |

```json
{"query": "MATCH (n) RETURN n LIMIT 10"}
```

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `result` | any | Raw query result from FalkorDB |

---

### GET /api/pipeline/stats

Return a JSON snapshot of the current or most recent pipeline run.
See [Telemetry](telemetry.md) for the full response schema.

**Response:** `PipelineStats` JSON object.

---

### GET /api/pipeline/events

Server-Sent Events stream for real-time pipeline progress.
See [Telemetry](telemetry.md) for event types and usage.

**Events:** `snapshot`, `progress`, `done`.

---

### DELETE /api/graph

Delete the entire graph and all its data.

**Response:**

| Field | Type | Description |
|-------|------|-------------|
| `status` | string | `"deleted"` |

```json
{"status": "deleted"}
```

---

## Go Package API

### Pipeline (`internal/pipeline`)

Constructor and core methods on `*Pipeline`.

| Method | Signature | Description |
|--------|-----------|-------------|
| `New` | `New(cfg *config.Config, store *falkor.FalkorStore, llmClient llm.Client) *Pipeline` | Create a new pipeline instance |
| `Ingest` | `Ingest(docs []document.Document) error` | Run the full ingestion pipeline on a slice of documents |
| `IngestDir` | `IngestDir(path string) error` | Discover and ingest all supported files under a directory |
| `IngestRawText` | `IngestRawText(text, source string) error` | Ingest a raw text string with a source label |
| `Query` | `Query(question string, humanAnswer bool) (*QueryResult, error)` | Query the graph; optionally generate a narrative answer |
| `Chat` | `Chat(req *ChatRequest) (*QueryResult, error)` | Multi-turn query with history and question rewriting |
| `UpdateDocument` | `UpdateDocument(content, source string) error` | Incrementally update a document in the graph |
| `DeleteDocument` | `DeleteDocument(docID string) error` | Remove a document and its exclusive entities |
| `Finalize` | `Finalize() error` | Global deduplication and embedding backfill |
| `Stats` | `Stats() *PipelineStats` | Return current graph statistics |
| `SubscribeStats` | `SubscribeStats() chan []byte` | Return a channel that emits JSON-encoded stats on every phase transition |

### Strategy Interfaces (`internal/pipeline/strategies`)

Pluggable strategy interfaces used by the pipeline.

| Interface | Key method | Description |
|-----------|-----------|-------------|
| `Chunker` | `Chunk(text string, chunkSize, overlap int) []Chunk` | Split text into overlapping chunks |
| `Extractor` | `Extract(chunk Chunk) ([]Entity, []Edge, error)` | Extract entities and relations from a chunk |
| `Resolver` | `Resolve(entities []Entity) ([]Entity, map[string]string, error)` | Deduplicate and merge entities, return alias map |
| `Canonicalizer` | `Canonicalize(name string) string` | Normalize an entity name to canonical form |
| `Reranker` | `Rerank(question string, results []SearchResult) []SearchResult` | Reorder retrieval results by relevance |

### FalkorStore (`internal/falkor`)

Graph and vector storage backed by FalkorDB.

| Method | Signature | Description |
|--------|-----------|-------------|
| `New` | `New(cfg *config.Config) (*FalkorStore, error)` | Connect to FalkorDB and initialize the graph |
| `Close` | `Close() error` | Close the connection |
| `Query` | `Query(cypher string, params ...map[string]interface{}) (interface{}, error)` | Execute a read-write Cypher query |
| `ROQuery` | `ROQuery(cypher string, params ...map[string]interface{}) (interface{}, error)` | Execute a read-only Cypher query |
| `CreateEntity` | `CreateEntity(e Entity) error` | Create or merge an entity node |
| `CreateEdge` | `CreateEdge(e Edge) error` | Create or merge an edge |
| `SetEntityEmbedding` | `SetEntityEmbedding(name string, embedding []float32) error` | Store a vector embedding on an entity node |
| `FindSimilarEntities` | `FindSimilarEntities(embedding []float32, topK int) ([]EntityResult, error)` | Vector similarity search over entities |
| `FindSimilarChunks` | `FindSimilarChunks(embedding []float32, topK int) ([]ChunkResult, error)` | Vector similarity search over chunks |
| `DeleteDocument` | `DeleteDocument(docID string) error` | Remove a document and its exclusive subgraph |
| `NodeCount` | `NodeCount() (int, error)` | Return total node count |
| `EdgeCount` | `EdgeCount() (int, error)` | Return total edge count |
| `DropGraph` | `DropGraph() error` | Delete the entire graph |

### Schema Governance (`internal/schema`)

Type and relation governance layer.

| Method | Signature | Description |
|--------|-----------|-------------|
| `CheckProposedType` | `CheckProposedType(name string) CheckResult` | Heuristic check against accepted types (overlap, word-order, alias) |
| `CheckProposedRelation` | `CheckProposedRelation(name string) CheckResult` | Heuristic check against accepted relations |
| `ApproveType` | `ApproveType(name string) error` | Accept a type into the schema |
| `ApproveRelation` | `ApproveRelation(name string) error` | Accept a relation into the schema |
| `AddTypeAlias` | `AddTypeAlias(alias, canonical string) error` | Register a type alias |
| `AddRelationAlias` | `AddRelationAlias(alias, canonical string) error` | Register a relation alias |
| `GovernTypeCandidates` | `GovernTypeCandidates(candidates []string) ([]GovernResult, error)` | LLM-based batch governance of candidate types |
| `GovernRelationCandidates` | `GovernRelationCandidates(candidates []string) ([]GovernResult, error)` | LLM-based batch governance of candidate relations |
