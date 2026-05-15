package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"rediskg/internal/llm"
	"rediskg/internal/pipeline"
	"rediskg/internal/store"
	"rediskg/pkg/config"
)

// Server is the HTTP REST server for rediskg.
type Server struct {
	cfg      *config.Config
	store    *store.FalkorStore
	pipeline *pipeline.Pipeline
	mux      *http.ServeMux
}

// New creates a new Server.
func New(cfg *config.Config) (*Server, error) {
	s, err := store.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to FalkorDB: %w", err)
	}

	llmClient := llm.NewClient(cfg)
	p := pipeline.New(cfg, s, llmClient)

	srv := &Server{
		cfg:      cfg,
		store:    s,
		pipeline: p,
		mux:      http.NewServeMux(),
	}

	srv.routes()
	return srv, nil
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleUI)
	s.mux.HandleFunc("GET /api/graph", s.handleGetGraph)
	s.mux.HandleFunc("GET /api/stats", s.handleGetStats)
	s.mux.HandleFunc("POST /api/ingest", s.handleIngest)
	s.mux.HandleFunc("POST /api/query", s.handleQuery)
	s.mux.HandleFunc("POST /api/cypher", s.handleCypher)
	s.mux.HandleFunc("GET /api/export", s.handleExport)
	s.mux.HandleFunc("DELETE /api/graph", s.handleDeleteGraph)
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	addr := ":" + s.cfg.HTTPPort
	log.Printf("Starting HTTP server on %s", addr)
	return http.ListenAndServe(addr, s.mux)
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(graphHTML))
}

// handleGetGraph supports:
//   - ?limit=N   — max nodes to return (default 500)
//   - ?offset=N  — skip first N nodes for pagination
//   - ?node=name — return only neighbors of a specific node (depth 2)
func (s *Server) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	type VisNode struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Group string `json:"group,omitempty"`
	}
	type GraphResponse struct {
		Nodes   []VisNode `json:"nodes"`
		Edges   []visEdge `json:"edges"`
		Total   int64     `json:"total"`
		HasMore bool      `json:"hasMore"`
	}

	q := r.URL.Query()
	limit := 500
	offset := 0
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if o, err := strconv.Atoi(q.Get("offset")); err == nil && o > 0 {
		offset = o
	}

	resp := GraphResponse{
		Nodes: []VisNode{},
		Edges: []visEdge{},
	}
	seen := map[string]bool{}

	// Get total count for pagination
	stats, _ := s.store.GetGraphStats()
	resp.Total = stats["nodes"]

	// If querying neighbors of a specific node
	if nodeName := q.Get("node"); nodeName != "" {
		neighborResult, err := s.store.ROQuery(fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r*1..2]-(m) RETURN DISTINCT m.name, labels(m), m.type LIMIT %d`,
			escapeCypherParam(nodeName), limit,
		))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Add the center node
		resp.Nodes = append(resp.Nodes, VisNode{ID: nodeName, Label: nodeName, Group: "focus"})
		seen[nodeName] = true

		// Add neighbor nodes
		if arr, ok := neighborResult.([]interface{}); ok && len(arr) >= 2 {
			if rows, ok := arr[1].([]interface{}); ok {
				for _, row := range rows {
					if cols, ok := row.([]interface{}); ok && len(cols) > 0 {
						name, _ := cols[0].(string)
						if name == "" || seen[name] {
							continue
						}
						seen[name] = true
						group := ""
						if len(cols) >= 3 {
							if t, ok := cols[2].(string); ok {
								group = t
							}
						}
						resp.Nodes = append(resp.Nodes, VisNode{ID: name, Label: name, Group: group})
					}
				}
			}
		}

		// Get edges between all visible nodes
		edgeResult, err := s.store.ROQuery(fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r]-(m) WHERE m.name IS NOT NULL RETURN n.name, type(r), m.name, r.weight`,
			escapeCypherParam(nodeName),
		))
		if err == nil {
			resp.Edges = parseVisEdges(edgeResult)
		}

		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Paginated full graph
	nodeCypher := fmt.Sprintf(`MATCH (n) RETURN n.name, labels(n), n.type ORDER BY n.name SKIP %d LIMIT %d`, offset, limit)
	nodeResult, err := s.store.ROQuery(nodeCypher)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if arr, ok := nodeResult.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) > 0 {
					name, _ := cols[0].(string)
					if name == "" || seen[name] {
						continue
					}
					seen[name] = true
					group := ""
					if len(cols) >= 3 {
						if t, ok := cols[2].(string); ok {
							group = t
						}
					}
					resp.Nodes = append(resp.Nodes, VisNode{ID: name, Label: name, Group: group})
				}
			}
		}
	}

	resp.HasMore = int64(offset+limit) < resp.Total

	// Get edges only between the visible nodes
	edgeResult, err := s.store.ROQuery(fmt.Sprintf(
		`MATCH (a)-[r]->(b) RETURN a.name, type(r), b.name, r.weight SKIP %d LIMIT %d`,
		offset, limit*3,
	))
	if err == nil {
		for _, e := range parseVisEdges(edgeResult) {
			if seen[e.From] && seen[e.To] {
				resp.Edges = append(resp.Edges, e)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleExport returns the full graph as JSON (all nodes + edges, no pagination).
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	type ExportNode struct {
		Name string `json:"name"`
		Type string `json:"type,omitempty"`
	}
	type ExportEdge struct {
		From        string  `json:"from"`
		To          string  `json:"to"`
		Relation    string  `json:"relation"`
		Weight      float64 `json:"weight,omitempty"`
		Description string  `json:"description,omitempty"`
	}
	type ExportData struct {
		Nodes []ExportNode `json:"nodes"`
		Edges []ExportEdge `json:"edges"`
	}

	export := ExportData{}

	// Get all nodes
	nodeResult, err := s.store.ROQuery(`MATCH (n) RETURN n.name, n.type ORDER BY n.name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if arr, ok := nodeResult.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 1 {
					name, _ := cols[0].(string)
					typ := ""
					if len(cols) >= 2 {
						typ, _ = cols[1].(string)
					}
					if name != "" {
						export.Nodes = append(export.Nodes, ExportNode{Name: name, Type: typ})
					}
				}
			}
		}
	}

	// Get all edges
	edgeResult, err := s.store.ROQuery(`MATCH (a)-[r]->(b) RETURN a.name, type(r), b.name, r.weight, r.description`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if arr, ok := edgeResult.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 3 {
					from, _ := cols[0].(string)
					rel, _ := cols[1].(string)
					to, _ := cols[2].(string)
					weight := 0.0
					desc := ""
					if len(cols) >= 4 {
						if w, ok := cols[3].(float64); ok {
							weight = w
						}
					}
					if len(cols) >= 5 {
						desc, _ = cols[4].(string)
					}
					if from != "" && to != "" {
						export.Edges = append(export.Edges, ExportEdge{
							From: from, To: to, Relation: rel,
							Weight: weight, Description: desc,
						})
					}
				}
			}
		}
	}

	// Check if user wants a file download
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=knowledge_graph.json")
	}

	writeJSON(w, http.StatusOK, export)
}

func (s *Server) handleDeleteGraph(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteGraph(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

type visEdge struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Label  string  `json:"label"`
	Weight float64 `json:"weight,omitempty"`
}

func parseVisEdges(result interface{}) []visEdge {
	var edges []visEdge
	if arr, ok := result.([]interface{}); ok && len(arr) >= 2 {
		if rows, ok := arr[1].([]interface{}); ok {
			for _, row := range rows {
				if cols, ok := row.([]interface{}); ok && len(cols) >= 3 {
					from, _ := cols[0].(string)
					relType, _ := cols[1].(string)
					to, _ := cols[2].(string)
					weight := 1.0
					if len(cols) >= 4 {
						if w, ok := cols[3].(float64); ok {
							weight = w
						}
					}
					if from != "" && to != "" {
						edges = append(edges, visEdge{From: from, To: to, Label: relType, Weight: weight})
					}
				}
			}
		}
	}
	return edges
}

func escapeCypherParam(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `'`, `\'`)
}

func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetGraphStats()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"graph": s.cfg.GraphName,
		"nodes": stats["nodes"],
		"edges": stats["edges"],
	})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.Path != "" {
		info, err := os.Stat(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot access path: %v", err))
			return
		}
		if info.IsDir() {
			if err := s.pipeline.IngestDirectory(req.Path); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			data, err := os.ReadFile(req.Path)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.pipeline.IngestText(string(data), req.Path); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	} else if req.Text != "" {
		source := req.Source
		if source == "" {
			source = "api"
		}
		if err := s.pipeline.IngestText(req.Text, source); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "provide 'text' or 'path' in request body")
		return
	}

	stats, _ := s.store.GetGraphStats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ok",
		"nodes":  stats["nodes"],
		"edges":  stats["edges"],
	})
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "provide 'question' in request body")
		return
	}

	result, err := s.pipeline.Query(req.Question)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Build subgraph for matched entities so the UI can render it directly
	type VisNode struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Group string `json:"group,omitempty"`
	}
	type QueryResponse struct {
		Answer   string                   `json:"answer"`
		Entities []map[string]interface{} `json:"entities"`
		Cypher   string                   `json:"cypher"`
		Graph    struct {
			Nodes []VisNode `json:"nodes"`
			Edges []visEdge `json:"edges"`
		} `json:"graph"`
	}

	resp := QueryResponse{
		Answer:   result.Answer,
		Entities: result.Entities,
		Cypher:   result.Cypher,
	}
	resp.Graph.Nodes = []VisNode{}
	resp.Graph.Edges = []visEdge{}

	// Fetch subgraph for each matched entity
	seen := map[string]bool{}
	for _, em := range result.Entities {
		name, _ := em["name"].(string)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		// Add center node
		resp.Graph.Nodes = append(resp.Graph.Nodes, VisNode{ID: name, Label: name, Group: "focus"})

		// Get neighbors
		neighborResult, err := s.store.ROQuery(fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r*1..2]-(m) RETURN DISTINCT m.name, labels(m), m.type LIMIT 50`,
			escapeCypherParam(name),
		))
		if err == nil {
			if arr, ok := neighborResult.([]interface{}); ok && len(arr) >= 2 {
				if rows, ok := arr[1].([]interface{}); ok {
					for _, row := range rows {
						if cols, ok := row.([]interface{}); ok && len(cols) > 0 {
							mname, _ := cols[0].(string)
							if mname == "" || seen[mname] {
								continue
							}
							seen[mname] = true
							group := ""
							if len(cols) >= 3 {
								if t, ok := cols[2].(string); ok {
									group = t
								}
							}
							resp.Graph.Nodes = append(resp.Graph.Nodes, VisNode{ID: mname, Label: mname, Group: group})
						}
					}
				}
			}
		}

		// Get edges
		edgeResult, err := s.store.ROQuery(fmt.Sprintf(
			`MATCH (n {name: '%s'})-[r]-(m) WHERE m.name IS NOT NULL RETURN n.name, type(r), m.name, r.weight`,
			escapeCypherParam(name),
		))
		if err == nil {
			for _, e := range parseVisEdges(edgeResult) {
				if seen[e.From] && seen[e.To] {
					resp.Graph.Edges = append(resp.Graph.Edges, e)
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCypher(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
		writeError(w, http.StatusBadRequest, "provide 'query' in request body")
		return
	}

	result, err := s.pipeline.QueryCypher(req.Query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": result,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
