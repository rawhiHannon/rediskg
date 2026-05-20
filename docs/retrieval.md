# RedisKG Retrieval System

This document describes how RedisKG answers natural-language questions over a
knowledge graph stored in FalkorDB. The system uses a multi-path retrieval
pipeline inspired by GraphRAG-SDK, adapted for RedisKG's typed-edge graph
schema (individually typed relationships instead of a single `:RELATES` edge
with a `rel_type` property).

---

## High-Level Flow

```
                         +-------------------+
                         |   User Question   |
                         +--------+----------+
                                  |
                        +---------v----------+
                        | Analytical Check   |
                        | (count/most/least) |
                        +---------+----------+
                           /             \
                      yes /               \ no
                         /                 \
            +-----------v---+       +------v-----------+
            | LLM-to-Cypher |       | Multi-Path       |
            | Generation    |       | Retrieval (9ph)  |
            +-------+-------+       +------+-----------+
                    |                       |
              ok?  / \  fail                |
                  /   \----fallback-------->|
                 v                          v
          +------+------+        +---------+----------+
          | Execute      |       | Context Assembly    |
          | Cypher Query |       | + LLM Answer Gen    |
          +------+-------+       +---------+----------+
                 |                          |
                 v                          v
          +------+------+        +---------+----------+
          | Summarise    |       | QueryResult          |
          | Rows via LLM |       | {Answer, Graph,      |
          +------+-------+       |  Facts, Entities}    |
                 |               +--------------------+
                 v
          +------+------+
          | QueryResult  |
          +--------------+
```

---

## Query Entry Points

### `Pipeline.Query(question, withHumanAnswer)`

The primary entry point. Accepts a natural-language question and a flag
controlling whether the LLM generates a human-readable answer or returns raw
structured data (for agent callers).

**Source:** `internal/pipeline/query.go`

### `Pipeline.Chat(ChatRequest)`

Multi-turn conversation wrapper. Accepts a question, conversation history, and
an optional rewrite flag. Delegates to `Query` after optionally rewriting the
question.

**Source:** `internal/pipeline/chat.go`

---

## Mode 1: Multi-Path Retrieval

Used for non-analytical questions (entity lookups, factual queries, general
knowledge questions). Implemented in `runMultiPath` in
`internal/pipeline/multi_path.go`.

### Phase Diagram

```
  Question
     |
     v
 [1] Keyword Extraction ----> simple[] + llm[]
     |
     v
 [2] Question Embedding ----> qvec (float32 vector)
     |
     v
 [3] Edge-Fact Vector Search
     |   UNION ALL across per-relation-type vector indexes
     |   Returns: scored facts + endpoint entities
     |
     v
 [4] Entity Discovery (3 paths merged)
     |   +-- Fulltext search on Concept.name index
     |   +-- Cypher CONTAINS matching (exact + substring)
     |   +-- Entity-name vector similarity search
     |   +-- Merge edge-fact endpoint entities from phase 3
     |
     v
 [4b] Sibling Expansion (enumeration queries only)
     |   "list all / name every / enumerate" patterns
     |   Pulls in graph neighbors of hub nodes
     |
     v
 [5] Relationship Expansion
     |   1-hop: entity -[r]-> neighbor (up to 150 rows)
     |   2-hop: entity -[r1]-> mid -[r2]-> far (up to 25 rows)
     |
     v
 [6] Chunk Retrieval (4 paths merged)
     |   +-- Fulltext search on Chunk.text
     |   +-- Chunk vector similarity search
     |   +-- MENTIONED_IN traversal with stored-vector cosine ranking
     |   +-- 2-hop: entity -> neighbor -> MENTIONED_IN -> chunk
     |
     v
 [7] Source Document Paths
     |   Batch resolve chunk -> PART_OF -> Document.path
     |
     v
 [8] Cosine Reranking
     |   Fast-path: use stored embeddings (zero API calls)
     |   Slow-path: re-embed candidates at query time
     |
     v
 [9] Context Assembly + LLM Answer Generation
     |   Build structured sections -> hand to LLM -> QueryResult
     v
  QueryResult
```

### Phase 1: Keyword Extraction

Two keyword sets are produced from the question:

- **Simple keywords** -- stopword-filtered tokens from the question, capped at
  12. Uses a built-in stopword list (question words, articles, prepositions,
  common verbs).
- **LLM keywords** -- proper nouns, character names, place names, and specific
  terms extracted by a single LLM call. The LLM returns a JSON object
  `{"names": ["name1", "name2", ...]}`.

Both lists are merged (LLM keywords first, capped at 8, then simple keywords
appended) to form the combined keyword list used by downstream phases.

### Phase 2: Question Embedding

The question is embedded into a dense vector via `llmClient.Embed(question)`.
This vector (`qvec`) is reused across entity discovery, edge-fact search, chunk
retrieval, and reranking -- a single embedding call serves all vector-based
phases.

### Phase 3: Edge-Fact Vector Search

Searches across ALL relation-type vector indexes in the graph. Unlike
GraphRAG-SDK (which uses a single `:RELATES` edge type), RedisKG stores each
relation as its own edge type (`HAS_BRANCH`, `MANAGES`, `BASED_AT`, etc.),
each with its own vector index.

**Procedure:**

1. Query `db.relationshipTypes()` to enumerate all relation types.
2. Filter to those where at least one edge has a non-null `embedding` property.
3. Call `FindSimilarEdgeFacts` with `UNION ALL` across all qualifying types.
4. Return up to 15 scored facts, filtered by relevance (minimum score 0.25,
   always keep at least 3, cap at 12).

The endpoint entities from matched edge facts are forwarded to entity discovery
as additional graph entry points (tagged with source `"edge_fact"`).

### Phase 4: Entity Discovery

Three independent search paths are executed and merged into a deduplicated
entity pool:

| Path | Method | Details |
|------|--------|---------|
| Fulltext | `FulltextSearch("Concept", "name", kw, 5)` | RediSearch fulltext index on `Concept.name`. Falls back silently if index does not exist. |
| Cypher CONTAINS | Parameterized `UNWIND $keywords` query | Two passes: exact name match first (`toLower(e.name) = toLower(kw)`), then substring CONTAINS with shorter-name priority. Up to 8 keywords, 5 results per keyword. |
| Vector | `FindSimilarEntities("Concept", "embedding", qvec, 10)` | Cosine similarity against entity embedding vectors. |

Edge-fact endpoint entities from phase 3 are also added to the pool.

Entity deduplication is by lowercase-trimmed name; first path to discover an
entity wins the source tag.

### Phase 4b: Sibling Expansion

Triggered only for enumeration queries (detected by regex: `every`, `each`,
`complete list`, `full list`, `list all`, `enumerate`, `name all`, etc.).

Finds "hub" nodes connected to two or more already-discovered entities, then
pulls in their other neighbors (siblings). Capped at 20 additional entities.

### Phase 5: Relationship Expansion

From the top 30 discovered entities (top 15 for 2-hop), expands the graph:

- **1-hop:** `MATCH (a:Concept)-[r]->(b:Concept)` -- returns source, relation
  type, target, and the edge's `fact` or `description` property. Up to 150
  rows.
- **2-hop:** `MATCH (a:Concept)-[r1]->(b:Concept)-[r2]->(c:Concept)` -- up to
  25 rows, only for the top 5 entities.

Results are formatted as human-readable strings:
`"Entity A --[RELATION]-> Entity B: fact text"` and deduplicated.

### Phase 6: Chunk Retrieval

Four independent retrieval paths feed into a shared chunk pool:

| Path | Method | Cap |
|------|--------|-----|
| Fulltext | RediSearch fulltext index on `Chunk.text` | 10 chunks (question) + 3 per keyword |
| Vector | `FindSimilarChunks(qvec, 15)` | 15 chunks |
| MENTIONED_IN | Entity -> MENTIONED_IN -> Chunk, ranked by `vec.cosineDistance(c.embedding, qvec)` | 3 per entity, top 15 entities |
| 2-hop | Entity -> neighbor -> MENTIONED_IN -> Chunk | 20 chunks, top 10 entities |

After collection, stored chunk embeddings are batch-loaded in a single Cypher
round-trip (`UNWIND $ids ... WHERE c.embedding IS NOT NULL`) and attached to
the candidate pool for use in the reranking phase.

### Phase 7: Source Document Paths

Each candidate chunk's source document path is resolved via
`MATCH (c:Chunk)-[:PART_OF]->(d:Document)` in a single batch query. These
paths are used to tag passages with `[Source: /path/to/doc]` in the final
context.

### Phase 8: Stored-Vector Reranking

The reranker selects two strategies based on embedding coverage:

- **Fast path (>=90% coverage):** When 90% or more of candidate chunks already
  have a stored embedding vector, cosine similarity is computed locally against
  `qvec`. Zero additional API calls. This is the common case after ingestion.
- **Slow path (<90% coverage):** Re-embeds all candidate chunk texts at query
  time using concurrent workers (default 8). Used only when embeddings are
  missing (e.g., chunks ingested before embedding support was added).

Top-K chunks (default 15) survive reranking and proceed to context assembly.

### Phase 9: Context Assembly and Answer Generation

The final context handed to the LLM is built from structured sections:

```
## Answer Format Hint
(auto-detected: yes/no, who, where, when, how many)

## Key Entities
- Entity A: description
- Entity B

## Entity Relationships
- Entity A --[MANAGES]-> Entity B: fact text
- Entity B --[BASED_AT]-> Entity C

## Knowledge Graph Facts
- Entity X --[OFFERS]-> Service Y
- ...

## Source Document Passages
[Source: /path/to/doc1.pdf]
Passage text ...
---
[Source: /path/to/doc2.txt]
Passage text ...
```

The question-type detector (`detectQuestionType`) examines the question prefix
to prepend a format hint (e.g., "This is a yes/no question -- start with Yes
or No"). The LLM is instructed to respond as JSON (`{"answer": "..."}`),
using only the provided context, never fabricating information.

---

## Mode 2: Analytical Query Detection

Used for aggregation, ranking, and counting questions. Detected by keyword
matching against analytical cues.

**Source:** `internal/pipeline/analytical.go`

### Detection

The question (lowercased) is scanned for cue phrases:

```
"how many", "count of", "number of", "most", "least",
"highest", "lowest", "biggest", "largest", "smallest",
"top", "best", "worst", "average", "median", "total",
"which has", "rank", "ranking", "ordered by"
```

If any cue is found, the analytical path runs first.

### LLM-to-Cypher Generation

```
  Question + Graph Schema + Allowed Relation Types
     |
     v
  LLM generates ONE read-only Cypher query (JSON response)
     |
     v
  Safety check: reject any CREATE/MERGE/DELETE/SET/REMOVE
     |
     v
  Execute against FalkorDB (ROQuery)
     |
     v
  Parse rows -> format as text -> LLM summarises -> QueryResult
```

The system prompt includes:

- The graph schema (node labels, properties, edge conventions).
- The full list of allowed relation types (from the schema governance layer).
- Worked examples for common patterns (count, ranking, filtering).
- Strict rules: read-only only, lowercase name matching, entity names as first
  column.

The Cypher extractor tries three strategies in order: JSON `{"cypher": "..."}`,
fenced code block, raw `MATCH` statement.

### Fallback

If the Cypher generation fails, the generated query errors out, or no rows are
returned, the system falls back to multi-path retrieval transparently. The user
sees no error -- the analytical failure is logged and the multi-path result is
returned instead.

---

## Multi-Turn Chat

**Source:** `internal/pipeline/chat.go`

### Flow

```
  ChatRequest {question, history[], rewrite_question}
     |
     v
  Should rewrite? (history non-empty AND rewrite flag not false)
     |
    yes
     |
     v
  LLM rewrites question using last 6 history messages
  "Given conversation history, rewrite as standalone question"
  -> {"rewritten_question": "..."}
     |
     v
  Pipeline.Query(rewritten_question, withHumanAnswer=true)
     |
     v
  Custom system prompt? (check history for role="system")
     |
    yes
     |
     v
  Re-generate answer with custom system prompt + full history
  using CompleteMessages (multi-message LLM call)
     |
     v
  QueryResult
```

### Question Rewriting

When conversation history is provided, the follow-up question is rewritten
into a standalone query that resolves pronouns and references. For example:

- History: "Tell me about Yara Haddad" / "She manages the Karmiel branch"
- Follow-up: "What services does it offer?"
- Rewritten: "What services does the Karmiel branch offer?"

The rewriter uses at most the last 6 non-system messages (each truncated to
300 characters) to stay within context limits.

### Custom System Prompts

If the conversation history contains a message with `role: "system"`, that
message is used as the system prompt for the final answer generation instead
of the default `answerPrompt`. This allows callers to control tone, format,
and constraints.

---

## Query Features

### Alias Rewriting

Before entity matching, the question is rewritten using the alias map stored
in the graph (`ALIAS_OF` edges). This ensures that abbreviations and alternate
names resolve correctly:

- "IBM" -> "international business machines"
- "CGHN" -> "cedargate health network"

Alias replacement uses word-boundary-aware regex to avoid partial matches
(e.g., "ed" inside "named" is not replaced). Alias rewriting is skipped for
name-lookup queries ("is there anyone called X?") to preserve the user's
literal intent.

**Source:** `rewriteQueryWithAliases` in `internal/pipeline/query.go`

### Entity Profile Building

Discovered entities carry descriptions and source tags indicating how they
were found (`fulltext`, `cypher_exact`, `cypher_contains`, `vector`,
`edge_fact`, `sibling`). The entity pool deduplicates by lowercase name --
the first discovery path wins the source tag, preserving provenance.

### Fact-Enriched Answers

Edge facts (from vector search) and relationship strings (from graph
expansion) are both included in the LLM context. Edge facts carry their
cosine similarity score and are filtered by a minimum relevance threshold
(0.25), with at least 3 always kept. This ensures the LLM sees both
direct graph structure and semantically relevant edge annotations.

### Subgraph Visualization

Every `QueryResult` includes a focused subgraph for visualization:

- **Focus nodes:** The top 8 discovered entities (always included).
- **1-hop neighbors:** Direct graph neighbors, admitted in descending edge
  weight order. Capped at 60 neighborhood nodes.
- **Edges between focus nodes** are always kept regardless of weight.

The subgraph is deliberately limited to 1-hop expansion -- 2-hop from any
well-connected entity (e.g., a branch or parent network) would pull in most
of the graph.

### Answer Format Detection

The question prefix is analyzed to provide the LLM with an answer-format hint:

| Pattern | Hint |
|---------|------|
| `is/are/was/did/does/can/...` | "This is a yes/no question -- start with Yes or No" |
| `who` | "Name the specific person(s) or character(s)" |
| `where` | "Name the specific place or location" |
| `when` | "Provide the specific time, date, or period" |
| `how many/how much` | "Provide a specific number or quantity" |

---

## Key Implementation Files

| File | Purpose |
|------|---------|
| `internal/pipeline/query.go` | Query entry point, alias rewriting, entity matching, subgraph building |
| `internal/pipeline/multi_path.go` | 9-phase multi-path retrieval orchestrator |
| `internal/pipeline/analytical.go` | Analytical detection, LLM-to-Cypher, row formatting |
| `internal/pipeline/chat.go` | Multi-turn chat, question rewriting, custom system prompts |
| `internal/store/` | FalkorDB operations (vector search, fulltext, Cypher execution) |
| `pkg/models/` | `QueryResult`, `SubGraph`, `GraphNode`, `GraphEdge` structs |
