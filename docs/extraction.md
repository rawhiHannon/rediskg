# Extraction

How RedisKG extracts entities and relationships from text.

## Big Picture

```
                          +------------------+
                          |   Chunk Text     |
                          +--------+---------+
                                   |
                          +--------v---------+
                          |  Schema Context  |
                          |  (accepted types |
                          |   + relations)   |
                          +--------+---------+
                                   |
                     +-------------v--------------+
                     |   LLM Extraction (Pass 1)  |
                     |   Entity NER + Relations   |
                     +-------------+--------------+
                                   |
                     +-------------v--------------+
                     |   JSON Parsing + Fallback  |
                     +-------------+--------------+
                                   |
              +--------------------+--------------------+
              |                                         |
    +---------v----------+                   +----------v---------+
    | CandidateEntity[]  |                   | CandidateEdge[]    |
    | name, type, aliases|                   | source, target,    |
    | evidence, mention  |                   | relation, evidence |
    +--------------------+                   +--------------------+
```

## Pluggable Extractor Interface

Extraction is abstracted behind the `Extractor` interface:

```go
type Extractor interface {
    Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph
}
```

The default implementation (`defaultExtractor`) delegates to
`extractSchemaConstrained`, which uses LLM-based extraction with schema
context. Swap the Extractor on the Pipeline struct to plug in alternative
strategies (e.g., local NER, zero-shot, hybrid).

```go
p := pipeline.New(cfg, store, llmClient)
p.Extractor = myCustomExtractor{}
```

## Schema-Constrained LLM Extraction

The default extraction strategy sends each chunk to the LLM with the
current accepted schema as context:

1. **Schema context** -- the LLM receives a summary of accepted entity
   types and relation types, so it extracts entities that fit the known
   ontology while remaining free to propose new types.

2. **Per-chunk extraction** -- each chunk is processed independently,
   bounded by the `Workers` concurrency limit (default 8 goroutines).

3. **JSON response parsing** -- the LLM returns structured JSON with
   entities and relationships. A fallback parser handles malformed
   responses.

### Prompt Structure

The extraction prompt asks the LLM to return:

```json
{
  "entities": [
    {
      "name": "Acme Corp",
      "type": "organization",
      "canonical_name": "acme corp",
      "aliases": ["Acme", "ACME Corporation"],
      "evidence": "Acme Corp announced quarterly earnings..."
    }
  ],
  "relationships": [
    {
      "source": "acme corp",
      "target": "john smith",
      "relation": "EMPLOYS",
      "evidence": "John Smith, CEO of Acme Corp..."
    }
  ]
}
```

## Concurrent Extraction

Extraction runs concurrently across chunks:

```
Chunk 1 ──► goroutine 1 ──► LLM call ──┐
Chunk 2 ──► goroutine 2 ──► LLM call ──┤
Chunk 3 ──► goroutine 3 ──► LLM call ──┼──► merge ──► CandidateGraph
...                                     │
Chunk N ──► goroutine N ──► LLM call ──┘
```

A semaphore of size `Workers` bounds concurrency. Results are merged
under a mutex into the combined `CandidateGraph`.

## Candidate Data Model

### CandidateEntity

| Field           | Type     | Description                                    |
|-----------------|----------|------------------------------------------------|
| `Mention`       | string   | Surface form as it appeared in text             |
| `CanonicalName` | string   | Normalized name (lowercase, trimmed)            |
| `Type`          | string   | Entity type proposed by the LLM                 |
| `Aliases`       | []string | Alternative names                               |
| `Evidence`      | []string | Source text spans supporting this entity         |
| `ChunkIDs`      | []string | Which chunks this entity was found in            |
| `Properties`    | map      | Additional properties (status, roles, etc.)      |

### CandidateEdge

| Field       | Type     | Description                                    |
|-------------|----------|------------------------------------------------|
| `Source`    | string   | Source entity canonical name                    |
| `Target`    | string   | Target entity canonical name                    |
| `Relation`  | string   | Relationship type                              |
| `Evidence`  | string   | Source text supporting this relationship         |
| `ChunkIDs`  | []string | Which chunks this edge was found in              |
| `Weight`    | float64  | Confidence weight                               |
| `Inferred`  | bool     | Whether this was inferred (not directly stated)  |
| `Status`    | string   | Temporal status (active, planned, former)        |
| `Condition` | string   | Conditional qualifier                           |

## Quality Filtering

After extraction, a quality filter removes noise:

- **Value entities** -- dates, times, quantities, percentages are dropped
  (they belong as edge properties, not graph nodes)
- **Empty identifiers** -- entities with blank name or type
- **Orphan edges** -- edges referencing entities that were filtered out

## Coreference Resolution

An optional pre-extraction step resolves pronouns to entity names:

```go
type CorefResolver struct {
    LLM     *llm.Client
    Workers int
}
```

When enabled (default), each chunk is scanned for pronouns (he, she, it,
they, the company, etc.). Chunks containing pronouns are sent to the LLM
for rewriting before extraction, so "he announced" becomes "John Smith
announced".

This runs as Phase 1c, between chunking and extraction.

## Evidence Tracking

Every extracted entity and edge carries its source text as evidence.
This evidence propagates through the entire pipeline:

1. **Extraction** -- LLM includes the source text span
2. **Canonicalization** -- evidence from all mentions is merged
3. **Edge rewriting** -- evidence preserved when edges are remapped
4. **Negation fixing** -- evidence is inspected for negation patterns
5. **Conditional annotation** -- evidence is inspected for conditions
6. **Materialization** -- evidence stored on graph edges
7. **Retrieval** -- evidence used in answer context

This deep evidence tracking is a key differentiator from systems that
only track provenance via Document-Chunk links.

## Key Files

| File | Purpose |
|------|---------|
| `internal/pipeline/strategies.go` | Extractor interface definition |
| `internal/pipeline/ingest.go` | extractSchemaConstrained implementation |
| `internal/pipeline/coref.go` | Coreference resolution |
| `internal/llm/client.go` | LLM API calls |
| `pkg/models/kg.go` | CandidateEntity, CandidateEdge models |

## Next Steps

- [Ingestion Pipeline](ingestion.md) -- how extraction fits into the full pipeline
- [Schema Governance](schema-governance.md) -- how proposed types are governed
- [Strategies](strategies.md) -- swapping the Extractor implementation
