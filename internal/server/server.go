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
	s.mux.HandleFunc("POST /api/chat", s.handleChat)
	s.mux.HandleFunc("PUT /api/document", s.handleUpdateDocument)
	s.mux.HandleFunc("DELETE /api/document", s.handleDeleteDocument)
	s.mux.HandleFunc("POST /api/finalize", s.handleFinalize)
	s.mux.HandleFunc("GET /api/export", s.handleExport)
	s.mux.HandleFunc("GET /api/pipeline/stats", s.handlePipelineStats)
	s.mux.HandleFunc("GET /api/pipeline/events", s.handlePipelineSSE)
	s.mux.HandleFunc("DELETE /api/graph", s.handleDeleteGraph)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
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

		// Get edges along every 1-2 hop path so 2nd-hop nodes stay connected.
		edgeResult, err := s.store.ROQuery(fmt.Sprintf(
			`MATCH p = (n {name: '%s'})-[*1..2]-(m) UNWIND relationships(p) AS r RETURN DISTINCT startNode(r).name, type(r), endNode(r).name, r.weight`,
			escapeCypherParam(nodeName),
		))
		if err == nil {
			resp.Edges = parseVisEdges(edgeResult)
		}

		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Paginated full graph
	nodeCypher := fmt.Sprintf(`MATCH (n) WHERE NOT n:__Schema__ RETURN n.name, labels(n), n.type ORDER BY n.name SKIP %d LIMIT %d`, offset, limit)
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
//
// Uses Cypher's properties() function for both nodes and edges so the
// export carries every stored property (including temporal keys written
// by extractTemporalFacts and any future additions) instead of a
// hand-curated subset. The familiar top-level fields are still exposed
// as named JSON keys for back-compat with the UI / existing consumers.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	type ExportNode struct {
		Name            string                 `json:"name"`
		Labels          []string               `json:"labels,omitempty"`
		Type            string                 `json:"type,omitempty"`
		Status          string                 `json:"status,omitempty"`
		FunctionalRoles string                 `json:"functional_roles,omitempty"`
		DomainType      string                 `json:"domain_type,omitempty"`
		Properties      map[string]interface{} `json:"properties,omitempty"`
	}
	type ExportEdge struct {
		From        string                 `json:"from"`
		To          string                 `json:"to"`
		Relation    string                 `json:"relation"`
		Weight      float64                `json:"weight,omitempty"`
		Description string                 `json:"description,omitempty"`
		Status      string                 `json:"status,omitempty"`
		Condition   string                 `json:"condition,omitempty"`
		Evidence    string                 `json:"evidence,omitempty"`
		ChunkIDs    string                 `json:"chunk_ids,omitempty"`
		Properties  map[string]interface{} `json:"properties,omitempty"`
	}
	type ExportData struct {
		Nodes []ExportNode `json:"nodes"`
		Edges []ExportEdge `json:"edges"`
	}

	export := ExportData{}

	// --- Nodes ---
	nodeResult, err := s.store.ROQuery(`MATCH (n) WHERE NOT n:__Schema__ RETURN n.name, labels(n), properties(n) ORDER BY n.name`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, row := range parseRows(nodeResult) {
		if len(row) < 1 {
			continue
		}
		name, _ := row[0].(string)
		if name == "" {
			continue
		}
		var labels []string
		if len(row) >= 2 {
			labels = parseStringList(row[1])
		}
		props := map[string]interface{}{}
		if len(row) >= 3 {
			props = parsePropertyMap(row[2])
		}
		out := ExportNode{
			Name:            name,
			Labels:          labels,
			Type:            stringProp(props, "type"),
			Status:          stringProp(props, "status"),
			FunctionalRoles: stringProp(props, "functional_roles"),
			DomainType:      stringProp(props, "domain_type"),
			Properties:      stripWellKnown(props, "type", "status", "functional_roles", "domain_type", "name", "embedding"),
		}
		export.Nodes = append(export.Nodes, out)
	}

	// --- Edges ---
	edgeResult, err := s.store.ROQuery(`MATCH (a)-[r]->(b) RETURN a.name, type(r), b.name, properties(r)`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, row := range parseRows(edgeResult) {
		if len(row) < 3 {
			continue
		}
		from, _ := row[0].(string)
		rel, _ := row[1].(string)
		to, _ := row[2].(string)
		if from == "" || to == "" {
			continue
		}
		props := map[string]interface{}{}
		if len(row) >= 4 {
			props = parsePropertyMap(row[3])
		}
		out := ExportEdge{
			From:        from,
			To:          to,
			Relation:    rel,
			Weight:      floatProp(props, "weight"),
			Description: stringProp(props, "description"),
			Status:      stringProp(props, "status"),
			Condition:   stringProp(props, "condition"),
			Evidence:    stringProp(props, "evidence"),
			ChunkIDs:    stringProp(props, "chunk_ids"),
			Properties:  stripWellKnown(props, "weight", "description", "status", "condition", "evidence", "chunk_ids", "embedding"),
		}
		export.Edges = append(export.Edges, out)
	}

	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=knowledge_graph.json")
	}
	writeJSON(w, http.StatusOK, export)
}

// parseRows is the shared "pull the [1] rows slice out of a GRAPH.QUERY
// reply" idiom. Returns each row as a []interface{} of column values.
func parseRows(result interface{}) [][]interface{} {
	arr, ok := result.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, ok := arr[1].([]interface{})
	if !ok {
		return nil
	}
	out := make([][]interface{}, 0, len(rows))
	for _, r := range rows {
		if cols, ok := r.([]interface{}); ok {
			out = append(out, cols)
		}
	}
	return out
}

// parsePropertyMap converts a properties(n)/properties(r) result cell into
// a Go map. FalkorDB returns property maps as a list of [key, value] pairs.
func parsePropertyMap(v interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	pairs, ok := v.([]interface{})
	if !ok {
		return out
	}
	for _, pair := range pairs {
		kv, ok := pair.([]interface{})
		if !ok || len(kv) < 2 {
			continue
		}
		key, _ := kv[0].(string)
		if key == "" {
			continue
		}
		out[key] = kv[1]
	}
	return out
}

// parseStringList converts a labels(n) result cell into []string.
func parseStringList(v interface{}) []string {
	raw, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// stringProp pulls a string property by key, tolerating non-string values.
func stringProp(props map[string]interface{}, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// floatProp pulls a numeric property as a float64.
func floatProp(props map[string]interface{}, key string) float64 {
	v, ok := props[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}

// stripWellKnown returns a copy of props with the listed keys removed, so
// the "everything else" Properties map doesn't repeat the named JSON fields.
func stripWellKnown(props map[string]interface{}, keys ...string) map[string]interface{} {
	if len(props) == 0 {
		return nil
	}
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make(map[string]interface{}, len(props))
	for k, v := range props {
		if drop[k] {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
		Text               string `json:"text"`
		Path               string `json:"path"`
		Source             string `json:"source"`
		ExtractionStrategy string `json:"extraction_strategy,omitempty"` // "llm" or "hybrid"
		NERServiceURL      string `json:"ner_service_url,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Apply per-request extraction strategy override
	if req.ExtractionStrategy != "" && req.ExtractionStrategy != s.cfg.ExtractionStrategy {
		origStrategy := s.cfg.ExtractionStrategy
		origNERURL := s.cfg.NERServiceURL
		s.cfg.ExtractionStrategy = req.ExtractionStrategy
		if req.NERServiceURL != "" {
			s.cfg.NERServiceURL = req.NERServiceURL
		}
		s.pipeline.SetExtractor(req.ExtractionStrategy, req.NERServiceURL)
		defer func() {
			s.cfg.ExtractionStrategy = origStrategy
			s.cfg.NERServiceURL = origNERURL
			s.pipeline.SetExtractor(origStrategy, origNERURL)
		}()
	}

	if req.Path != "" {
		info, err := os.Stat(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot access path: %v", err))
			return
		}
		if info.IsDir() {
			if err := s.pipeline.IngestDir(req.Path); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			data, err := os.ReadFile(req.Path)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := s.pipeline.IngestRawText(string(data), req.Path); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	} else if req.Text != "" {
		source := req.Source
		if source == "" {
			source = "api"
		}
		if err := s.pipeline.IngestRawText(req.Text, source); err != nil {
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
		Human    *bool  `json:"human,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "provide 'question' in request body")
		return
	}

	// Opt-in human answer: ?human=1 OR {"human": true}. Default off so
	// agent callers don't pay for an extra LLM round-trip they don't need.
	human := false
	if req.Human != nil {
		human = *req.Human
	}
	switch strings.ToLower(r.URL.Query().Get("human")) {
	case "1", "true", "yes":
		human = true
	case "0", "false", "no":
		human = false
	}

	result, err := s.pipeline.Query(req.Question, human)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Two primary fields: "answer" (human response) and "graph" (the focused
	// subgraph the answer was derived from). The pipeline builds the subgraph
	// from the same neighborhood it fed to the LLM, so they stay consistent.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"answer":   result.Answer,
		"graph":    result.Graph,
		"entities": result.Entities,
		"cypher":   result.Cypher,
	})
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


func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req pipeline.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		writeError(w, http.StatusBadRequest, "provide 'question' in request body")
		return
	}
	result, err := s.pipeline.Chat(req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"answer":   result.Answer,
		"graph":    result.Graph,
		"facts":    result.Facts,
		"entities": result.Entities,
	})
}

func (s *Server) handleUpdateDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text   string `json:"text"`
		Path   string `json:"path"`
		Source string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	content := req.Text
	source := req.Source
	if req.Path != "" {
		data, err := os.ReadFile(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot read path: %v", err))
			return
		}
		content = string(data)
		if source == "" {
			source = req.Path
		}
	}
	if content == "" || source == "" {
		writeError(w, http.StatusBadRequest, "provide 'text'+'source' or 'path' in request body")
		return
	}

	if err := s.pipeline.UpdateDocument(content, source); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated", "document": source})
}

func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DocumentID == "" {
		writeError(w, http.StatusBadRequest, "provide 'document_id' in request body")
		return
	}
	if err := s.pipeline.DeleteDocument(req.DocumentID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "document": req.DocumentID})
}

func (s *Server) handleFinalize(w http.ResponseWriter, r *http.Request) {
	if err := s.pipeline.Finalize(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats, _ := s.store.GetGraphStats()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "finalized",
		"nodes":  stats["nodes"],
		"edges":  stats["edges"],
	})
}

// handlePipelineStats returns the current or most recent pipeline telemetry as JSON.
func (s *Server) handlePipelineStats(w http.ResponseWriter, r *http.Request) {
	stats := s.pipeline.Stats()
	if stats == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "idle", "message": "no pipeline run in progress or completed"})
		return
	}
	data, err := stats.Snapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// handlePipelineSSE streams real-time pipeline progress via Server-Sent Events.
// The client connects with EventSource and receives JSON events for each phase
// transition until the pipeline completes or fails.
func (s *Server) handlePipelineSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	ch := s.pipeline.SubscribeStats()
	if ch == nil {
		// No active pipeline — send current stats as a single event and close
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		stats := s.pipeline.Stats()
		if stats != nil {
			data, _ := stats.Snapshot()
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
		} else {
			fmt.Fprintf(w, "event: idle\ndata: {\"status\":\"idle\"}\n\n")
		}
		flusher.Flush()
		return
	}
	defer s.pipeline.UnsubscribeStats(ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher.Flush()

	// Send initial snapshot
	if stats := s.pipeline.Stats(); stats != nil {
		data, _ := stats.Snapshot()
		fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", data)
		flusher.Flush()
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				// Pipeline finished, channel closed
				fmt.Fprintf(w, "event: done\ndata: {\"status\":\"closed\"}\n\n")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "event: progress\ndata: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"extraction_strategy": s.cfg.ExtractionStrategy,
		"ner_service_url":     s.cfg.NERServiceURL,
		"chunk_strategy":      s.cfg.ChunkStrategy,
		"llm_provider":        s.cfg.LLMProvider,
		"llm_model":           s.cfg.LLMModel,
		"workers":             s.cfg.Workers,
		"chunk_size":          s.cfg.ChunkSize,
		"chunk_overlap":       s.cfg.ChunkOverlap,
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
