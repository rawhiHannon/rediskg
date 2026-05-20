package pipeline

import (
	"fmt"
	"log"
	"strings"

	"rediskg/internal/store"
)

// batchSize matches GraphRAG-SDK's GraphStore._BATCH_SIZE. 500 keeps the
// CYPHER prefix length well within FalkorDB's parser limits while still
// amortising the round-trip cost across many MERGEs per query.
const batchSize = 500

// entityRow is the per-item payload UNWIND'd into the entity-upsert query.
type entityRow struct {
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
}

// edgeRow is the per-item payload UNWIND'd into the edge-upsert query.
// Fields mirror models.EdgeRecord but flattened so they ride inside one
// Cypher map literal.
type edgeRow struct {
	From        string                 `json:"from"`
	To          string                 `json:"to"`
	FromType    string                 `json:"from_type,omitempty"`
	ToType      string                 `json:"to_type,omitempty"`
	Description string                 `json:"description"`
	Weight      float64                `json:"weight"`
	Inferred    bool                   `json:"inferred"`
	ChunkIDs    string                 `json:"chunk_ids"`
	Evidence    string                 `json:"evidence,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Condition   string                 `json:"condition,omitempty"`
	Temporal    map[string]interface{} `json:"temporal,omitempty"`
}

// writeEntitiesBatched groups entities by their PascalCase typed label and
// UNWINDs each group through one parameter-bound MERGE template. Same shape
// the old per-entity CreateEntity produced; just amortised.
func writeEntitiesBatched(s *store.FalkorStore, entities []entityRow) {
	if len(entities) == 0 {
		return
	}
	// Group by intended typed label so each batch sets the same extra label.
	byLabel := map[string][]entityRow{}
	order := []string{}
	for _, e := range entities {
		label := store.ToTypeLabel(e.Type)
		if _, ok := byLabel[label]; !ok {
			order = append(order, label)
		}
		byLabel[label] = append(byLabel[label], e)
	}

	total := 0
	for _, label := range order {
		group := byLabel[label]
		safeLabel := ""
		if label != "" && label != "Concept" {
			if cleaned, err := store.SanitizeCypherLabel(label); err == nil {
				safeLabel = cleaned
			}
		}
		extra := ""
		if safeLabel != "" {
			extra = " SET n:`" + safeLabel + "`"
		}
		query := "UNWIND $batch AS item " +
			"MERGE (n:Concept {name: item.name}) " +
			"SET n.type = item.type, n += item.properties" + extra

		for start := 0; start < len(group); start += batchSize {
			end := start + batchSize
			if end > len(group) {
				end = len(group)
			}
			rows := group[start:end]
			batch := make([]interface{}, len(rows))
			for i, r := range rows {
				batch[i] = map[string]interface{}{
					"name":       r.Name,
					"type":       r.Type,
					"properties": cleanPropsForCypher(r.Properties),
				}
			}
			if _, err := s.QueryWithParams(query, map[string]interface{}{"batch": batch}); err != nil {
				log.Printf("Warning: batch entity upsert failed (label=%s, n=%d): %v — falling back to per-item",
					safeLabel, len(rows), err)
				for _, r := range rows {
					p := cleanPropsForCypher(r.Properties)
					q := "MERGE (n:Concept {name: $name}) SET n.type = $type, n += $props" + extra
					if _, err := s.QueryWithParams(q, map[string]interface{}{
						"name":  r.Name,
						"type":  r.Type,
						"props": p,
					}); err != nil {
						log.Printf("Warning: failed to store entity %q: %v", r.Name, err)
					}
				}
				continue
			}
			total += len(rows)
		}
	}
	log.Printf("  Wrote %d entities (batched UNWIND, %d label group%s)",
		total, len(byLabel), pluralS(len(byLabel)))
}

// writeEdgesBatched groups edges by relation type and UNWINDs each group
// through one templated query. Preserves the existing on-create/on-match
// semantics: weight accumulates, chunk_ids append, evidence/condition dedup
// via CONTAINS, status keeps first non-empty value, temporal keys are set
// only on create (first-write wins per key — close enough to the old per-
// key CASE behaviour).
func writeEdgesBatched(s *store.FalkorStore, edges []edgeRow, relType string) {
	if len(edges) == 0 {
		return
	}
	safeRel, err := store.SanitizeCypherLabel(relType)
	if err != nil {
		log.Printf("Warning: invalid relation type %q, skipping %d edges", relType, len(edges))
		return
	}

	query := "UNWIND $batch AS item " +
		"MERGE (a:Concept {name: item.from}) " +
		"MERGE (b:Concept {name: item.to}) " +
		"SET a.type = CASE WHEN item.from_type = '' THEN a.type ELSE item.from_type END " +
		"SET b.type = CASE WHEN item.to_type = '' THEN b.type ELSE item.to_type END " +
		"MERGE (a)-[r:`" + safeRel + "`]->(b) " +
		"ON CREATE SET " +
		"  r.description = item.description, " +
		"  r.weight = item.weight, " +
		"  r.inferred = item.inferred, " +
		"  r.chunk_ids = item.chunk_ids, " +
		"  r.evidence = item.evidence, " +
		"  r.status = item.status, " +
		"  r.condition = item.condition, " +
		"  r.fact = item.fact, " +
		"  r += item.temporal " +
		"ON MATCH SET " +
		"  r.weight = r.weight + item.weight, " +
		"  r.chunk_ids = r.chunk_ids + ',' + item.chunk_ids, " +
		"  r.fact = CASE WHEN r.fact IS NULL OR r.fact = '' THEN item.fact ELSE r.fact END, " +
		"  r.evidence = CASE " +
		"    WHEN r.evidence IS NULL OR r.evidence = '' THEN item.evidence " +
		"    WHEN r.evidence CONTAINS item.evidence THEN r.evidence " +
		"    ELSE r.evidence + '\\n---\\n' + item.evidence END, " +
		"  r.status = CASE WHEN r.status IS NULL OR r.status = '' THEN item.status ELSE r.status END, " +
		"  r.condition = CASE " +
		"    WHEN r.condition IS NULL OR r.condition = '' THEN item.condition " +
		"    WHEN r.condition CONTAINS item.condition THEN r.condition " +
		"    ELSE r.condition + '\\n---\\n' + item.condition END"

	total := 0
	for start := 0; start < len(edges); start += batchSize {
		end := start + batchSize
		if end > len(edges) {
			end = len(edges)
		}
		rows := edges[start:end]
		batch := make([]interface{}, len(rows))
		for i, r := range rows {
			batch[i] = edgeRowToMap(r)
		}
		if _, err := s.QueryWithParams(query, map[string]interface{}{"batch": batch}); err != nil {
			log.Printf("Warning: batch edge upsert failed (rel=%s, n=%d): %v — falling back to per-item",
				safeRel, len(rows), err)
			for _, r := range rows {
				if _, err := s.QueryWithParams(query, map[string]interface{}{"batch": []interface{}{edgeRowToMap(r)}}); err != nil {
					log.Printf("Warning: failed to store edge %s -[%s]-> %s: %v",
						r.From, safeRel, r.To, err)
				}
			}
			continue
		}
		total += len(rows)
	}
	log.Printf("  Wrote %d [%s] edges (batched UNWIND)", total, safeRel)
}

func edgeRowToMap(r edgeRow) map[string]interface{} {
	temporal := map[string]interface{}{}
	for k, v := range r.Temporal {
		temporal[k] = v
	}
	// Pre-compute the human-readable fact string at write time so the
	// retrieval layer can embed and surface it without re-formatting.
	// "<src> —[<rel>]→ <tgt>: <evidence>" mirrors GraphRAG-SDK's format.
	fact := r.From + " —[" + r.Description + "]→ " + r.To
	ev := store.SanitizeControl(r.Evidence)
	if ev != "" {
		fact += ": " + ev
	}
	return map[string]interface{}{
		"from":        r.From,
		"to":          r.To,
		"from_type":   r.FromType,
		"to_type":     r.ToType,
		"description": r.Description,
		"weight":      r.Weight,
		"inferred":    r.Inferred,
		"chunk_ids":   r.ChunkIDs,
		"evidence":    ev,
		"status":      r.Status,
		"condition":   store.SanitizeControl(r.Condition),
		"temporal":    temporal,
		"fact":        fact,
	}
}

// cleanPropsForCypher strips nil values + control characters so the encoded
// map literal is safe to feed FalkorDB. Mirrors the upstream cleaning rule.
func cleanPropsForCypher(props map[string]interface{}) map[string]interface{} {
	if len(props) == 0 {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(props))
	for k, v := range props {
		if v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = store.SanitizeControl(x)
		case []string:
			cleaned := make([]string, 0, len(x))
			for _, item := range x {
				cleaned = append(cleaned, store.SanitizeControl(item))
			}
			if len(cleaned) > 0 {
				out[k] = cleaned
			}
		default:
			out[k] = v
		}
	}
	return out
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// sanitizePropertyKey mirrors store.sanitizePropertyKey for the rare cases
// where the pipeline constructs property keys from extracted text. Kept here
// so the pipeline doesn't reach into store internals.
func sanitizePropertyKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "prop"
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	cleaned := b.String()
	for strings.Contains(cleaned, "__") {
		cleaned = strings.ReplaceAll(cleaned, "__", "_")
	}
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		return "prop"
	}
	if cleaned[0] >= '0' && cleaned[0] <= '9' {
		cleaned = "p_" + cleaned
	}
	if len(cleaned) > 40 {
		cleaned = strings.TrimRight(cleaned[:40], "_")
	}
	return strings.ToLower(cleaned)
}

// unused: kept for future use when extracted property keys need sanitising
// outside the store package's reach.
var _ = sanitizePropertyKey
var _ = fmt.Sprintf
