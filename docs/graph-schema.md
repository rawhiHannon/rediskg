# Knowledge Graph Schema

This document describes the node types, edge types, property conventions, and
indexing strategy used by RedisKG's FalkorDB graph.

---

## Graph Structure

```
+------------+    :PART_OF     +---------+   :NEXT_CHUNK   +---------+
|  Document  |<---------------| Chunk 0 |---------------->| Chunk 1 |---> ...
| {id, hash, |                | {id,    |                 | {id,    |
|  path}     |                |  text,  |                 |  text,  |
+------------+                |  embed} |                 |  embed} |
                              +----+----+                 +----+----+
                                   ^                           ^
                                   | :MENTIONED_IN             | :MENTIONED_IN
                                   |                           |
                          +--------+--------+         +--------+--------+
                          |    Concept      |         |    Concept      |
                          |  :Organization  |         |    :Person      |
                          | {name, type,    |         | {name, type,    |
                          |  embedding, ... }|         |  embedding, ... }|
                          +--------+--------+         +--------+--------+
                                   |                           |
                                   |      :MANAGES             |
                                   +-------------------------->+
                                   |                           |
                                   |      :BASED_AT            |
                                   +<--------------------------+
                                   |
                          +--------+--------+
                          | __Schema__      |
                          | :__EntityType__ |
                          | {name, desc,    |
                          |  parent_type}   |
                          +-----------------+
```

The graph contains five categories of nodes (Document, Chunk, Concept, and two
schema meta-node types) connected by structural edges (PART_OF, NEXT_CHUNK,
MENTIONED_IN) and domain-specific typed relation edges.

---

## Node Types

### :Document

Source document metadata.  One node per ingested file.

| Property       | Type   | Description                              |
|----------------|--------|------------------------------------------|
| `id`           | string | Stable document identifier               |
| `content_hash` | string | SHA-256 of document content              |
| `path`         | string | Original file path or URI                |

### :Chunk

Text fragment produced by the chunking phase.  Linked to its source Document
and to adjacent chunks in reading order.

| Property    | Type      | Description                                |
|-------------|-----------|--------------------------------------------|
| `id`        | string    | Stable chunk identifier                    |
| `text`      | string    | Raw chunk text                             |
| `source`    | string    | Parent document ID                         |
| `index`     | int       | Zero-based position within document        |
| `embedding` | vecf32    | Dense vector (dimension matches LLM model) |

### :Concept

The primary entity node.  Every extracted entity receives the `:Concept` label
plus an additional PascalCase label derived from its resolved type (e.g.,
`:Concept:Person`, `:Concept:Organization`).

| Property           | Type     | Description                              |
|--------------------|----------|------------------------------------------|
| `name`             | string   | Canonical name (lowercase)               |
| `type`             | string   | Resolved entity type (lowercase)         |
| `embedding`        | vecf32   | Dense vector for similarity search       |
| `status`           | string   | Verification status (e.g., `verified`)   |
| `functional_roles` | string   | Comma-separated roles from LLM profiles  |
| *(additional)*     | varies   | Domain-specific properties from extraction |

Entity names are stored in lowercase.  The entity type is stored both as a
string property (`type`) and as an additional Cypher label in PascalCase.
Label conversion uses `toTypeLabel`: underscored tokens are capitalized and
joined (e.g., `government_agency` becomes `GovernmentAgency`).

### :\_\_Schema\_\_:\_\_EntityType\_\_

Schema governance meta-node.  One per accepted entity type.

| Property      | Type   | Description                                |
|---------------|--------|--------------------------------------------|
| `name`        | string | Type name (lowercase)                      |
| `description` | string | LLM-generated description of the type      |
| `parent_type` | string | Parent in the type hierarchy               |

### :\_\_Schema\_\_:\_\_RelationType\_\_

Schema governance meta-node.  One per accepted relation type.

| Property       | Type   | Description                                |
|----------------|--------|--------------------------------------------|
| `name`         | string | Relation type name (uppercase)             |
| `description`  | string | LLM-generated description                 |
| `source_types` | string | Comma-separated valid source entity types  |
| `target_types` | string | Comma-separated valid target entity types  |
| `symmetric`    | bool   | Whether the relation is bidirectional      |

---

## Edge Types

### Structural Edges

These edges define the document-to-chunk hierarchy and entity provenance.

| Edge            | Source   | Target  | Direction | Description                    |
|-----------------|----------|---------|-----------|--------------------------------|
| `:PART_OF`      | Chunk    | Document| Chunk -> Document | Chunk belongs to document |
| `:NEXT_CHUNK`   | Chunk    | Chunk   | Chunk -> Chunk    | Reading order            |
| `:MENTIONED_IN` | Concept  | Chunk   | Concept -> Chunk  | Entity appears in chunk  |

`:MENTIONED_IN` edges are batched separately during ingest so they do not
interfere with domain-edge grouping.

### Domain Relation Edges

Domain relations use typed edge labels derived from the LLM extraction output.
Examples: `:MANAGES`, `:HAS_BRANCH`, `:BASED_AT`, `:ACQUIRED_BY`.  There is
no single `:RELATES` catch-all edge.

| Property      | Type    | Description                                     |
|---------------|---------|-------------------------------------------------|
| `description` | string  | Human-readable relation label from extraction   |
| `weight`      | float   | Accumulated confidence (incremented on re-match)|
| `inferred`    | bool    | True if relation was inferred, not stated       |
| `chunk_ids`   | string  | Comma-separated chunk IDs that sourced this edge|
| `evidence`    | string  | Extracted text evidence (appended with `\n---\n`)|
| `status`      | string  | e.g., `active`, `historical` (first-write wins) |
| `condition`   | string  | Conditional qualifier (appended with separator) |
| `fact`        | string  | Pre-formatted fact string for retrieval/embedding|
| `embedding`   | vecf32  | Dense vector of the fact string                 |
| *(temporal)*  | string  | Keys like `start_date`, `end_date`, `as_of`     |

### Why Typed Edges (Not a Single RELATES)

1. **Native pattern matching.** Cypher queries use edge types directly:

   ```cypher
   -- Find all managers of a given entity
   MATCH (mgr)-[:MANAGES]->(e:Concept {name: 'acme corp'})
   RETURN mgr.name
   ```

   A single `:RELATES` edge would require filtering on a `type` property,
   which cannot use edge-type indexes.

2. **Per-relation-type vector indexes.** FalkorDB scopes vector indexes by
   `(relationship_type, property)`.  Typed edges allow precise semantic search
   within a specific relation category:

   ```cypher
   CALL db.idx.vector.queryRelationships('MANAGES', 'embedding', 10, vecf32([...]))
   ```

3. **Schema governance per type.** Each relation type has its own
   `__RelationType__` meta-node with `source_types`, `target_types`, and
   `symmetric` constraints.  Governance rules (candidate -> heuristic ->
   LLM review -> accept/reject) operate per relation type.

4. **Graph visualization.** Tools like RedisInsight and Gephi use edge types
   for color/shape coding.  Typed edges produce readable visualizations
   without extra configuration.

---

## Property Conventions

### Entity Names

All entity names are normalized to lowercase before storage:

```go
name := strings.ToLower(strings.TrimSpace(entity.Name))
```

### Type Labels

The `type` property stores the lowercase type string.  An additional Cypher
label in PascalCase is added via a second `SET n:<Label>` query:

```cypher
MERGE (n:Concept {name: 'acme corp'})
  SET n.type = 'organization'
MATCH (n:Concept {name: 'acme corp'})
  SET n:Organization
```

This allows both property-based filtering (`WHERE n.type = 'organization'`)
and label-based pattern matching (`MATCH (n:Organization)`).

### Edge Weight Accumulation

When the same relation is extracted from multiple chunks, the weight
accumulates and chunk IDs are appended:

```cypher
ON CREATE SET r.weight = 0.85, r.chunk_ids = 'c_001'
ON MATCH  SET r.weight = r.weight + 0.85, r.chunk_ids = r.chunk_ids + ',c_002'
```

### Evidence Deduplication

Evidence strings are appended only if the existing evidence does not already
contain the new text:

```cypher
ON MATCH SET r.evidence = CASE
  WHEN r.evidence IS NULL OR r.evidence = '' THEN $new
  WHEN r.evidence CONTAINS $new THEN r.evidence
  ELSE r.evidence + '\n---\n' + $new
END
```

The same pattern applies to `condition`.  The `status` field uses first-write
semantics (keeps the first non-empty value).

### Fact Strings

Each edge stores a pre-formatted `fact` string for embedding and retrieval:

```
acme corp --[MANAGES]--> west division: evidence text here
```

This is computed at write time so the retrieval layer can embed and surface
facts without re-formatting.

---

## Indexes

### Vector Indexes

| Target              | Label/Type    | Property    | Dimension | Function |
|---------------------|---------------|-------------|-----------|----------|
| Entity embeddings   | `Concept`     | `embedding` | 1536      | cosine   |
| Chunk embeddings    | `Chunk`       | `embedding` | 1536      | cosine   |
| Edge fact embeddings| per rel type  | `embedding` | 1536      | cosine   |

Edge vector indexes are created per relation type because FalkorDB requires a
specific `(type, property)` pair for vector index scoping.

```cypher
CREATE VECTOR INDEX FOR (n:Concept) ON (n.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}

CREATE VECTOR INDEX FOR (n:Chunk) ON (n.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}

CREATE VECTOR INDEX FOR ()-[r:MANAGES]-() ON (r.embedding)
  OPTIONS {dimension: 1536, similarityFunction: 'cosine'}
```

### Fulltext Indexes

| Target          | Label     | Property | Engine     |
|-----------------|-----------|----------|------------|
| Entity names    | `Concept` | `name`   | RediSearch |
| Chunk text      | `Chunk`   | `text`   | RediSearch |

```cypher
CREATE FULLTEXT INDEX FOR (n:Concept) ON (n.name)
CREATE FULLTEXT INDEX FOR (n:Chunk)   ON (n.text)
```

Fulltext queries use RediSearch syntax under the hood.  Special characters are
escaped before querying to ensure literal matching.

---

## Schema Persistence

Accepted entity and relation types are persisted as `__Schema__` nodes inside
the graph itself, not in an external store.

```cypher
-- Persist an entity type
MERGE (s:__Schema__:__EntityType__ {name: 'organization'})
  SET s.description = 'A company, agency, or institution',
      s.parent_type = 'concept'

-- Persist a relation type
MERGE (s:__Schema__:__RelationType__ {name: 'MANAGES'})
  SET s.description = 'One entity manages or oversees another',
      s.source_types = 'person,organization',
      s.target_types = 'organization,department',
      s.symmetric = false
```

Schema nodes are excluded from user-facing queries via
`WHERE NOT n:__Schema__` filters.  On pipeline startup, persisted schema types
are loaded back with `LoadSchemaEntityTypes` and `LoadSchemaRelationTypes` so
the governance layer has full context from prior runs.
