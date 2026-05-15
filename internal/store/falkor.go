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

// CreateEntity creates or merges an entity node in the graph.
func (s *FalkorStore) CreateEntity(entity models.Entity) error {
	name := escapeCypher(entity.Name)
	entityType := escapeCypher(entity.Type)
	if entityType == "" {
		entityType = "Concept"
	}

	cypher := fmt.Sprintf(
		`MERGE (n:%s {name: '%s'}) ON CREATE SET n.type = '%s'`,
		entityType, name, entityType,
	)

	// Add properties
	setParts := []string{}
	for k, v := range entity.Properties {
		setParts = append(setParts, fmt.Sprintf("n.%s = '%s'", escapeCypher(k), escapeCypher(fmt.Sprint(v))))
	}
	if len(setParts) > 0 {
		cypher += ", " + strings.Join(setParts, ", ")
	}

	_, err := s.Query(cypher)
	return err
}

// CreateEdge creates or merges a relationship in the graph.
func (s *FalkorStore) CreateEdge(record models.EdgeRecord) error {
	if record.Node1 == record.Node2 {
		return nil // skip self-referencing edges
	}
	n1 := escapeCypher(record.Node1)
	n2 := escapeCypher(record.Node2)
	edgeType := toEdgeType(record.Edge)
	edgeDesc := escapeCypher(record.Edge)
	n1Type := escapeCypher(record.Node1Type)
	n2Type := escapeCypher(record.Node2Type)

	// Set entity type on nodes if available
	n1SetType := ""
	n2SetType := ""
	if n1Type != "" {
		n1SetType = fmt.Sprintf(", a.type = '%s'", n1Type)
	}
	if n2Type != "" {
		n2SetType = fmt.Sprintf(", b.type = '%s'", n2Type)
	}

	cypher := fmt.Sprintf(
		`MERGE (a:Concept {name: '%s'})
		 ON CREATE SET a.name = '%s'%s
		 MERGE (b:Concept {name: '%s'})
		 ON CREATE SET b.name = '%s'%s
		 MERGE (a)-[r:%s]->(b)
		 ON CREATE SET r.description = '%s', r.weight = %f, r.inferred = %t, r.chunk_ids = '%s'
		 ON MATCH SET r.weight = r.weight + %f, r.chunk_ids = r.chunk_ids + ',%s'`,
		n1, n1, n1SetType,
		n2, n2, n2SetType,
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
	cypher := `MATCH (n) RETURN n.name, labels(n), n.type ORDER BY n.name`
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

	nodeResult, err := s.ROQuery(`MATCH (n) RETURN count(n)`)
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

// Close closes the Redis connection.
func (s *FalkorStore) Close() error {
	return s.client.Close()
}

// escapeCypher escapes single quotes for Cypher strings.
func escapeCypher(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
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
