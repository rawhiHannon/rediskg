# Ingestion Pipeline

This guide explains how RedisKG transforms raw documents into a verified,
queryable knowledge graph inside FalkorDB. The pipeline runs 20 instrumented
phases, each observable via telemetry (Server-Sent Events on
`/api/pipeline/events` or CLI log lines).

---

## Big-Picture Flow

```
                         +------------------+
                         |  Documents / Dir |
                         |  / Raw Text      |
                         +--------+---------+
                                  |
                    1. Content-hash dedup (SHA-256)
                                  |
                    2. Chunking (4 strategies)
                                  |
                    3. Coreference resolution (optional, LLM)
                                  |
                    4. Lexical backbone (Document/Chunk nodes)
                                  |
                    5. Entity & relation extraction (LLM, concurrent)
                                  |
                    6. Quality filtering
                                  |
                    7. Entity resolution (tiered: exact/cosine/LLM)
                                  |
                    8. Canonicalization
                                  |
                    9. Edge rewriting (alias map)
                                  |
                   10. Relation normalization
                                  |
                   11. Negation fixing (evidence-based)
                                  |
                   12. Conditional annotation (evidence-based)
                                  |
                   13. Status-aware rewriting
                                  |
                   14. Alternative group building
                                  |
                   15. Hard constraints (13 semantic rules)
                                  |
                   16. Global graph selection (weighted scorer)
                                  |
                   17. Post-solver validation
                                  |
                   18. Conflict resolution & enrichment
                                  |
                   19. Materialization (batched UNWIND)
                                  |
                   20. Embedding generation (vector + fulltext)
                                  |
                         +--------v---------+
                         |    FalkorDB      |
                         |  Knowledge Graph |
                         +------------------+
```

---

## Phase-by-Phase Walkthrough

### Phase 1 -- Content-Hash Dedup

Before any work begins, the pipeline computes a SHA-256 hash over each
document's content and compares it against the `content_hash` property
stored on the corresponding `:Document` node in FalkorDB. Documents whose
hash matches are silently skipped. This makes repeated ingestion of the
same corpus essentially free.

The check runs inside `filterUnchangedDocs`. If every document is
unchanged, the pipeline exits immediately with a "nothing to ingest"
status.

### Phase 2 -- Chunking

Each document is split into overlapping text chunks by a pluggable
`Chunker` interface. Four strategies are available:

| Strategy | How it splits |
|---|---|
| **recursive** (default) | Paragraph boundaries first, then sentence, then word. |
| **sentence** | Strictly on sentence boundaries. Handles Arabic/CJK punctuation. |
| **structural** | On heading/section markers (Markdown `#`, HTML `<h*>`). |
| **contextual** | LLM-assisted: prepends a context summary to each chunk so it stands alone. |

Default settings: chunk size 1500 characters, overlap 150 characters. Both
are configurable via `--chunk-size` and `--chunk-overlap`.

```bash
rediskg ingest ./docs/ --chunk-strategy sentence --chunk-size 2000 --chunk-overlap 200
```

### Phase 3 -- Coreference Resolution (Optional)

When enabled, an LLM pass replaces pronouns and anaphoric references with
their antecedents before extraction. For example, "He joined the company
in 2019" becomes "John Smith joined the company in 2019".

This is an optional phase. When the `Coref` component is nil (default),
the pipeline logs a skip and moves on. Enable it when your documents
contain heavy pronoun usage and you want higher entity-recall.

### Phase 4 -- Lexical Backbone

The pipeline writes structural nodes and edges that anchor the knowledge
graph to its source text:

- **`:Document` nodes** -- one per ingested file, carrying `id`,
  `source`, and `content_hash` properties.
- **`:Chunk` nodes** -- one per text segment, with `id`, `text`, and
  `doc_id` properties.
- **`PART_OF` edges** -- each `:Chunk` points to its parent `:Document`.
- **`NEXT_CHUNK` edges** -- chain chunks in reading order so retrieval can
  walk the sequence.

All writes use batched `UNWIND` queries for efficiency.  This backbone is
what makes per-document deletion, citation, and chunk-level retrieval
possible.

### Phase 5 -- Entity and Relation Extraction

The core extraction phase sends each chunk to the LLM through a pluggable
`Extractor` interface. The default extractor is schema-constrained: it
passes the current accepted schema to the LLM so extractions conform to
known types and relations where possible.

Extraction is concurrent. The `Workers` configuration (default 8, set via
`--workers`) controls how many chunks are processed in parallel using a
semaphore-bounded goroutine pool.

The default extractor uses a two-pass strategy:

1. **NER pass** -- identify entity mentions, types, aliases, and
   functional roles.
2. **Verify + Relate pass** -- verify entities and extract relations
   between them with supporting evidence.

Each extracted entity carries:
- Mention text and canonical name
- Base types and domain types with confidence scores
- Aliases and functional roles
- Evidence references (chunk ID + source text span)

Each extracted edge carries:
- Source and target mentions
- Raw relation label and resolved relation ID
- Evidence text from the source chunk
- Confidence score

### Phase 6 -- Quality Filtering

A cleanup pass removes noise that LLMs commonly produce:

- **Raw value entities** -- dates, times, quantities, monetary amounts,
  and schedule fragments are dropped. These belong as edge properties, not
  as standalone nodes. Detection uses both type-based checks (e.g.,
  `date_time`, `quantity` types) and regex patterns (e.g., `10:00`,
  `2024-11-06`, `Q4 2026`, `every Monday`).
- **Empty entities** -- entities with blank mention and canonical name.
- **Orphan edges** -- edges whose source or target was removed by entity
  filtering.

### Phase 7 -- Entity Resolution

A pluggable `Resolver` merges duplicate entity mentions into canonical
entries. The default `TieredResolver` applies three levels of matching:

1. **Exact match** -- case-insensitive string equality.
2. **Semantic cosine similarity** -- embedding-based comparison with two
   thresholds:
   - Hard merge at cosine >= 0.95 (no further verification needed).
   - Soft candidate at cosine >= 0.80 (proceeds to LLM verification).
3. **LLM verification** -- for soft candidates, the LLM confirms or
   rejects the merge.

Transitively connected mentions are clustered using a Union-Find data
structure, so if A merges with B and B merges with C, all three resolve to
the same canonical entity.

The output is:
- A map of canonical entity names to `CanonicalEntity` structs (with
  aggregated types, roles, evidence, and aliases).
- An alias map: `mention -> canonical name`.

### Phase 8 -- Canonicalization

A pluggable `Canonicalizer` normalizes the canonical entities:

- **Role cleanup** -- removes functional roles that conflict with the
  entity's domain types (e.g., a "lab" entity should not have a "courier"
  role).
- **Status fixing** -- events with past-tense evidence (e.g., "occurred
  on", "was resolved") are corrected from "planned" to "historical".
- **Service-name collapse** -- deduplicates service entity variants
  (e.g., "blood test" merges into "blood tests").
- **Alias property propagation** -- ensures every alias in the alias map
  is also recorded in the canonical entity's `aliases` property.

### Phase 9 -- Edge Rewriting

All edges are rewritten from raw mention strings to canonical entity
names using the alias map built in Phase 7. Both the `FromMention` and
`ToMention` fields are lowercased, trimmed, and resolved through the map.

After this phase, every edge references canonical entities rather than
surface-form mentions.

### Phase 10 -- Relation Normalization

Raw relation labels are normalized to stable internal relation IDs using
the schema's relation registry. This phase also:

- Drops edges with empty or rejected relation IDs.
- Flips `(from, to)` for inverse aliases (e.g., `MANAGED_BY` becomes
  `MANAGES` with swapped endpoints).
- Removes self-loops (edges where source equals target).

### Phase 11 -- Negation Fixing

An evidence-based pass detects negated relations by scanning the evidence
text for negation phrases ("does not handle", "not available", "no
contract with", etc.). When negation is detected, the positive relation ID
is replaced with its negative counterpart:

| Positive | Negative |
|---|---|
| `OFFERS` | `DOES_NOT_OFFER` |
| `HANDLES_BILLING_FOR` | `DOES_NOT_HANDLE_BILLING_FOR` |
| `CONTRACTED_WITH` | `NO_CONTRACT_WITH` |
| `PROCESSES_TESTS_FOR` | `DOES_NOT_PROCESS_TESTS_FOR` |

This runs before hard constraints so the solver sees correct relation IDs.

### Phase 12 -- Conditional Annotation

Another evidence-based pass identifies conditional and backup
relationships by scanning for trigger phrases:

- **Conditional triggers**: "if", "when", "during", "unless", "in case"
- **Backup triggers**: "downtime", "redirected", "backup", "fallback",
  "emergency"

Matching edges receive a `status` property (`conditional` or `backup`) and
a `condition` property containing the extracted conditional clause.

Backup status is only applied to eligible partner/service relations (e.g.,
`PROCESSES_TESTS_FOR`, `HANDLES_BILLING_FOR`). Event relations like
`INVOLVES` or `CAUSED_BY` are never marked as backup.

### Phase 13 -- Status-Aware Rewriting

Edges are rewritten based on the status of the entities they connect:

- A planned entity that `OFFERS` a service becomes `PLANNED_SERVICE`.
- A parent with a planned child via `HAS_BRANCH` becomes
  `HAS_PLANNED_BRANCH`.
- Any edge touching a planned entity inherits `status=planned` if no
  status was previously set.

This phase also includes two sub-steps:

- **Planned-service misuse fix** -- if the LLM emitted `PLANNED_SERVICE`
  but the source entity is actually active, the edge is corrected back to
  `OFFERS`.
- **Branch completion** -- fills in missing branch edges when evidence
  supports them.

### Phase 14 -- Alternative Group Building

Competing edges for the same entity pair are grouped into alternative
sets. For example, if entity A has both `OFFERS` and `PLANNED_SERVICE`
edges pointing to entity B, they form an alternative group. The solver
(Phase 16) will select the best candidate from each group.

### Phase 15 -- Hard Constraints

Thirteen domain-aware semantic rules filter clearly invalid edges. These
include:

- **Alias compatibility** -- edges referencing unresolved aliases are
  removed.
- **Evidence-backed** -- edges without supporting evidence may be
  penalized or dropped.
- **Hierarchy validation** -- structural relations (PART_OF, HAS_BRANCH)
  must respect entity type hierarchies.
- **Role compatibility** -- relation source/target must have compatible
  functional roles.
- **Domain type restrictions** -- certain relation types forbid specific
  source or target domain types.

The constraints are defined in the schema layer and applied by the solver
package.

### Phase 16 -- Global Graph Selection

A weighted scoring function selects the final set of edges from the
surviving candidates. The scorer balances three factors:

| Factor | Weight | What it measures |
|---|---|---|
| Evidence strength | 40% | How well the source text supports the edge |
| Schema fit | 30% | Whether the edge conforms to accepted schema rules |
| Confidence | 30% | The LLM's extraction confidence score |

The solver iterates over alternative groups and picks the highest-scoring
edge from each group, then includes all non-competing edges that pass a
minimum threshold.

### Phase 17 -- Post-Solver Validation

A final cleanup pass on the solved graph:

- Removes edges whose endpoints do not exist in the entity set.
- Removes self-loops.
- Rejects non-ALIAS_OF edges where either endpoint is still an alias
  (should have been rewritten in Phase 9).
- Drops orphan entities that do not participate in any edge.
- Ensures every entity has at least one base type (defaults to "concept").

### Phase 18 -- Conflict Resolution and Enrichment

Three enrichment steps run on the final graph:

1. **Negative-fact resolution** -- when both a positive edge (e.g.,
   `OFFERS`) and its negative counterpart (`DOES_NOT_OFFER`) exist for the
   same entity pair, the positive edge is removed. The negative fact wins.

2. **Inverse derivation** -- structural relations are completed in both
   directions. If the LLM only extracted one side, the inverse is
   generated automatically:
   - `(branch)-[:PART_OF]->(parent)` adds
     `(parent)-[:HAS_BRANCH]->(branch)`
   - `(parent)-[:HAS_BRANCH]->(child)` adds
     `(child)-[:PART_OF]->(parent)`
   - Planned branches produce `HAS_PLANNED_BRANCH` instead of
     `HAS_BRANCH`.

3. **Temporal extraction** -- temporal facts (dates, durations, schedules)
   are extracted from edge evidence and stored as properties on the
   relevant edges.

Additionally, address statuses are propagated: if an active entity is
`LOCATED_AT` an address, that address inherits "active" status.

### Phase 19 -- Materialization

The final graph is written to FalkorDB using batched `UNWIND` queries.
Each batch contains up to 500 items, with per-item fallback on failure so
a single bad record does not block the entire batch.

Four kinds of writes occur:

1. **Entity nodes** -- each entity becomes a `:Concept` node with an
   additional typed label (e.g., `:Organization`, `:Service`). Properties
   include `domain_type`, `status`, `functional_roles`, and `aliases`.

2. **Relation edges** -- edges are grouped by relation type and written
   with one `UNWIND` per type. Each edge carries `weight`, `evidence`,
   `status`, `condition`, and `temporal` properties.

3. **ALIAS_OF edges** -- surviving aliases that were not materialized as
   full entities get an `:Alias` node pointing to the canonical entity via
   `ALIAS_OF`.

4. **MENTIONED_IN edges** -- link each materialized `:Concept` node back
   to the `:Chunk` nodes it was extracted from. This provenance backbone
   powers citations, per-document deletion, and chunk-level retrieval.

### Phase 20 -- Embedding Generation

Three kinds of embeddings are generated and stored:

1. **Entity embeddings** -- each `:Concept` node gets a vector embedding
   of its canonical name and key properties, stored in the `embedding`
   property.
2. **Chunk embeddings** -- each `:Chunk` node gets a vector embedding of
   its text content.
3. **Edge fact embeddings** -- for each relation type, edge descriptions
   are embedded to support semantic edge search.

After embedding, the pipeline creates (or verifies) vector and fulltext
indexes:

```cypher
CREATE VECTOR INDEX FOR (e:Concept) ON (e.embedding)
    OPTIONS {dimension: 1536, similarityFunction: 'cosine'}

CREATE VECTOR INDEX FOR (c:Chunk) ON (c.embedding)
    OPTIONS {dimension: 1536, similarityFunction: 'cosine'}
```

The embedding dimension is configurable (default 1536 for OpenAI
`text-embedding-3-small`).

---

## Entry Points

### Ingest (files)

```bash
rediskg ingest report.pdf --graph mykg --llm openai --model gpt-4o
```

Calls `Pipeline.Ingest(docs)` which runs all 20 phases.

### IngestDir (directory)

```bash
rediskg ingest ./documents/ --graph mykg --workers 8
```

Calls `Pipeline.IngestDir(path)`. Internally, `loader.LoadDirectory`
recursively reads all supported files from the directory, then passes the
resulting document slice to `Pipeline.Ingest`.

### IngestRawText (string)

```bash
curl -X POST http://localhost:8080/api/ingest \
  -H "Content-Type: application/json" \
  -d '{"text": "Marie Curie discovered radium in 1898."}'
```

Calls `Pipeline.IngestRawText(text, source)`. Wraps the string in a
single `Document` struct via `loader.LoadText` and passes it to
`Pipeline.Ingest`.

---

## Incremental Updates

### UpdateDocument

```bash
curl -X PUT http://localhost:8080/api/document \
  -H "Content-Type: application/json" \
  -d '{"path": "/path/to/updated-report.txt"}'
```

`UpdateDocument` uses a three-step transactional pattern with crash
recovery:

1. **Ingest under a pending ID** -- the new content is ingested as a
   `__pending__:<docID>` document node. This runs the full 20-phase
   pipeline but writes to a separate namespace in the graph.

2. **Mark ready to commit** -- once ingestion succeeds, the pending
   document node is atomically marked with `ready_to_commit = true`.

3. **Swap** -- the old document's chunks and entity references are
   deleted, and the pending node is renamed to the live document ID.

**Crash recovery**: if the process dies between steps 1 and 3, the next
call to `UpdateDocument` or `Finalize` detects pending nodes (via
`MATCH (d:Document) WHERE d.id STARTS WITH '__pending__:'`) and either
completes or rolls back the cutover automatically.

### DeleteDocument

```bash
curl -X DELETE http://localhost:8080/api/document \
  -H "Content-Type: application/json" \
  -d '{"document_id": "report.pdf"}'
```

`DeleteDocument` removes a document and its chunks from the graph while
preserving shared entities. The process:

1. Find all `:Chunk` nodes belonging to the document via `PART_OF` edges.
2. Delete `MENTIONED_IN` edges pointing to those chunks.
3. Delete the chunks themselves and the `:Document` node.
4. Entities that are still referenced by other documents remain in the
   graph. Only entities whose sole provenance was the deleted document
   become orphans (cleaned up by `Finalize`).

---

## Finalize

```bash
rediskg finalize --graph mykg
```

`Finalize` is a post-ingestion maintenance step that should be run after
a batch of incremental updates. It performs three tasks:

1. **Crash recovery** -- recovers any stuck `__pending__` documents from
   prior crashes.
2. **Global deduplication** -- cross-document entity deduplication that
   merges entities which were ingested in separate pipeline runs but refer
   to the same real-world concept.
3. **Embedding backfill** -- generates embeddings for any entities or
   chunks that are missing them (e.g., due to a previous embedding failure
   or a newly merged entity).

---

## Configuration Reference

| Flag | Default | Description |
|---|---|---|
| `--chunk-size` | 1500 | Maximum characters per chunk |
| `--chunk-overlap` | 150 | Character overlap between adjacent chunks |
| `--chunk-strategy` | recursive | Splitting strategy (recursive, sentence, structural, contextual) |
| `--workers` | 8 | Concurrent extraction goroutines |
| `--embedding-dim` | 1536 | Embedding vector dimension |
| `--graph` | knowledge_graph | FalkorDB graph name |

---

## Graph Schema After Ingestion

After a successful ingestion, the FalkorDB graph contains these node and
edge types:

**Nodes:**

| Label | Purpose |
|---|---|
| `:Document` | Source document with `id`, `source`, `content_hash` |
| `:Chunk` | Text segment with `id`, `text`, `doc_id` |
| `:Concept` | Canonical entity (also carries a typed label like `:Organization`) |
| `:Alias` | Alias variant pointing to a canonical entity |

**Edges:**

| Type | Connects | Purpose |
|---|---|---|
| `PART_OF` | Chunk -> Document | Chunk provenance |
| `NEXT_CHUNK` | Chunk -> Chunk | Reading-order chain |
| `MENTIONED_IN` | Concept -> Chunk | Entity provenance |
| `ALIAS_OF` | Alias -> Concept | Alias resolution |
| *(domain edges)* | Concept -> Concept | Knowledge relations (OFFERS, HAS_BRANCH, etc.) |

**Indexes:**

| Index | Type | On |
|---|---|---|
| Concept embedding | Vector (cosine) | `:Concept(embedding)` |
| Chunk embedding | Vector (cosine) | `:Chunk(embedding)` |
| Concept fulltext | Fulltext | `:Concept(name)` |
