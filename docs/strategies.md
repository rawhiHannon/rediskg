# Strategy Reference

RedisKG uses the strategy pattern for its core pipeline operations. Each
strategy is defined as a Go interface in `internal/pipeline/strategies.go`.
The pipeline ships with default implementations but any strategy can be
swapped at runtime by setting the corresponding field on the `*Pipeline`
struct before calling `Ingest` or `Query`.

This document covers all five strategy interfaces, their implementations,
and how to write custom strategies.

---

## Strategy Overview

| Interface | Purpose | Default Implementation |
|---|---|---|
| `Chunker` | Split documents into chunks | `defaultChunker` (recursive character splitter) |
| `Resolver` | Build alias map and canonical entity set | `defaultResolver` (alias map + service canon rules) |
| `Canonicalizer` | Post-process canonical entities | `defaultCanonicalizer` (role/status/service cleanup) |
| `Extractor` | Extract entities and relations from chunks | `defaultExtractor` (schema-constrained LLM extraction) |
| `Reranker` | Rank candidate chunks for query results | `defaultReranker` (cosine similarity, fast/slow path) |

All strategies are set on the `Pipeline` struct after creation:

```go
p := pipeline.New(cfg)
p.Chunker = chunker.SentenceChunker{}
p.Resolver = pipeline.NewTieredResolver(llmClient)
p.Ingest("/path/to/docs")
```

---

## 1. Chunker

```go
type Chunker interface {
    ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk
}
```

The Chunker splits documents into chunks for LLM extraction. Implementations
must be deterministic -- the same input must yield the same chunk IDs so that
content-hash deduplication and `MENTIONED_IN` edges remain stable across runs.

### defaultChunker (recursive)

**Package:** `internal/chunker`
**Strategy flag:** `--chunk-strategy recursive`

The default chunker implements recursive character splitting, similar to
LangChain's `RecursiveCharacterTextSplitter`. It tries a hierarchy of
separators in order:

1. Double newline (`\n\n`) -- paragraph boundaries
2. Single newline (`\n`)
3. Sentence-ending punctuation (`. `, `? `, `! `)
4. Multilingual punctuation (`؟ ` Arabic, `。` CJK, `، ` Arabic comma, `、` CJK comma)
5. Semicolons and commas
6. Word boundaries (space)

Each chunk carries section context from the nearest preceding heading,
stored in the chunk's metadata as `section`.

```go
// Used automatically when ChunkStrategy is "recursive" or unset.
chunks := chunker.ChunkDocuments(docs, 1500, 150)
```

**When to use:** General-purpose text without strong structural markers.
Good default for mixed-format corpora.

### SentenceChunker

**Package:** `internal/chunker`
**Strategy flag:** `--chunk-strategy sentence`

Splits text strictly on sentence boundaries. Sentences are detected using
period, question mark, and exclamation mark patterns (including Arabic and
CJK equivalents). Sentences are then merged into chunks up to the size limit,
ensuring no chunk ever breaks mid-sentence.

```go
p.Chunker = chunker.SentenceChunker{}
```

**When to use:** Prose-heavy documents (articles, reports, books) where
sentence integrity matters for extraction quality. Produces slightly uneven
chunk sizes since it never splits sentences.

### StructuralChunker

**Package:** `internal/chunker`
**Strategy flag:** `--chunk-strategy structural`

Splits documents by markdown heading hierarchy (`#`, `##`, `###`, etc.).
Each section under a heading becomes one chunk. If a section exceeds
`chunkSize`, it falls back to recursive character splitting within that
section. Every chunk preserves its heading context in metadata.

```go
p.Chunker = chunker.StructuralChunker{}
```

**When to use:** Markdown files, technical documentation, wiki pages, or
any document with clear heading structure. Produces chunks that align with
the document's logical organization.

### ContextualChunker

**Package:** `internal/chunker`
**Strategy flag:** `--chunk-strategy contextual`

A decorator that wraps any base chunker and prepends a short LLM-generated
context summary to each chunk. This implements the "contextual retrieval"
pattern: each chunk carries a 1-2 sentence description of where it fits
in the overall document, improving both extraction and retrieval quality.

```go
p.Chunker = &chunker.ContextualChunker{
    Base:      chunker.ChunkDocuments,    // any base chunking function
    ContextFn: myLLMContextGenerator,     // func(docText, chunkText string) string
    Workers:   8,                         // concurrent LLM calls for context generation
}
```

The `ContextFn` callback is wired at the pipeline layer to avoid a direct
dependency between the chunker package and the LLM client.

**When to use:** When retrieval quality is the top priority and LLM cost
is acceptable. Each chunk requires one additional LLM call for context
generation. Best paired with the `recursive` or `sentence` base chunker.

---

## 2. Resolver

```go
type Resolver interface {
    Resolve(entities []models.CandidateEntity) (
        canonicals map[string]*models.CanonicalEntity,
        aliasMap   map[string]string,
    )
}
```

The Resolver builds the canonical entity set and the alias map from raw
extraction output. It determines which surface forms refer to the same
real-world entity and selects one canonical name for each group.

### defaultResolver

**Package:** `internal/pipeline` (unexported)

Runs three steps in sequence:

1. **buildAliasMap** -- Groups entities by normalized name, builds initial
   alias mappings based on substring matching and common patterns.
2. **addServiceCanonRules** -- Adds domain-specific canonicalization rules
   for service-type entities (e.g., "AWS Lambda" and "Lambda" merge).
3. **selectCanonicalEntities** -- Picks the best canonical name for each
   group based on frequency, specificity, and completeness.

```go
// Used by default. No explicit setup needed.
p := pipeline.New(cfg)
```

**When to use:** Fast, deterministic resolution suitable for most corpora.
No LLM calls, no embedding computation. Handles common aliasing patterns
well but may miss semantically similar entities with very different surface
forms.

### TieredResolver

**Package:** `internal/pipeline`
**File:** `internal/pipeline/resolver_tiered.go`

A 3-tier entity resolution strategy inspired by GraphRAG-SDK's multi-phase
deduplication:

| Tier | Method | Threshold | Cost |
|---|---|---|---|
| 1 | Exact match | Case-insensitive name+label grouping | Free |
| 2 | Semantic similarity | Cosine similarity on embeddings | Embedding API calls |
| 3 | LLM verification | YES/NO judgment on ambiguous pairs | LLM API calls |

Between tiers, Union-Find clustering ensures transitive merges: if A is
similar to B and B is similar to C, all three merge into one canonical
entity even if A and C were never directly compared.

```go
resolver := pipeline.NewTieredResolver(llmClient)

// Optional: tune thresholds
resolver.HardThreshold = 0.95       // auto-merge above this (default: 0.95)
resolver.SoftThreshold = 0.80       // send to LLM between soft and hard (default: 0.80)
resolver.MaxLLMVerifications = 50   // cap LLM calls per ingest (default: 50)
resolver.Workers = 8                // concurrent embedding calls (default: 8)

p.Resolver = resolver
```

**Configuration:**

| Parameter | Default | Description |
|---|---|---|
| `HardThreshold` | `0.95` | Pairs above this cosine similarity are merged without LLM check |
| `SoftThreshold` | `0.80` | Pairs between soft and hard thresholds are sent to LLM for verification |
| `MaxLLMVerifications` | `50` | Maximum LLM calls per ingest to control cost |
| `Workers` | `8` | Concurrent embedding API calls |

**When to use:** Corpora with many entity name variations (e.g., "IBM",
"International Business Machines", "Big Blue"). Higher quality than the
default resolver but requires embedding and LLM API calls. The
`MaxLLMVerifications` cap prevents runaway costs on large ingests.

---

## 3. Canonicalizer

```go
type Canonicalizer interface {
    Canonicalize(entities map[string]*models.CanonicalEntity, aliasMap map[string]string)
}
```

The Canonicalizer applies domain-aware post-processing to canonical entities
after resolution. It is decoupled from the Resolver so that the same
resolution output can be cleaned up with different domain rules.

### defaultCanonicalizer

**Package:** `internal/pipeline` (unexported)

Runs four cleanup passes in sequence:

1. **cleanConflictingFunctionalRoles** -- Removes contradictory role labels
   (e.g., an entity tagged as both "CEO" and "Engineer" when only one role
   is supported by the evidence).
2. **fixEntityStatuses** -- Normalizes status fields (active, inactive,
   deceased) based on temporal evidence in the source text.
3. **canonicalizeServiceEntities** -- Collapses service-type entities that
   differ only in version or deployment qualifier into a single canonical
   form.
4. **applyAliasProperties** -- Propagates properties (descriptions,
   attributes) from alias entities to their canonical form so no
   information is lost during merging.

```go
// Used by default. No explicit setup needed.
p := pipeline.New(cfg)
```

**When to use:** Suitable for most domains. The cleanup rules are generic
enough for business, technical, and scientific corpora. Replace it only
when your domain has specific canonicalization requirements that conflict
with the default behavior.

---

## 4. Extractor

```go
type Extractor interface {
    Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph
}
```

The Extractor takes chunked documents and returns a candidate graph of
entities and edges. The `workers` parameter controls concurrency.

### defaultExtractor

**Package:** `internal/pipeline` (unexported)

Uses schema-constrained LLM extraction. The current accepted schema (entity
types, relation types) is included in the LLM prompt to guide extraction.
This produces more consistent output than unconstrained extraction because
the LLM is encouraged to reuse existing types rather than inventing new ones.

The extractor holds a reference to the pipeline so it can access the schema
state and LLM client.

```go
// Used by default. No explicit setup needed.
p := pipeline.New(cfg)
```

**When to use:** The standard choice for all ingestion workloads. Schema
constraints improve type consistency across chunks and documents.

### HybridExtractor (NER + LLM)

**Package:** `internal/pipeline`
**File:** `internal/pipeline/hybrid_extractor.go`
**Strategy flag:** `--extraction-strategy hybrid`

Uses a local NER service (GLiNER, spaCy, or any HTTP NER API) for entity
extraction, then sends those entities to the LLM for verification and
relationship extraction. This cuts LLM calls in half per chunk.

```
LLM strategy:    NER (LLM) -> Verify+Relations (LLM) = 2 calls/chunk
Hybrid strategy:  NER (local) -> Verify+Relations (LLM) = 1 call/chunk
```

Per-chunk flow:

1. Call NER service (`POST /ner`) for entity spans (free, fast)
2. Convert spans to JSON summary with base type hints
3. Send entities + chunk text to LLM verify+relations pass
4. If NER service fails, fall back to standard two-pass LLM extraction

```go
// Via config (automatic)
cfg.ExtractionStrategy = "hybrid"
cfg.NERServiceURL = "http://localhost:9000"
p := pipeline.New(cfg, store, llmClient)

// Or manually
p.Extractor = pipeline.NewHybridExtractor(p, "http://localhost:9000")
```

**NER service protocol:** Any HTTP service implementing `POST /ner` with
`{"text":"..."}` -> `{"entities":[{text, start, end, label}]}`. A ready-to-use
service is provided in `scripts/ner_service.py` (GLiNER and spaCy backends).

**When to use:** Large corpora where LLM cost is a concern. Standard
entities (people, organizations, locations) are well-handled by local NER
models. For domain-specific entities, the default LLM strategy may produce
better results.

See [Extraction](extraction.md#hybrid-ner--llm-extraction) for full details.

### Custom Extractor Example

To implement a completely custom extractor:

```go
type MyExtractor struct {
    LLMClient *llm.Client
}

func (e *MyExtractor) Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph {
    // Your implementation here
}

p.Extractor = &MyExtractor{LLMClient: llmClient}
```

---

## 5. Reranker

```go
type Reranker interface {
    Rerank(queryVec []float32, candidates []*RankedChunk, topK int) []*RankedChunk
}
```

The Reranker scores and selects the top-K candidate chunks given a query
embedding vector. It is used during the query path after initial retrieval
from FalkorDB's vector index.

### RankedChunk

The `RankedChunk` struct carries all the data needed for reranking:

```go
type RankedChunk struct {
    ID        string
    Text      string
    Embedding []float32
    Score     float64
    Sources   []string   // retrieval paths that found this chunk
}
```

### defaultReranker

**Package:** `internal/pipeline` (unexported)

Uses cosine similarity with a two-path strategy:

**Fast path (>=90% of candidates have stored embeddings):**
Computes cosine similarity directly between the query vector and each
candidate's stored embedding. No API calls required. Candidates without
embeddings receive a score of -1.0 and sink to the bottom.

**Slow path (<90% have stored embeddings):**
Re-embeds chunks that are missing embeddings by calling the LLM embedding
API at query time. This handles cases where embeddings were not generated
during ingestion (e.g., due to API errors or older data).

Results are sorted by descending cosine similarity score and truncated
to `topK`.

```go
// Used by default. No explicit setup needed.
p := pipeline.New(cfg)
```

**When to use:** Good balance between speed and quality. The fast path
makes it efficient for graphs where embeddings are populated during
ingestion (the common case).

### Custom Reranker Example

To implement a cross-encoder reranker that uses an LLM to score
query-chunk relevance directly:

```go
type CrossEncoderReranker struct {
    LLMClient *llm.Client
}

func (cr *CrossEncoderReranker) Rerank(
    queryVec []float32,
    candidates []*pipeline.RankedChunk,
    topK int,
) []*pipeline.RankedChunk {
    type scored struct {
        chunk *pipeline.RankedChunk
        score float64
    }

    items := make([]scored, len(candidates))
    for i, c := range candidates {
        // Use LLM to score relevance on a 0-1 scale
        score := cr.LLMClient.ScoreRelevance(queryText, c.Text)
        items[i] = scored{c, score}
    }

    sort.SliceStable(items, func(i, j int) bool {
        return items[i].score > items[j].score
    })

    out := make([]*pipeline.RankedChunk, 0, topK)
    for i := 0; i < len(items) && i < topK; i++ {
        items[i].chunk.Score = items[i].score
        out = append(out, items[i].chunk)
    }
    return out
}
```

---

## Writing Custom Strategies

All five strategies follow the same pattern for integration:

### Step 1: Implement the Interface

Create a struct that satisfies the interface. The struct can hold any
dependencies (LLM clients, models, configuration) as fields.

```go
package mystrategy

import "rediskg/pkg/models"

type MyChunker struct {
    MaxTokens int
}

func (mc *MyChunker) ChunkDocuments(
    docs []*models.Document,
    chunkSize, overlap int,
) []*models.Chunk {
    // Your implementation here
}
```

### Step 2: Set It on the Pipeline

After creating the pipeline with `pipeline.New(cfg)`, assign your
implementation to the corresponding field before calling `Ingest` or
`Query`:

```go
p := pipeline.New(cfg)
p.Chunker = &mystrategy.MyChunker{MaxTokens: 512}
p.Ingest("/path/to/docs")
```

### Step 3: Ensure Determinism (Chunker)

If implementing a custom `Chunker`, chunk IDs must be deterministic for
the same input. The pipeline relies on content-hash-based deduplication
and stable `MENTIONED_IN` edge targets. Use a hash of the chunk content
(or content + position) as the chunk ID.

### Step 4: Handle Concurrency (Extractor)

If implementing a custom `Extractor`, respect the `workers` parameter.
The pipeline passes this value from `Config.Workers`. Use a worker pool
or `sync.WaitGroup` to process chunks concurrently:

```go
func (e *MyExtractor) Extract(chunks []*models.Chunk, workers int) *models.CandidateGraph {
    sem := make(chan struct{}, workers)
    var mu sync.Mutex
    graph := &models.CandidateGraph{}

    var wg sync.WaitGroup
    for _, chunk := range chunks {
        wg.Add(1)
        sem <- struct{}{}
        go func(c *models.Chunk) {
            defer wg.Done()
            defer func() { <-sem }()

            result := e.processChunk(c)

            mu.Lock()
            graph.Merge(result)
            mu.Unlock()
        }(chunk)
    }
    wg.Wait()
    return graph
}
```

---

## Strategy Selection Guide

| Scenario | Chunker | Resolver | Notes |
|---|---|---|---|
| General text, quick start | `recursive` | `default` | Zero config, fast |
| Academic papers, books | `sentence` | `default` | Preserves sentence integrity |
| Markdown documentation | `structural` | `default` | Aligns with heading structure |
| High-quality RAG | `contextual` | `TieredResolver` | Best quality, highest cost |
| Entity-heavy corpus | `recursive` | `TieredResolver` | Catches name variations |
| Local/offline setup | `recursive` | `default` | No extra API calls |

| Cost-sensitive, large corpus | `recursive` | `default` | Use `--extraction-strategy hybrid` |
| Domain-specific entities | `recursive` | `TieredResolver` | Use `--extraction-strategy llm` (default) |

The `Canonicalizer` and `Reranker` defaults are appropriate for most
workloads. For the `Extractor`, choose between `llm` (higher quality,
2 LLM calls/chunk) and `hybrid` (lower cost, 1 LLM call/chunk).

---

## Interface Source

All five interfaces are defined in a single file for easy reference:

```
internal/pipeline/strategies.go
```

The default implementations are in the same file. Alternative
implementations live in their own files:

```
internal/chunker/chunker.go              -- defaultChunker (recursive)
internal/chunker/sentence.go             -- SentenceChunker
internal/chunker/structural.go           -- StructuralChunker
internal/chunker/contextual.go           -- ContextualChunker
internal/pipeline/resolver_tiered.go     -- TieredResolver
internal/pipeline/hybrid_extractor.go    -- HybridExtractor (NER + LLM)
internal/ner/client.go                   -- NER HTTP service client
```
