# Storage Layer Internals

This document describes the storage layer that sits between the pipeline and
FalkorDB.  Every graph read or write flows through `FalkorStore`, which adds
connection pooling, circuit-breaker protection, batched writes, and safe Cypher
encoding.

---

## Architecture Overview

```
Pipeline / CLI / gRPC
        |
        v
+------------------+
|   FalkorStore    |  internal/store/falkor.go
|  (Redis client)  |
+--------+---------+
         |
   +-----+------+
   | Circuit     |  internal/store/circuitbreaker.go
   | Breaker     |
   +-----+------+
         |
   +-----+------+
   | Cypher      |  internal/store/cypher.go
   | Encoding    |
   +-------------+
         |
         v
+------------------+
|   FalkorDB       |  Redis 8.x + FalkorDB module
|  (graph+vector)  |
+------------------+
```

---

## FalkorStore

**Source:** `internal/store/falkor.go`

`FalkorStore` wraps a `redis.Client` and exposes every graph operation the
project needs: entity/edge CRUD, vector search, fulltext search, schema
persistence, and stats.

### Connection Settings

| Parameter      | Value             | Notes                                |
|----------------|-------------------|--------------------------------------|
| `PoolSize`     | 100               | Max concurrent Redis connections     |
| `ReadTimeout`  | 2 min             | Long reads for heavy vector queries  |
| `WriteTimeout` | 1 min             | Covers large UNWIND batches          |
| `DB`           | 0                 | Default Redis database               |
| `Password`     | (empty)           | Local-only; set via config for prod  |

On startup the constructor issues a lightweight `GRAPH.QUERY __rediskg_probe__
"RETURN 1"` to confirm the FalkorDB module is loaded, then deletes the probe
graph.

### Query Methods

| Method                | Redis Command     | Circuit Breaker | Description                          |
|-----------------------|-------------------|-----------------|--------------------------------------|
| `Query`               | `GRAPH.QUERY`     | Yes             | Read-write Cypher                    |
| `ROQuery`             | `GRAPH.RO_QUERY`  | Yes             | Read-only Cypher (replica-safe)      |
| `QueryWithParams`     | `GRAPH.QUERY`     | Yes             | Parameterized via CYPHER prefix      |
| `ROQueryWithParams`   | `GRAPH.RO_QUERY`  | Yes             | Read-only parameterized variant      |

`QueryWithParams` prepends the FalkorDB `CYPHER k=v ...` prefix to the query
string so parameter values are bound server-side before planning.  This lets
the batched writer reuse the same query template across thousands of items.

---

## Circuit Breaker

**Source:** `internal/store/circuitbreaker.go`

Every `Query` and `ROQuery` call passes through a three-state circuit breaker
that prevents cascading failures when FalkorDB becomes unresponsive.

### State Machine

```
                success
            +------------+
            |            |
            v            |
 +--------+    +----------+    consecutive    +---------+
 | Closed | -->| executing |--- fails >= 5 -->|  Open   |
 +--------+    +----------+                   +----+----+
      ^                                            |
      |           cooldown expires                 |
      |        +-------------+                     |
      +--------| Half-Open   |<--------------------+
   probe OK    | (one probe) |    probe fails --> Open
               +-------------+      (increased cooldown)
```

### Configuration

| Parameter       | Default | Description                                      |
|-----------------|---------|--------------------------------------------------|
| `FailThreshold` | 5       | Consecutive failures before tripping              |
| `BaseCooldown`  | 1 s     | Initial wait before first Half-Open probe         |
| `MaxCooldown`   | 30 s    | Upper bound on exponential backoff                |
| Jitter          | +/-25%  | Random factor applied to each cooldown            |

### Backoff Formula

```
cooldown = BaseCooldown * 2^(openCount - 1)
cooldown = min(cooldown, MaxCooldown)
cooldown = cooldown + cooldown * 0.25 * random(-1, 1)
```

Each time the circuit re-opens (including failed Half-Open probes), `openCount`
increments, doubling the cooldown.  A successful probe resets `openCount` to
zero and transitions back to Closed.

---

## Batched Writes

**Source:** `internal/pipeline/batchwriter.go`

The pipeline groups entities and edges into 500-item UNWIND batches to
amortize round-trip cost.  The batch size matches GraphRAG-SDK's
`GraphStore._BATCH_SIZE` and stays within FalkorDB's parser limits.

### Entity Batching

1. Entities are grouped by their PascalCase typed label (e.g., `Person`,
   `Organization`).
2. Each group shares one query template:

```cypher
UNWIND $batch AS item
MERGE (n:Concept {name: item.name})
SET n.type = item.type, n += item.properties
SET n:`Person`
```

3. The `$batch` parameter is a list of maps, each with `name`, `type`, and
   `properties` keys.
4. Properties are cleaned (nil values stripped, control characters removed)
   before encoding.

### Edge Batching

1. Edges are grouped by relation type (`MANAGES`, `HAS_BRANCH`, etc.).
2. Each group shares one query template with the relation type baked in:

```cypher
UNWIND $batch AS item
MERGE (a:Concept {name: item.from})
MERGE (b:Concept {name: item.to})
SET a.type = CASE WHEN item.from_type = '' THEN a.type ELSE item.from_type END
SET b.type = CASE WHEN item.to_type = '' THEN b.type ELSE item.to_type END
MERGE (a)-[r:`MANAGES`]->(b)
ON CREATE SET
  r.description = item.description,
  r.weight = item.weight,
  r.inferred = item.inferred,
  r.chunk_ids = item.chunk_ids,
  r.evidence = item.evidence,
  r.status = item.status,
  r.condition = item.condition,
  r.fact = item.fact,
  r += item.temporal
ON MATCH SET
  r.weight = r.weight + item.weight,
  r.chunk_ids = r.chunk_ids + ',' + item.chunk_ids,
  r.evidence = CASE
    WHEN r.evidence IS NULL OR r.evidence = '' THEN item.evidence
    WHEN r.evidence CONTAINS item.evidence THEN r.evidence
    ELSE r.evidence + '\n---\n' + item.evidence END,
  r.status = CASE WHEN r.status IS NULL OR r.status = '' THEN item.status ELSE r.status END,
  r.condition = CASE
    WHEN r.condition IS NULL OR r.condition = '' THEN item.condition
    WHEN r.condition CONTAINS item.condition THEN r.condition
    ELSE r.condition + '\n---\n' + item.condition END
```

3. `MENTIONED_IN` edges (Concept to Chunk) are batched separately from
   domain relation edges.

### Per-Item Fallback

When a batch write fails (e.g., a single malformed property poisons the
UNWIND), the writer falls back to inserting each item individually.  This
isolates the bad row without losing the rest of the batch.

---

## Cypher Utilities

**Source:** `internal/store/cypher.go`

### Parameter Encoding

`EncodeCypherParams` builds the `CYPHER k1=v1 k2=v2 ` prefix that FalkorDB
parses before query planning:

```go
EncodeCypherParams(map[string]interface{}{
    "batch": []interface{}{
        map[string]interface{}{"id": "a"},
    },
})
// => "CYPHER batch=[{id: 'a'}] "
```

### Value Encoding

`EncodeCypherValue` converts Go values to Cypher literals:

| Go Type              | Cypher Literal        |
|----------------------|-----------------------|
| `string`             | `'escaped string'`    |
| `int`, `int64`       | `42`                  |
| `float64`            | `3.14`                |
| `bool`               | `true` / `false`      |
| `nil`                | `null`                |
| `[]string`           | `['a', 'b']`          |
| `[]interface{}`      | `[1, 'x', true]`     |
| `map[string]interface{}` | `{key: 'val'}`   |

### Injection Prevention

| Function              | Purpose                                            |
|-----------------------|----------------------------------------------------|
| `SanitizeCypherLabel` | Strips backticks, rejects non-identifier chars      |
| `IsCypherIdentifier`  | Validates `[letter_][letter_digit_]*` pattern       |
| `escapeCypher`        | Escapes `\`, `'`, `;` in interpolated strings       |
| `sanitizePropertyKey` | Normalizes keys: lowercase, `[a-z0-9_]`, max 40    |

---

## Index Management

FalkorStore creates three kinds of indexes at ingest time.

### Vector Indexes (Cosine Similarity)

```cypher
-- Entity embeddings
CREATE VECTOR INDEX FOR (n:Concept) ON (n.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}

-- Chunk embeddings
CREATE VECTOR INDEX FOR (n:Chunk) ON (n.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}

-- Per-relation-type edge embeddings
CREATE VECTOR INDEX FOR ()-[r:MANAGES]-() ON (r.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}
```

Edge vector indexes are scoped per relation type because FalkorDB requires a
specific `(type, property)` pair.

### Fulltext Indexes (RediSearch)

```cypher
CREATE FULLTEXT INDEX FOR (n:Concept) ON (n.name)
CREATE FULLTEXT INDEX FOR (n:Chunk)   ON (n.text)
```

---

## Search Methods

| Method                  | Index Used            | Returns                        |
|-------------------------|-----------------------|--------------------------------|
| `FindSimilarEntities`   | Concept.embedding     | `[]string` (entity names)      |
| `FindSimilarChunks`     | Chunk.embedding       | `[]ChunkSimilarity` (id, text, score) |
| `FindSimilarEdgeFacts`  | per-rel edge.embedding| `[]EdgeFactSimilarity` (src, tgt, rel, fact, score) |
| `FulltextSearch`        | any (label, property) | `[]string` (node names)        |
| `FulltextSearchChunks`  | Chunk.text            | `[]ChunkSimilarity`            |

### Multi-Relation Edge Vector Search

`FindSimilarEdgeFacts` queries multiple per-relation-type indexes in a single
Cypher call using `UNION ALL`, then sorts results by score descending and trims
to top-k:

```cypher
CALL db.idx.vector.queryRelationships('MANAGES', 'embedding', 10, vecf32([...]))
  YIELD relationship, score
  MATCH (a)-[r]->(b) WHERE id(r) = id(relationship)
  RETURN a.name AS src, type(r) AS rel_type, b.name AS tgt,
         COALESCE(r.fact, '') AS fact, score
UNION ALL
CALL db.idx.vector.queryRelationships('HAS_BRANCH', 'embedding', 10, vecf32([...]))
  YIELD relationship, score
  MATCH (a)-[r]->(b) WHERE id(r) = id(relationship)
  RETURN a.name AS src, type(r) AS rel_type, b.name AS tgt,
         COALESCE(r.fact, '') AS fact, score
```

Results across all relation types are merged and the overall top-k is returned.
