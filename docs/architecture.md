# RedisKG Architecture

## Overview

RedisKG is a Go microservice that builds and queries knowledge graphs from
unstructured documents. It combines LLM-driven entity/relation extraction with
FalkorDB (a Redis module) for unified graph + vector storage. A single binary
handles ingestion, retrieval, and serving -- no Python runtime, no separate
vector database.

```
 Documents (PDF, TXT, MD, ...)
        |
        v
 +------------------+      +-------------+      +-----------+
 | Ingestion        |----->| FalkorDB    |<-----| Retrieval |
 | Pipeline (18ph)  |      | graph+vector|      | Pipeline  |
 +------------------+      +-------------+      | (9 phase) |
        ^                        ^               +-----------+
        |                        |                     |
 +------+------+          +------+------+        +-----+-----+
 | LLM Client  |          | Circuit     |        | REST/CLI  |
 | (multi-prov) |          | Breaker     |        | + SSE     |
 +-------------+          +-------------+        +-----------+
```

## Design Principles

1. **Domain-agnostic.** No hardcoded entity types or relation names. The LLM
   proposes types; a schema governance layer (heuristic check then LLM review)
   accepts or rejects them. A configurable base-type scaffold (upper ontology)
   seeds the initial vocabulary.

2. **Quality over quantity.** An 18-phase ingestion pipeline applies hard
   constraints, edge conflict resolution, negation detection, conditional
   annotation, and evidence tracking. Every edge carries provenance back to its
   source chunk.

3. **Pluggable strategies.** Five interface slots -- Chunker, Resolver,
   Canonicalizer, Extractor, Reranker -- can be swapped after construction
   without touching pipeline internals.

4. **Single binary deployment.** Pure Go. No Python, no subprocess calls, no
   ONNX runtime. LLM calls go over HTTP to OpenAI, Claude, Gemini, or Ollama.

5. **Unified graph + vector store.** FalkorDB handles property-graph queries
   AND vector similarity search. No Pinecone, no Weaviate, no Qdrant. One
   Redis process, one connection pool, one circuit breaker.


## Ingestion Pipeline

The pipeline runs 18 instrumented phases. Each phase reports timing and counts
through PipelineStats, which broadcasts SSE events to connected clients.

```
                         INGESTION PIPELINE
 ================================================================

  Documents
      |
      v
 [0]  Content-hash dedup (SHA-256, skip unchanged)
      |
      v
 [1]  Chunking (recursive | sentence | structural | contextual)
      |
      v
 [1c] Coreference resolution (pronoun -> entity, LLM-backed)
      |
      v
 [1b] Lexical backbone (Document -> PART_OF -> Chunk -> NEXT_CHUNK)
      |
      v
 [2]  Entity & relation extraction (schema-constrained, concurrent)
      |
      v
 [2b] Quality filter (drop raw-value entities, orphan edges)
      |
      v
 [3]  Entity resolution (alias map + canonical selection)
      |
      v
 [4]  Canonicalization (role cleanup, status fix, alias propagation)
      |
      v
 [5]  Edge rewriting (alias endpoints -> canonical names)
      |
      v
 [6]  Relation normalization (raw labels -> canonical IDs, flip inverses)
      |
      v
 [7]  Negation fixing (evidence-driven: "does not handle" -> DOES_NOT_*)
      |
      v
 [8]  Conditional annotation (if/when/unless -> status=conditional|backup)
      |
      v
 [9]  Status-aware rewriting (planned entities -> PLANNED_SERVICE, etc.)
      |
      v
 [10] Alternative group building (mutually exclusive edge sets)
      |
      v
 [11] Hard constraints (solver pre-filter, domain rules)
      |
      v
 [12] Global graph selection (solver picks highest-weight consistent set)
      |
      v
 [13] Post-solver validation (orphan removal, alias endpoint cleanup)
      |
      v
 [14] Conflict resolution + inverse derivation + temporal extraction
      |
      v
 [15] Materialization (batched UNWIND writes to FalkorDB)
      |
      v
 [16] Embedding generation (entity names + chunk text + edge facts)
```


## Retrieval Pipeline

Retrieval follows a multi-path strategy inspired by GraphRAG-SDK, adapted to
RedisKG's typed-edge schema.

```
                        RETRIEVAL PIPELINE
 ================================================================

  Question
      |
      +-- analytical? ---> LLM-to-Cypher path (aggregation queries)
      |                         |
      v                         v (fallback on failure)
 [1]  Keyword extraction (stopword filter + LLM proper-noun detection)
      |
      v
 [2]  Query embedding
      |
      v
 [3]  Edge-fact vector search (per-relation-type vector indexes)
      |
      v
 [4]  Entity discovery
      |   Path A: Cypher CONTAINS on :Concept(name)
      |   Path B: entity-vector similarity on :Concept(embedding)
      |   Path C: endpoints from edge-fact hits
      |   Path D: sibling expansion for enumeration queries
      |
      v
 [5]  Relationship expansion (1-hop + 2-hop from top entities)
      |
      v
 [6]  Chunk retrieval (4 paths)
      |   Path A: chunk-vector similarity
      |   Path B: MENTIONED_IN from discovered entities, cosine-ranked
      |   Path C: 2-hop chunk walk
      |   Path D: fulltext on :Chunk(text)
      |
      v
 [7]  Cosine reranking (stored embeddings fast path / re-embed slow path)
      |
      v
 [8]  Context assembly (question-type hint + structured sections)
      |
      v
 [9]  LLM answer generation (JSON response, facts-only grounding)
```


## Strategy Pattern

Five interfaces govern the pluggable behaviors. Default implementations are
wired in `Pipeline.New()`; callers swap them by direct field assignment.

```
 +-------------------+   +-----------------------------------------+
 | Interface         |   | Implementations                         |
 +-------------------+   +-----------------------------------------+
 | Chunker           |   | recursive (default), sentence,          |
 |                   |   | structural, contextual (LLM-augmented)  |
 +-------------------+   +-----------------------------------------+
 | Resolver          |   | TieredResolver (exact -> semantic ->    |
 |                   |   |   LLM merge)                            |
 +-------------------+   +-----------------------------------------+
 | Canonicalizer     |   | defaultCanonicalizer (role cleanup,     |
 |                   |   |   status fix, service collapse, alias   |
 |                   |   |   property propagation)                 |
 +-------------------+   +-----------------------------------------+
 | Extractor         |   | defaultExtractor (schema-constrained    |
 |                   |   |   LLM extraction, concurrent per chunk) |
 +-------------------+   +-----------------------------------------+
 | Reranker          |   | defaultReranker (cosine similarity,     |
 |                   |   |   fast path with stored embeddings)     |
 +-------------------+   +-----------------------------------------+
```


## Data Flow

```
 Raw Documents
      |
      v
 Candidate Entities + Edges  (LLM extraction, per-chunk)
      |
      v
 Canonical Entities          (alias resolution, dedup, type merge)
      |
      v
 Rewritten Edges             (canonical endpoints, normalized relations)
      |
      v
 Annotated Edges             (negation, conditional, status-aware)
      |
      v
 Alternative Groups          (mutually exclusive edge sets)
      |
      v
 Hard Constraints            (domain rules, forbidden combos)
      |
      v
 Solver Output               (globally consistent edge selection)
      |
      v
 Final Graph                 (validated, conflict-resolved, enriched)
      |
      v
 FalkorDB                    (typed nodes + typed edges + vectors)
```


## Storage Layer

FalkorDB (Redis module) provides both property-graph and vector-index
capabilities behind a single connection.

**Node labels:**
- `:Concept` -- base label for all entities
- Per-type labels: `:Concept:Organization`, `:Concept:Person`, etc.
- `:Chunk` -- document chunks with text and embedding
- `:Document` -- source documents with content hash

**Edge types:**
- Per-relation typed edges: `HAS_BRANCH`, `MANAGES`, `OFFERS`, etc.
  (not a single `:RELATES` with a property)
- `PART_OF` / `NEXT_CHUNK` -- lexical backbone
- `MENTIONED_IN` -- entity-to-chunk provenance
- `ALIAS_OF` -- alias nodes pointing to canonical entities

**Vector indexes:**
- `Concept.embedding` -- entity name embeddings
- `Chunk.embedding` -- chunk text embeddings
- Per-relation-type `<RelType>.embedding` -- edge fact embeddings (lazy creation)

**Fulltext indexes:**
- `Concept.name`
- `Chunk.text`

**Resilience:**
- Circuit breaker with three states (Closed / Open / HalfOpen)
- Exponential backoff with jitter on consecutive failures
- Configurable fail threshold, base cooldown, and max cooldown

```
 +----------------------------------------------------------+
 |                      FalkorDB                            |
 |                                                          |
 |  Concept(name, embedding, status, domain_type, ...)      |
 |     |                                                    |
 |     +--[HAS_BRANCH]----> Concept                         |
 |     +--[MANAGES]-------> Concept                         |
 |     +--[OFFERS]--------> Concept                         |
 |     +--[MENTIONED_IN]--> Chunk                           |
 |     +--[ALIAS_OF]------> Concept                         |
 |                                                          |
 |  Document(path, hash)                                    |
 |     +--[PART_OF]-------> Chunk(id, text, embedding)      |
 |                             +--[NEXT_CHUNK]--> Chunk     |
 |                                                          |
 |  Vector indexes:  Concept(embedding)                     |
 |                   Chunk(embedding)                        |
 |                   <RelType>(embedding) per relation       |
 +----------------------------------------------------------+
```


## Schema Governance

The three-layer trust model prevents unchecked schema growth.

```
 Layer 1: Base Types (configurable upper ontology)
      |  Person, Organization, Location, Event, ...
      |  Loaded at startup. Extensible via config.
      |
 Layer 2: Candidate Types (LLM-proposed)
      |  Pass heuristic checks: word-order variants,
      |  token overlap (Jaccard), inverse detection (_BY suffix)
      |
 Layer 3: Accepted Types (LLM-governed)
         GovernTypeCandidates -> approve/reject
         Persisted as __Schema__:__EntityType__ / __RelationType__ nodes
```

Key files: `schema/governance.go`, `schema/base.go`, `llm/governance.go`


## Telemetry

`PipelineStats` tracks every phase with start/end timestamps, durations, and
aggregate counts (documents, chunks, entities extracted, edges after solver,
final graph size, FalkorDB node/edge counts).

```
 Pipeline                PipelineStats             SSE Subscribers
    |                         |                         |
    +-- StartPhase("...") --> |                         |
    |                         +-- broadcast("phase_start") --> ch
    |                         |                         |
    +-- EndPhase("...") ----> |                         |
    |                         +-- broadcast("phase_end") ----> ch
    |                         |                         |
    +-- SetCounts(fn) ------> |                         |
    |                         +-- broadcast("counts_update") -> ch
    |                         |                         |
    +-- Complete() ---------> |                         |
                              +-- broadcast("pipeline_complete") -> ch
                              +-- close all subscribers
```

**REST endpoints:**
- `GET /api/pipeline/stats` -- JSON snapshot of current run
- `GET /api/pipeline/events` -- SSE stream of phase transitions


## API Surface

```
 REST API
 --------
 POST /api/ingest          Ingest documents (file upload)
 POST /api/query           Natural-language question -> answer + subgraph
 POST /api/chat            Multi-turn chat with graph context
 POST /api/cypher          Raw Cypher query execution
 PUT  /api/document        Update a single document (re-ingest)
 DELETE /api/document      Remove a document and its graph footprint
 POST /api/finalize        Run post-ingest finalization
 GET  /api/graph           Paginated graph visualization data
 GET  /api/export          Full graph export
 GET  /api/stats           Graph statistics (node/edge counts)
 GET  /api/pipeline/stats  Pipeline telemetry snapshot
 GET  /api/pipeline/events Pipeline SSE event stream
 DELETE /api/graph         Drop the entire graph
```


## Project Layout

```
 cmd/rediskg/main.go           CLI + server entrypoint
 internal/
   pipeline/
     pipeline.go               Pipeline struct, strategy wiring, embeddings
     strategies.go             Chunker/Resolver/Canonicalizer/Extractor/Reranker
     ingest.go                 18-phase ingestion pipeline
     query.go                  Question answering, lookup-name detection
     multi_path.go             9-phase multi-path retrieval
     analytical.go             LLM-to-Cypher for aggregation queries
     enrich.go                 Enrichment passes
     lexical.go                Lexical backbone (Document/Chunk/PART_OF/NEXT_CHUNK)
     coref.go                  Coreference resolution
     dedup.go                  Content-hash deduplication
     resolver_tiered.go        TieredResolver (exact -> semantic -> LLM)
     telemetry.go              PipelineStats, SSE broadcast
     batchwriter.go            Batched UNWIND materialization
     chat.go                   Multi-turn chat
     update.go                 Document update/delete
   llm/
     client.go                 Multi-provider LLM client
     claude.go                 Anthropic Claude provider
     gemini.go                 Google Gemini provider
     extract_schema.go         Schema-constrained extraction prompts
     governance.go             LLM-backed type governance
     normalize.go              LLM-backed name normalization
     prompts.go                Shared prompt templates
   schema/
     schema.go                 Schema registry
     base.go                   Base types (upper ontology)
     governance.go             Candidate -> accepted type flow
     normalize.go              Relation alias resolution, inverse flip
     evolve.go                 Schema evolution
     ontology.go               Ontology rules
   store/
     falkor.go                 FalkorDB client (graph + vector + fulltext)
     cypher.go                 Cypher query builders
     circuitbreaker.go         Circuit breaker with exponential backoff
   solver/
     hard_constraints.go       Domain constraint enforcement
     selector.go               Global graph selection (ILP-style)
   graph/
     merge.go                  Graph merge operations
     validate.go               Validation helpers
     filter.go                 Graph filtering
     preserve.go               Preservation rules
     proximity.go              Proximity scoring
   loader/
     loader.go                 Document loading (PDF, TXT, MD, ...)
   server/
     server.go                 REST API server + routes
     ui.go                     Embedded graph visualization UI
   chunker/
     chunker.go                Recursive chunker (default)
     sentence.go               Sentence-boundary chunker
     structural.go             Heading/section chunker
     contextual.go             LLM-augmented contextual chunker
   setup/
     setup.go                  Initialization helpers
 pkg/
   config/config.go            Configuration (env + flags)
   models/
     models.go                 Core domain types
     kg.go                     Knowledge graph types (KGEntity, KGEdge, FinalGraph)
```
