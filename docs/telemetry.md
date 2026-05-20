# RedisKG Telemetry and Observability

Guide to monitoring pipeline execution, tracking phase progress, and integrating
real-time updates into frontend applications.

---

## PipelineStats Overview

The `PipelineStats` struct (defined in `internal/pipeline/telemetry.go`) captures
the full state of a pipeline run: phase-level timing, status transitions, and
aggregate counters.

### Aggregate Counters

| Counter | Description |
|---------|-------------|
| `documents` | Number of source documents ingested |
| `chunks` | Total chunks produced by the chunker |
| `entities_extracted` | Raw entities returned by the extractor |
| `edges_extracted` | Raw edges returned by the extractor |
| `entities_canonical` | Entities remaining after deduplication |
| `alias_mappings` | Number of alias-to-canonical mappings created |
| `edges_after_solver` | Edges remaining after schema validation and solving |
| `final_entities` | Entities written to the graph |
| `final_edges` | Edges written to the graph |
| `graph_nodes` | Total node count in FalkorDB after the run |
| `graph_edges` | Total edge count in FalkorDB after the run |

### Phase Statuses

Each pipeline phase carries one of the following statuses:

| Status | Meaning |
|--------|---------|
| `pending` | Phase has not started yet |
| `running` | Phase is currently executing |
| `completed` | Phase finished successfully |
| `skipped` | Phase was skipped (not applicable to this run) |
| `failed` | Phase encountered an error |

### Phase List

The pipeline tracks these phases in order:

| # | Phase Name | Description |
|---|-----------|-------------|
| 1 | Init schema | Load base types and persisted schema from FalkorDB |
| 2 | Chunking documents | Split documents into overlapping chunks |
| 3 | Entity & relation extraction | LLM-based extraction with concurrent workers |
| 4 | Noise filtering | Remove low-confidence or malformed extractions |
| 5 | Entity name standardization | LLM deduplication of entity names |
| 6 | Entity profile building | Construct global registry with all evidence |
| 7 | Entity type resolution | LLM-based type assignment using full evidence |
| 8 | Entity type governance | Candidate types through heuristic + LLM approval |
| 9 | Relation type governance | Candidate relations through heuristic + LLM approval |
| 10 | Triple normalization | Apply aliases and flip inverse relations |
| 11 | Schema validation | Type propagation and constraint checking |
| 12 | Rich verification | LLM verification with profiles and evidence |
| 13 | Merge & store | Write to FalkorDB and generate embeddings |

---

## REST Endpoint: GET /api/pipeline/stats

Returns a JSON snapshot of the current or most recent pipeline run.

**Response schema:**

| Field | Type | Description |
|-------|------|-------------|
| `run_id` | string | Unique run identifier (e.g., `ingest_1716307200000`) |
| `status` | string | Overall status: `running`, `completed`, or `failed` |
| `duration` | string | Total elapsed time (e.g., `"45.3s"`) |
| `phases` | array | Ordered list of phase objects |
| `phases[].name` | string | Human-readable phase name |
| `phases[].status` | string | Phase status (see table above) |
| `phases[].duration` | string | Phase elapsed time |
| `phases[].details` | string | Summary (e.g., `"42 chunks"`, `"156 entities, 210 edges"`) |
| `counts` | object | Aggregate counters (see table above) |

**Example response:**

```json
{
  "run_id": "ingest_1716307200000",
  "status": "completed",
  "duration": "45.3s",
  "phases": [
    {
      "name": "Chunking documents",
      "status": "completed",
      "duration": "120ms",
      "details": "42 chunks"
    },
    {
      "name": "Entity & relation extraction",
      "status": "completed",
      "duration": "38.2s",
      "details": "156 entities, 210 edges"
    },
    {
      "name": "Entity name standardization",
      "status": "completed",
      "duration": "2.1s",
      "details": "156 -> 45 entities, 18 alias mappings"
    },
    {
      "name": "Merge & store",
      "status": "completed",
      "duration": "3.8s",
      "details": "48 nodes, 67 edges"
    }
  ],
  "counts": {
    "documents": 3,
    "chunks": 42,
    "entities_extracted": 156,
    "edges_extracted": 210,
    "entities_canonical": 45,
    "alias_mappings": 18,
    "edges_after_solver": 72,
    "final_entities": 45,
    "final_edges": 67,
    "graph_nodes": 48,
    "graph_edges": 67
  }
}
```

---

## SSE Endpoint: GET /api/pipeline/events

Server-Sent Events stream that pushes real-time progress updates to connected
clients. The connection stays open for the duration of the pipeline run.

### Event Types

| Event | Payload | When |
|-------|---------|------|
| `snapshot` | Full `PipelineStats` JSON | Immediately on connection |
| `progress` | Full `PipelineStats` JSON | On every phase status transition |
| `done` | `{"run_id": "...", "status": "completed"}` | Pipeline finished (success or failure) |

### Client Integration (JavaScript)

```javascript
const es = new EventSource('/api/pipeline/events');

es.addEventListener('snapshot', (e) => {
  const stats = JSON.parse(e.data);
  renderPipelineStatus(stats);
});

es.addEventListener('progress', (e) => {
  const stats = JSON.parse(e.data);
  updatePhaseIndicators(stats.phases);
  updateCounters(stats.counts);
});

es.addEventListener('done', (e) => {
  const result = JSON.parse(e.data);
  showCompletionBanner(result.status);
  es.close();
});

es.onerror = () => {
  console.error('SSE connection lost');
  es.close();
};
```

### Client Integration (Go)

```go
resp, err := http.Get("http://localhost:8080/api/pipeline/events")
if err != nil {
    log.Fatal(err)
}
defer resp.Body.Close()

scanner := bufio.NewScanner(resp.Body)
for scanner.Scan() {
    line := scanner.Text()
    if strings.HasPrefix(line, "data: ") {
        payload := line[6:]
        var stats PipelineStats
        json.Unmarshal([]byte(payload), &stats)
        fmt.Printf("Phase: %s -- %s\n", stats.Phases[len(stats.Phases)-1].Name, stats.Status)
    }
}
```

---

## Programmatic Access

From Go code, subscribe to stats updates via the pipeline's channel-based API:

```go
p := pipeline.New(cfg, store, llmClient)

ch := p.SubscribeStats()
go func() {
    for msg := range ch {
        var stats pipeline.PipelineStats
        json.Unmarshal(msg, &stats)
        log.Printf("[%s] %s -- %d entities, %d edges",
            stats.RunID, stats.Status,
            stats.Counts.FinalEntities, stats.Counts.FinalEdges)
    }
}()

err := p.Ingest(docs)
```

The channel emits a JSON-encoded `PipelineStats` snapshot on every phase
transition. It is closed automatically when the pipeline run completes.
