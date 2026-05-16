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
func (s *FalkorStore) CreateEntity(entity models.Entity) error {
	name := escapeCypher(strings.ToLower(strings.TrimSpace(entity.Name)))
	entityType := escapeCypher(strings.ToLower(strings.TrimSpace(entity.Type)))
	if entityType == "" {
		entityType = "Concept"
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

	_, err := s.Query(cypher)
	return err
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

	cypher := fmt.Sprintf(
		`MERGE (a:Concept {name: '%s'})
		 ON CREATE SET a.name = '%s'
		 MERGE (b:Concept {name: '%s'})
		 ON CREATE SET b.name = '%s'%s
		 MERGE (a)-[r:%s]->(b)
		 ON CREATE SET r.description = '%s', r.weight = %f, r.inferred = %t, r.chunk_ids = '%s'
		 ON MATCH SET r.weight = r.weight + %f, r.chunk_ids = r.chunk_ids + ',%s'`,
		n1, n1,
		n2, n2, typeSetClause,
		edgeType, edgeDesc,
		record.Weight, record.Inferred, strings.Join(record.ChunkIDs, ","),
		record.Weight, strings.Join(record.ChunkIDs, ","),
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

	// Keep at most 4 words to prevent verbose edge types
	if len(parts) > 4 {
		parts = parts[:4]
	}

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
