package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rediskg/pkg/config"
	"rediskg/pkg/models"

	"github.com/redis/go-redis/v9"
)

var ctx = context.Background()

// FalkorStore handles all FalkorDB graph operations.
type FalkorStore struct {
	client    *redis.Client
	graphName string
	cfg       *config.Config
}

// New creates a new FalkorStore and verifies the connection.
func New(cfg *config.Config) (*FalkorStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.RedisAddr,
		Password:     "",
		DB:           0,
		PoolSize:     100,
		ReadTimeout:  2 * time.Minute,
		WriteTimeout: 1 * time.Minute,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	// Verify FalkorDB module is loaded by running a lightweight graph command
	_, probeErr := client.Do(ctx, "GRAPH.QUERY", "__rediskg_probe__", "RETURN 1").Result()
	if probeErr != nil && !strings.Contains(probeErr.Error(), "GRAPH") {
		return nil, fmt.Errorf("FalkorDB module not loaded in Redis: %w", probeErr)
	}
	// Clean up probe graph
	client.Do(ctx, "GRAPH.DELETE", "__rediskg_probe__")

	return &FalkorStore{
		client:    client,
		graphName: cfg.GraphName,
		cfg:       cfg,
	}, nil
}

// Query executes a Cypher query and returns the raw result.
func (s *FalkorStore) Query(cypher string) (interface{}, error) {
	return s.client.Do(ctx, "GRAPH.QUERY", s.graphName, cypher).Result()
}

// ROQuery executes a read-only Cypher query.
func (s *FalkorStore) ROQuery(cypher string) (interface{}, error) {
	return s.client.Do(ctx, "GRAPH.RO_QUERY", s.graphName, cypher).Result()
}

// CreateEntity creates or merges an entity node in the graph with properties.
// Always updates type and properties (not just on create) so validated data overwrites earlier values.
// Adds the base type as an additional label (e.g., :Concept:Organization).
func (s *FalkorStore) CreateEntity(entity models.Entity) error {
	name := escapeCypher(strings.ToLower(strings.TrimSpace(entity.Name)))
	entityType := escapeCypher(strings.ToLower(strings.TrimSpace(entity.Type)))
	if entityType == "" {
		entityType = "concept"
	}

	// Build SET parts for properties
	setParts := []string{fmt.Sprintf("n.type = '%s'", entityType)}
	for k, v := range entity.Properties {
		key := sanitizePropertyKey(k)
		if key == "" || key == "name" || key == "type" {
			continue // skip reserved keys
		}
		val := escapeCypher(fmt.Sprint(v))
		setParts = append(setParts, fmt.Sprintf("n.%s = '%s'", key, val))
	}
	setClause := strings.Join(setParts, ", ")

	cypher := fmt.Sprintf(
		`MERGE (n:Concept {name: '%s'}) SET %s`,
		name, setClause,
	)

	if _, err := s.Query(cypher); err != nil {
		return err
	}

	// Add base type as additional label for typed queries (e.g., MATCH (p:Person))
	// Prefer BaseType over Type to ensure we label with the upper-ontology type
	labelSource := strings.ToLower(strings.TrimSpace(entity.BaseType))
	if labelSource == "" {
		labelSource = entityType
	}
	typeLabel := toTypeLabel(labelSource)
	if typeLabel != "" && typeLabel != "Concept" {
		labelCypher := fmt.Sprintf(
			`MATCH (n:Concept {name: '%s'}) SET n:%s`,
			name, typeLabel,
		)
		if _, err := s.Query(labelCypher); err != nil {
			// Non-fatal: label addition is an optimization
			_ = err
		}
	}

	return nil
}

// ToTypeLabel exposes toTypeLabel to callers in other packages (used by the
// batched ingest writer to group entities by their PascalCase typed label).
func ToTypeLabel(baseType string) string { return toTypeLabel(baseType) }

// toTypeLabel converts a base type string to a valid Cypher label (PascalCase).
func toTypeLabel(baseType string) string {
	if baseType == "" {
		return ""
	}
	// Capitalize first letter of each word
	parts := strings.Split(baseType, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	label := strings.Join(parts, "")
	// Validate: must start with letter
	if len(label) == 0 || (label[0] < 'A' || label[0] > 'Z') {
		return ""
	}
	return label
}

// CreateEdge creates or merges a relationship in the graph.
// Always updates entity types so validated types overwrite bad earlier ones.
func (s *FalkorStore) CreateEdge(record models.EdgeRecord) error {
	node1 := strings.ToLower(strings.TrimSpace(record.Node1))
	node2 := strings.ToLower(strings.TrimSpace(record.Node2))
	if node1 == node2 {
		return nil // skip self-referencing edges
	}
	n1 := escapeCypher(node1)
	n2 := escapeCypher(node2)
	edgeType := toEdgeType(record.Edge)
	edgeDesc := escapeCypher(record.Edge)
	n1Type := escapeCypher(strings.ToLower(strings.TrimSpace(record.Node1Type)))
	n2Type := escapeCypher(strings.ToLower(strings.TrimSpace(record.Node2Type)))

	// Build type SET clauses (applied after both MERGEs, always overwrites)
	var typeSets []string
	if n1Type != "" {
		typeSets = append(typeSets, fmt.Sprintf("a.type = '%s'", n1Type))
	}
	if n2Type != "" {
		typeSets = append(typeSets, fmt.Sprintf("b.type = '%s'", n2Type))
	}
	typeSetClause := ""
	if len(typeSets) > 0 {
		typeSetClause = fmt.Sprintf(" SET %s", strings.Join(typeSets, ", "))
	}

	evidenceStr := escapeCypher(record.Evidence)
	statusStr := escapeCypher(record.Status)
	conditionStr := escapeCypher(record.Condition)

	// Build optional ON CREATE SET clauses for status, condition, evidence
	extraCreate := ""
	if evidenceStr != "" {
		extraCreate += fmt.Sprintf(", r.evidence = '%s'", evidenceStr)
	}
	if statusStr != "" {
		extraCreate += fmt.Sprintf(", r.status = '%s'", statusStr)
	}
	if conditionStr != "" {
		extraCreate += fmt.Sprintf(", r.condition = '%s'", conditionStr)
	}
	for k, v := range record.Temporal {
		key := sanitizePropertyKey(k)
		if key == "" {
			continue
		}
		val := escapeCypher(strings.TrimSpace(v))
		if val == "" {
			continue
		}
		extraCreate += fmt.Sprintf(", r.%s = '%s'", key, val)
	}

	// Build optional ON MATCH SET clauses for evidence, status, condition
	extraMatch := ""
	if evidenceStr != "" {
		extraMatch += fmt.Sprintf(", r.evidence = CASE WHEN r.evidence IS NULL OR r.evidence = '' THEN '%s' WHEN r.evidence CONTAINS '%s' THEN r.evidence ELSE r.evidence + '\\n---\\n' + '%s' END", evidenceStr, evidenceStr, evidenceStr)
	}
	if statusStr != "" {
		extraMatch += fmt.Sprintf(", r.status = CASE WHEN r.status IS NULL OR r.status = '' THEN '%s' ELSE r.status END", statusStr)
	}
	if conditionStr != "" {
		extraMatch += fmt.Sprintf(", r.condition = CASE WHEN r.condition IS NULL OR r.condition = '' THEN '%s' WHEN r.condition CONTAINS '%s' THEN r.condition ELSE r.condition + '\\n---\\n' + '%s' END", conditionStr, conditionStr, conditionStr)
	}
	for k, v := range record.Temporal {
		key := sanitizePropertyKey(k)
		if key == "" {
			continue
		}
		val := escapeCypher(strings.TrimSpace(v))
		if val == "" {
			continue
		}
		extraMatch += fmt.Sprintf(", r.%s = CASE WHEN r.%s IS NULL OR r.%s = '' THEN '%s' ELSE r.%s END", key, key, key, val, key)
	}

	cypher := fmt.Sprintf(
		`MERGE (a:Concept {name: '%s'})
		 ON CREATE SET a.name = '%s'
		 MERGE (b:Concept {name: '%s'})
		 ON CREATE SET b.name = '%s'%s
		 MERGE (a)-[r:%s]->(b)
		 ON CREATE SET r.description = '%s', r.weight = %f, r.inferred = %t, r.chunk_ids = '%s'%s
		 ON MATCH SET r.weight = r.weight + %f, r.chunk_ids = r.chunk_ids + ',%s'%s`,
		n1, n1,
		n2, n2, typeSetClause,
		edgeType, edgeDesc,
		record.Weight, record.Inferred, strings.Join(record.ChunkIDs, ","), extraCreate,
		record.Weight, strings.Join(record.ChunkIDs, ","), extraMatch,
	)

	_, err := s.Query(cypher)
	return err
}

// SetEntityEmbedding stores a vector embedding on an entity node.
func (s *FalkorStore) SetEntityEmbedding(name string, embedding []float32) error {
	vecStr := float32SliceToVecStr(embedding)
	cypher := fmt.Sprintf(
		`MATCH (n:Concept {name: '%s'}) SET n.embedding = vecf32(%s)`,
		escapeCypher(name), vecStr,
	)
	_, err := s.Query(cypher)
	return err
}

// CreateVectorIndex creates a vector index on entity embeddings for similarity search.
func (s *FalkorStore) CreateVectorIndex(label, property string, dimension int) error {
	cypher := fmt.Sprintf(
		`CREATE VECTOR INDEX FOR (n:%s) ON (n.%s) OPTIONS {dimension: %d, similarityFunction: 'cosine'}`,
		label, property, dimension,
	)
	_, err := s.Query(cypher)
	return err
}

// CreateFulltextIndex creates a fulltext index on a node property for text search.
// FalkorDB uses RediSearch under the hood; the index is scoped to (label, property).
func (s *FalkorStore) CreateFulltextIndex(label, property string) error {
	cypher := fmt.Sprintf(
		`CREATE FULLTEXT INDEX FOR (n:%s) ON (n.%s)`,
		label, property,
	)
	_, err := s.Query(cypher)
	return err
}

// FulltextSearch runs a fulltext query against a (label, property) index.
// Returns matching node names. The query string supports RediSearch syntax.
func (s *FalkorStore) FulltextSearch(label, property, query string, k int) ([]string, error) {
	escaped := escapeFulltextQuery(query)
	cypher := fmt.Sprintf(
		`CALL db.idx.fulltext.queryNodes('%s', '%s') YIELD node RETURN node.name LIMIT %d`,
		label, escaped, k,
	)
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	return parseStringResults(result), nil
}

// FulltextSearchChunks runs a fulltext query on Chunk.text and returns matching chunks.
func (s *FalkorStore) FulltextSearchChunks(query string, k int) ([]ChunkSimilarity, error) {
	escaped := escapeFulltextQuery(query)
	cypher := fmt.Sprintf(
		`CALL db.idx.fulltext.queryNodes('Chunk', '%s') YIELD node RETURN node.id, node.text, 1.0 AS score LIMIT %d`,
		escaped, k,
	)
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	var out []ChunkSimilarity
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 2 {
		return out, nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return out, nil
	}
	for _, r := range rows {
		cols, ok := r.([]interface{})
		if !ok || len(cols) < 3 {
			continue
		}
		id, _ := cols[0].(string)
		text, _ := cols[1].(string)
		score := 1.0
		if v, ok := cols[2].(float64); ok {
			score = v
		}
		out = append(out, ChunkSimilarity{ID: id, Text: text, Score: score})
	}
	return out, nil
}

// escapeFulltextQuery escapes special RediSearch characters for literal matching.
func escapeFulltextQuery(q string) string {
	special := []string{`\`, `'`, `@`, `!`, `{`, `}`, `(`, `)`, `|`, `-`, `=`, `>`, `[`, `]`, `:`, `;`, `~`, `*`}
	for _, ch := range special {
		q = strings.ReplaceAll(q, ch, `\`+ch)
	}
	return q
}

// FindSimilarEntities uses vector similarity to find entities close to the given embedding.
func (s *FalkorStore) FindSimilarEntities(label, property string, embedding []float32, k int) ([]string, error) {
	vecStr := float32SliceToVecStr(embedding)
	cypher := fmt.Sprintf(
		`CALL db.idx.vector.queryNodes('%s', '%s', %d, vecf32(%s)) YIELD node, score RETURN node.name, score`,
		label, property, k, vecStr,
	)

	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}

	return parseStringResults(result), nil
}

// SetChunkEmbedding stores a vector embedding on a Chunk node, addressed by
// the chunk's stable id (not name).
func (s *FalkorStore) SetChunkEmbedding(chunkID string, embedding []float32) error {
	vecStr := float32SliceToVecStr(embedding)
	cypher := fmt.Sprintf(
		`MATCH (c:Chunk {id: '%s'}) SET c.embedding = vecf32(%s)`,
		escapeCypher(chunkID), vecStr,
	)
	_, err := s.Query(cypher)
	return err
}

// ChunkSimilarity is one result row from FindSimilarChunks.
type ChunkSimilarity struct {
	ID    string
	Text  string
	Score float64
}

// FindSimilarChunks returns the top-k Chunk nodes by cosine similarity.
func (s *FalkorStore) FindSimilarChunks(embedding []float32, k int) ([]ChunkSimilarity, error) {
	vecStr := float32SliceToVecStr(embedding)
	cypher := fmt.Sprintf(
		`CALL db.idx.vector.queryNodes('Chunk', 'embedding', %d, vecf32(%s)) YIELD node, score RETURN node.id, node.text, score`,
		k, vecStr,
	)
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	var out []ChunkSimilarity
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 2 {
		return out, nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return out, nil
	}
	for _, r := range rows {
		cols, ok := r.([]interface{})
		if !ok || len(cols) < 3 {
			continue
		}
		id, _ := cols[0].(string)
		text, _ := cols[1].(string)
		score := 0.0
		switch v := cols[2].(type) {
		case float64:
			score = v
		case int64:
			score = float64(v)
		}
		out = append(out, ChunkSimilarity{ID: id, Text: text, Score: score})
	}
	return out, nil
}

// CreateEdgeVectorIndex creates a vector index on a relationship property
// for similarity search. Per-rel-type because FalkorDB scopes vector indexes
// to a specific (type, property) pair.
func (s *FalkorStore) CreateEdgeVectorIndex(relType, property string, dimension int) error {
	cypher := fmt.Sprintf(
		`CREATE VECTOR INDEX FOR ()-[r:%s]-() ON (r.%s) OPTIONS {dimension: %d, similarityFunction: 'cosine'}`,
		relType, property, dimension,
	)
	_, err := s.Query(cypher)
	return err
}

// EdgeFactSimilarity is one result row from FindSimilarEdgeFacts.
type EdgeFactSimilarity struct {
	From       string
	To         string
	RelType    string
	Fact       string
	Score      float64
}

// FindSimilarEdgeFacts queries the per-rel-type edge vector indexes and
// returns the top-k matches merged across all listed relation types. FalkorDB
// scopes vector indexes by relationship type, so we have to query each type
// separately and merge — done with `UNION ALL` inside a single Cypher call
// to keep the round-trip count low.
func (s *FalkorStore) FindSimilarEdgeFacts(relTypes []string, embedding []float32, k int) ([]EdgeFactSimilarity, error) {
	if len(relTypes) == 0 {
		return nil, nil
	}
	vecStr := float32SliceToVecStr(embedding)
	parts := make([]string, 0, len(relTypes))
	for _, rt := range relTypes {
		sub := fmt.Sprintf(
			`CALL db.idx.vector.queryRelationships('%s', 'embedding', %d, vecf32(%s)) YIELD relationship, score `+
				`MATCH (a)-[r]->(b) WHERE id(r) = id(relationship) `+
				`RETURN a.name AS src, type(r) AS rel_type, b.name AS tgt, COALESCE(r.fact, '') AS fact, score`,
			rt, k, vecStr,
		)
		parts = append(parts, sub)
	}
	cypher := strings.Join(parts, " UNION ")
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	var out []EdgeFactSimilarity
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 2 {
		return out, nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return out, nil
	}
	for _, r := range rows {
		cols, ok := r.([]interface{})
		if !ok || len(cols) < 5 {
			continue
		}
		src, _ := cols[0].(string)
		rt, _ := cols[1].(string)
		tgt, _ := cols[2].(string)
		fact, _ := cols[3].(string)
		score := 0.0
		switch v := cols[4].(type) {
		case float64:
			score = v
		case int64:
			score = float64(v)
		}
		out = append(out, EdgeFactSimilarity{From: src, To: tgt, RelType: rt, Fact: fact, Score: score})
	}
	// Sort descending by score and trim to k overall.
	sortEdgeFactsDesc(out)
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func sortEdgeFactsDesc(s []EdgeFactSimilarity) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Score > s[j-1].Score; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// GetAllNodes returns all node names and types.
func (s *FalkorStore) GetAllNodes() ([]map[string]interface{}, error) {
	cypher := `MATCH (n) WHERE NOT n:__Schema__ RETURN n.name, labels(n), n.type ORDER BY n.name`
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	return parseMapResults(result), nil
}

// GetNeighbors returns nodes connected to the given node name.
func (s *FalkorStore) GetNeighbors(name string, depth int) ([]map[string]interface{}, error) {
	cypher := fmt.Sprintf(
		`MATCH (n {name: '%s'})-[r*1..%d]-(m) RETURN DISTINCT m.name, labels(m), type(r)`,
		escapeCypher(name), depth,
	)
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}
	return parseMapResults(result), nil
}

// GetGraphStats returns basic graph statistics.
func (s *FalkorStore) GetGraphStats() (map[string]int64, error) {
	stats := map[string]int64{}

	nodeResult, err := s.ROQuery(`MATCH (n) WHERE NOT n:__Schema__ RETURN count(n)`)
	if err == nil {
		if count := parseCount(nodeResult); count >= 0 {
			stats["nodes"] = count
		}
	}

	edgeResult, err := s.ROQuery(`MATCH ()-[r]->() RETURN count(r)`)
	if err == nil {
		if count := parseCount(edgeResult); count >= 0 {
			stats["edges"] = count
		}
	}

	return stats, nil
}

// DeleteGraph deletes the entire graph. Returns nil if the graph doesn't exist.
func (s *FalkorStore) DeleteGraph() error {
	_, err := s.client.Do(ctx, "GRAPH.DELETE", s.graphName).Result()
	if err != nil && strings.Contains(err.Error(), "empty key") {
		return nil // graph doesn't exist, that's fine
	}
	return err
}

// SaveSchema persists the schema (entity types + relation types) as special nodes in the graph.
func (s *FalkorStore) SaveSchema(entityTypes map[string]struct{ Desc, Parent string }, relationTypes map[string]struct {
	Desc        string
	SourceTypes []string
	TargetTypes []string
	Symmetric   bool
}) error {
	for name, et := range entityTypes {
		cypher := fmt.Sprintf(
			`MERGE (s:__Schema__:__EntityType__ {name: '%s'}) SET s.description = '%s', s.parent_type = '%s'`,
			escapeCypher(name), escapeCypher(et.Desc), escapeCypher(et.Parent),
		)
		if _, err := s.Query(cypher); err != nil {
			return fmt.Errorf("failed to save entity type %s: %w", name, err)
		}
	}

	for name, rt := range relationTypes {
		cypher := fmt.Sprintf(
			`MERGE (s:__Schema__:__RelationType__ {name: '%s'}) SET s.description = '%s', s.source_types = '%s', s.target_types = '%s', s.symmetric = %t`,
			escapeCypher(name),
			escapeCypher(rt.Desc),
			escapeCypher(strings.Join(rt.SourceTypes, ",")),
			escapeCypher(strings.Join(rt.TargetTypes, ",")),
			rt.Symmetric,
		)
		if _, err := s.Query(cypher); err != nil {
			return fmt.Errorf("failed to save relation type %s: %w", name, err)
		}
	}

	return nil
}

// LoadSchemaEntityTypes loads persisted entity type definitions from the graph.
func (s *FalkorStore) LoadSchemaEntityTypes() ([]map[string]string, error) {
	cypher := `MATCH (s:__Schema__:__EntityType__) RETURN s.name, s.description, s.parent_type`
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}

	var types []map[string]string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 3 {
					m := map[string]string{
						"name":        safeString(cols[0]),
						"description": safeString(cols[1]),
						"parent_type": safeString(cols[2]),
					}
					types = append(types, m)
				}
			}
		}
	}
	return types, nil
}

// LoadSchemaRelationTypes loads persisted relation type definitions from the graph.
func (s *FalkorStore) LoadSchemaRelationTypes() ([]map[string]string, error) {
	cypher := `MATCH (s:__Schema__:__RelationType__) RETURN s.name, s.description, s.source_types, s.target_types, s.symmetric`
	result, err := s.ROQuery(cypher)
	if err != nil {
		return nil, err
	}

	var types []map[string]string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 5 {
					m := map[string]string{
						"name":         safeString(cols[0]),
						"description":  safeString(cols[1]),
						"source_types": safeString(cols[2]),
						"target_types": safeString(cols[3]),
						"symmetric":    safeString(cols[4]),
					}
					types = append(types, m)
				}
			}
		}
	}
	return types, nil
}

func safeString(v interface{}) string {
	if v == nil {
		return ""
	}
	if str, ok := v.(string); ok {
		return str
	}
	return fmt.Sprint(v)
}

// Close closes the Redis connection.
func (s *FalkorStore) Close() error {
	return s.client.Close()
}

// escapeCypher escapes single quotes and semicolons for Cypher strings.
func escapeCypher(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	s = strings.ReplaceAll(s, `;`, `,`)
	return s
}

// sanitizePropertyKey converts a string into a valid Cypher property key.
// Only allows alphanumeric characters and underscores; replaces everything else.
func sanitizePropertyKey(key string) string {
	key = strings.TrimSpace(key)
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, key)
	// Collapse multiple underscores
	for strings.Contains(result, "__") {
		result = strings.ReplaceAll(result, "__", "_")
	}
	result = strings.Trim(result, "_")
	// Cypher property keys can't start with a digit
	if len(result) > 0 && result[0] >= '0' && result[0] <= '9' {
		result = "p_" + result
	}
	if result == "" {
		return "prop"
	}
	// Truncate long keys (e.g. "agreement_status_with_CedarGate_Health_Network")
	if len(result) > 40 {
		result = result[:40]
		result = strings.TrimRight(result, "_")
	}
	return strings.ToLower(result)
}

// toEdgeType converts a relationship description to a valid Cypher relationship type.
// It keeps only the first few words (up to 4) to avoid verbose edge types.
func toEdgeType(s string) string {
	s = strings.ToUpper(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, s)
	// Trim leading/trailing underscores and collapse multiples
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' })

	result := strings.Join(parts, "_")
	if result == "" {
		return "RELATES_TO"
	}
	// Cypher relationship types cannot start with a digit
	if result[0] >= '0' && result[0] <= '9' {
		result = "R_" + result
	}
	return result
}

func float32SliceToVecStr(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%f", f)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func parseStringResults(result interface{}) []string {
	var names []string
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) > 0 {
					if name, ok := cols[0].(string); ok {
						names = append(names, name)
					}
				}
			}
		}
	}
	return names
}

func parseMapResults(result interface{}) []map[string]interface{} {
	var results []map[string]interface{}
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok {
					m := map[string]interface{}{}
					for i, col := range cols {
						m[fmt.Sprintf("col_%d", i)] = col
					}
					results = append(results, m)
				}
			}
		}
	}
	return results
}

func parseCount(result interface{}) int64 {
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok && len(rows) > 0 {
			if cols, ok := rows[0].([]interface{}); ok && len(cols) > 0 {
				switch v := cols[0].(type) {
				case int64:
					return v
				case float64:
					return int64(v)
				}
			}
		}
	}
	return -1
}
