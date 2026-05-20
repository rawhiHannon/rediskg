# Schema Governance in RedisKG

RedisKG uses a dynamic, LLM-driven schema governance system that prevents
unchecked type proliferation while remaining fully domain-agnostic. Unlike
systems that rely on a fixed ontology or accept every type the LLM proposes,
RedisKG introduces a three-layer trust model with heuristic pre-filtering and
LLM-based review.

This document covers the architecture, data flow, heuristics, alias system,
and persistence model.

---

## Three-Layer Trust Model

### Layer 1: Base Types (Upper Ontology Scaffold)

Base types form the top-level categories in the type hierarchy. They are
**configurable, not hardcoded** -- you can extend or replace them entirely
before initialization.

The default set (`DefaultBaseTypes` in `internal/schema/base.go`):

| Base Type        | Description                                              |
|------------------|----------------------------------------------------------|
| person           | A named individual (any profession, role, or identity)   |
| organization     | A company, institution, agency, network, or formal group |
| location         | A geographic place: city, region, country, neighborhood  |
| address          | A specific street address or postal location             |
| physical_object  | A tangible object, device, tool, or material             |
| product          | Something manufactured, sold, or traded                  |
| service          | Something offered, provided, performed, or sold          |
| event            | A named incident, meeting, occurrence, or time-bound happening |
| technology       | Software, system, platform, portal, or digital tool      |
| document         | A named document, policy, agreement, report, or record   |
| role             | A job title, function, or position held by a person      |
| quantity         | A measurable amount, metric, or numeric value with units |
| date_time        | A specific date, time, duration, or temporal reference   |
| law_or_policy    | A regulation, law, legal rule, or official policy        |
| concept          | An abstract topic, rule, category, or principle (fallback) |

To replace the defaults, call `Schema.SetBaseTypes()` before
`Schema.InitWithBaseTypes()`. Base types are seeded into the entity type
registry with `DomainType: false`.

Every domain type ultimately resolves to one or more base types via
`ResolveBaseType()`, which walks the parent chain upward. If no parent is
found, it falls back to `concept`.

### Layer 2: Domain Types (LLM-Proposed, Ungoverned)

During extraction, the LLM proposes entity and relation types that do not
exist in the current schema. These are **not automatically accepted**. They
enter the governance pipeline as candidates:

```go
type CandidateType struct {
    ProposedName  string   // e.g. "healthcare_provider"
    ProposedBases []string // e.g. ["organization"]
    Evidence      string   // context from extraction
    Decision      string   // filled by governance: "new", "synonym", "subtype", "too_vague", "invalid"
    CanonicalName string   // if synonym/subtype, maps to this existing type
    Confidence    float64
}
```

Relation candidates follow a similar structure with source/target type
constraints and a symmetry flag.

### Layer 3: Accepted Types (Governed Schema)

Types that pass governance become part of the active schema. They are stored
in `Schema.EntityTypes` and `Schema.RelationTypes` with full metadata:
description, parent type, base type mapping, aliases, and (for relations)
source/target constraints plus symmetry.

---

## Governance Flow

The governance pipeline runs in two stages -- fast heuristic pre-filtering
followed by LLM review for ambiguous cases.

### Flow Diagram

```
Proposed Type/Relation (from LLM extraction)
           |
           v
  +--------------------+
  | Heuristic Checks   |   <-- CheckProposedType / CheckProposedRelation
  | (no LLM call)      |       internal/schema/governance.go
  +--------------------+
           |
     +-----+-----+-----+-----+
     |           |           |
  Exact      Alias       Heuristic
  Match      Match       Match
  (1.0)      (1.0)       (0.7-0.85)
     |           |           |
     v           v           v
  Resolved    Resolved    NeedsLLM=true
  immediately immediately (candidate)
                             |
           +-----------------+------------------+
           |                                    |
           v                                    v
  +-----------------------+          +---------------------+
  | Too Vague / Invalid   |          | LLM Governance      |
  | (rejected or flagged) |          | Review               |
  +-----------------------+          | (GovernTypeCandidates |
                                     |  / GovernRelation-   |
                                     |  Candidates)         |
                                     | internal/llm/        |
                                     |   governance.go      |
                                     +---------------------+
                                              |
                          +-------------------+-------------------+
                          |          |            |               |
                          v          v            v               v
                        "new"    "synonym"    "subtype"     "too_vague"
                          |          |        "inverse"      "invalid"
                          |          |            |               |
                          v          v            v               v
                     ApproveType  Register    Register        Discarded
                     (domain      Alias       as domain
                      type with              type with
                      base types)            parent set
```

### Stage 1: Heuristic Pre-Filtering (No LLM)

`CheckProposedType(proposed string) GovernanceResult` performs the following
checks in order:

1. **Exact match** -- case-insensitive lookup in `EntityTypes`. If found,
   returns `synonym` with confidence 1.0 and the canonical name.

2. **Alias index lookup** -- checks `typeAliases` map. O(1). If found,
   returns `synonym` with confidence 1.0.

3. **Word-order variant detection** -- splits on underscores and compares
   token sets. "branch_office" and "office_branch" have identical token sets,
   so they match. Returns `synonym` with confidence 0.85, flagged
   `NeedsLLM: true` for confirmation.

4. **Token overlap (Jaccard similarity)** -- computes Jaccard similarity
   between the proposed type's tokens and every existing type's tokens. If
   the best match scores >= 0.7, returns `synonym` with the score as
   confidence, flagged `NeedsLLM: true`.

5. **Vague type filter** -- single-word types matching known vague terms
   ("thing", "stuff", "item", "entity", "object", "other", "misc") are
   flagged as `too_vague`.

6. **Fallback** -- if none of the above matched, returns `accept_new` with
   confidence 0.5 and `NeedsLLM: true`.

`CheckProposedRelation(proposed string) GovernanceResult` follows a parallel
structure with additional checks:

1. **Exact match** in `RelationTypes`.
2. **Alias index lookup** in `relationAliases`, including inverse detection
   (the `Flip` flag).
3. **Inverse pattern detection** -- looks for `_BY` suffix patterns. If a
   relation ends with `_BY`, strips the suffix and checks for active forms
   (e.g., `MANAGED_BY` checks for `MANAGES`, `MANAGE`, `MANAGES`). Also
   checks the reverse: if adding `_BY` matches an existing relation.
4. **Word-order variant** -- same token-set comparison as entity types.
5. **Verbosity filter** -- relations with 4+ underscore-separated words are
   flagged as `too_vague`.
6. **Fallback** -- `accept_new` with `NeedsLLM: true`.

### Stage 2: LLM Governance Review

Candidates with `NeedsLLM: true` are batched and sent to the LLM via
`GovernTypeCandidates()` or `GovernRelationCandidates()` in
`internal/llm/governance.go`.

**For entity types**, the LLM receives:
- The full list of currently accepted entity types (with descriptions,
  parents, and aliases)
- The base type scaffold
- All candidate types with their heuristic pre-decisions and evidence

The LLM classifies each candidate as one of:
- **new** -- genuinely new concept; must specify which base type(s) it
  belongs to
- **synonym** -- naming variant of an existing type; must specify the
  canonical type
- **subtype** -- more specific version of an existing type; must specify the
  parent
- **too_vague** -- too generic to be useful
- **invalid** -- noise

**For relation types**, the LLM receives:
- Currently accepted relations (with source/target constraints, aliases,
  symmetry flags)
- All candidate relations with observed source/target base types

The LLM classifies each as:
- **new** -- must specify source/target base types and whether symmetric
- **synonym** -- maps to an existing canonical relation
- **inverse** -- same relation with flipped direction
- **too_vague** / **invalid**

The LLM is instructed to be aggressive about merging -- a type or relation
should only be "new" if its meaning is truly unique.

### Stage 3: Approval

After LLM review, candidates are processed through `ApproveType()` or
`ApproveRelation()`:

**Entity type approval** (`ApproveType`):
- **synonym** -- registers a type alias mapping the proposed name to the
  canonical name
- **subtype** -- registers as a domain type with the canonical type as parent
- **new** -- registers as a domain type under the specified base type(s);
  supports multi-base mapping via `RegisterDomainTypeMultiBase()`

**Relation approval** (`ApproveRelation`):
- **synonym** -- registers a relation alias (no direction flip)
- **inverse** -- registers a relation alias with `Flip: true`, so triples
  using this name will have their source and target swapped during
  normalization
- **new** -- registers a full relation type with source/target type
  constraints and symmetry flag

---

## Alias System

The alias system provides O(1) normalization during extraction and triple
processing. Two separate indexes are maintained:

### Type Aliases

```
typeAliases map[string]string    // alias -> canonical type name
```

- Registered via `RegisterTypeAlias(alias, canonical)`
- Queried via `ResolveTypeName(name)` or `NormalizeEntityType(name)`
- The canonical type's `Aliases` slice is also updated for LLM prompt
  visibility

### Relation Aliases

```
relationAliases map[string]RelationAliasInfo   // alias -> {Canonical, Flip}
```

- The `Flip` field distinguishes synonyms from inverses
- Registered via `RegisterRelationAlias(alias, canonical, isInverse)`
- Queried via `NormalizeTripleRelation(name)` which returns both the
  canonical name and whether to flip the triple direction
- `IsRelationInverse(name)` provides a quick check

### Triple Normalization

During the normalization pipeline phase, every triple's relation is resolved
through the alias index:

```go
canonical, shouldFlip := schema.NormalizeTripleRelation(triple.Relation)
triple.Relation = canonical
if shouldFlip {
    triple.Source, triple.Target = triple.Target, triple.Source
}
```

Entity types are similarly normalized:

```go
triple.SourceType = schema.NormalizeEntityType(triple.SourceType)
triple.TargetType = schema.NormalizeEntityType(triple.TargetType)
```

---

## Schema Persistence

Schema types are persisted as labeled nodes in FalkorDB using the
`__Schema__` label prefix:

- **Entity types**: `(:__Schema__:__EntityType__ {name, description, parent_type})`
- **Relation types**: `(:__Schema__:__RelationType__ {name, description, source_types, target_types, symmetric})`

These nodes are excluded from normal graph queries via
`WHERE NOT n:__Schema__`.

### Configuration Flags

Two flags in `pkg/config/config.go` control persistence behavior:

| Flag                  | Default | Effect                                      |
|-----------------------|---------|---------------------------------------------|
| `PersistSchema`       | false   | Save and load schema between pipeline runs  |
| `ResetSchemaOnIngest` | true    | Ignore persisted schema, start fresh        |

When `PersistSchema` is enabled and `ResetSchemaOnIngest` is false, the
pipeline loads previously accepted types and relations from FalkorDB at
startup, allowing the schema to accumulate knowledge across multiple
ingestion runs.

---

## Type Hierarchy and Compatibility

The schema supports a type hierarchy through the `ParentType` field. This
hierarchy is used during triple validation:

- `ValidateTripleDirection()` checks whether a triple's source and target
  types are compatible with the relation's constraints
- Compatibility walks up the parent chain: if a relation allows
  `organization` as source, then `healthcare_provider` (a subtype of
  `organization`) is also valid
- Multi-base types (via `BaseTypes` field) are also checked
- If constraints fail in the original direction but pass when flipped, the
  system returns `"flip"` to correct the triple direction

---

## Key Source Files

| File                              | Purpose                                    |
|-----------------------------------|--------------------------------------------|
| `internal/schema/schema.go`      | Core Schema struct, type/relation storage, alias indexes, resolution logic |
| `internal/schema/base.go`        | Base type definitions, `DefaultBaseTypes`, `SetBaseTypes`, `ResolveBaseType` |
| `internal/schema/governance.go`  | Heuristic checks (`CheckProposedType`, `CheckProposedRelation`), approval functions, normalization, Jaccard similarity |
| `internal/llm/governance.go`     | LLM governance prompts and candidate review (`GovernTypeCandidates`, `GovernRelationCandidates`) |
| `internal/store/falkor.go`       | Schema persistence to/from FalkorDB        |
| `pkg/config/config.go`           | `PersistSchema` and `ResetSchemaOnIngest` flags |
| `internal/pipeline/pipeline.go`  | Pipeline phases that invoke governance (phases 8-10) |

---

## Design Rationale

**Why not accept all LLM-proposed types?**
Without governance, extraction from multiple documents produces massive type
proliferation: "healthcare_provider", "health_care_provider",
"medical_provider", "provider_healthcare" all mean the same thing. This
pollutes the graph and breaks queries.

**Why not use a fixed ontology?**
A fixed ontology cannot adapt to arbitrary domains. RedisKG is designed to
work on any document corpus -- medical, legal, financial, technical -- so the
schema must emerge from the data while remaining controlled.

**Why two-stage (heuristic + LLM)?**
Heuristics catch the easy cases (exact matches, aliases, word reorderings)
without spending LLM tokens. The LLM only reviews genuinely ambiguous
candidates. This reduces cost and latency while maintaining quality.

**Why aggressive merging?**
Fewer, well-defined types produce a more queryable and navigable graph.
The LLM governance prompt explicitly instructs aggressive merging -- a new
type should only be created if its meaning is truly distinct from everything
already accepted.
