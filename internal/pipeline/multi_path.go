package pipeline

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"rediskg/internal/schema"
	"rediskg/internal/store"
	"rediskg/pkg/models"
)

// Multi-path retrieval — Go port of graphrag_sdk's MultiPathRetrieval.
//
// Same nine phases, adapted to our graph shape:
//   1. Keyword extraction (stopword filter + LLM proper nouns)
//   2. Embed the query
//   3. Edge-fact vector search across all relation-type indexes
//   4. Entity discovery (Cypher CONTAINS + entity-vector + merge edge-fact endpoints)
//   4b. Sibling expansion on enumeration queries
//   5. 1-hop + 2-hop relationship expansion from top entities
//   6. Chunk retrieval (3 paths: chunk-vector + MENTIONED_IN cosine-ranked + 2-hop)
//   7. Fetch :Document.path per chunk via PART_OF
//   8. Cosine rerank of chunks
//   9. Question-type hint + structured-sections assembly
//
// Differences from upstream:
//   - Our edges are typed individually (not :RELATES with rel_type prop), so
//     edge-vector search iterates relation-type indexes via FindSimilarEdgeFacts.
//   - Analytical (LLM-to-Cypher) is the auto-triggered analyticalQuery path
//     — already wired in Query(), runs ahead of multi-path when the question
//     contains aggregation cues.

// retrievalStopwords mirrors MultiPathRetrieval._STOP_WORDS.
var retrievalStopwords = map[string]bool{
	"what": true, "who": true, "where": true, "when": true, "why": true,
	"how": true, "which": true, "whom": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"the": true, "a": true, "an": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "of": true, "and": true, "or": true,
	"with": true, "by": true, "from": true, "as": true, "but": true,
	"not": true, "no": true, "nor": true, "does": true, "did": true,
	"do": true, "has": true, "had": true, "have": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"shall": true, "can": true, "this": true, "that": true, "these": true,
	"those": true, "it": true, "its": true, "they": true, "their": true,
	"he": true, "she": true, "him": true, "her": true, "his": true,
	"about": true, "after": true, "before": true, "between": true,
	"during": true, "through": true, "according": true, "described": true,
}

var nonWordRe = regexp.MustCompile(`[?.!,;:'"\-()\[\]]+`)

// extractKeywords returns (simple, llm) keyword slices, matching upstream.
// Simple is stopword-filtered tokens (max 12). LLM is the proper-nouns list
// pulled via a single LLM call.
func (p *Pipeline) extractKeywords(question string) (simple []string, llm []string) {
	cleaned := nonWordRe.ReplaceAllString(strings.ToLower(question), " ")
	for _, w := range strings.Fields(cleaned) {
		if retrievalStopwords[w] || len(w) <= 2 {
			continue
		}
		simple = append(simple, w)
		if len(simple) >= 12 {
			break
		}
	}

	const sys = "Extract ALL proper nouns, character names, person names, place names, " +
		"book titles, and specific terms from this question. " +
		"Return them as a JSON object: {\"names\": [\"name1\", \"name2\", ...]}. " +
		"If there are no proper nouns, return {\"names\": []}."
	user := "Question: " + question
	resp, err := p.llmClient.Complete(sys, user)
	if err == nil {
		llm = parseKeywordsJSON(resp)
	} else {
		log.Printf("multi-path: LLM keyword extraction failed: %v", err)
	}
	return simple, llm
}

// keywordsJSONRe looks anywhere in the response for "names": [ ... ]. Anchor
// is the literal "names" key — the surrounding object braces are not required
// so a JSON-mode response wrapped in extra markdown still matches.
var keywordsJSONRe = regexp.MustCompile(`(?s)"names"\s*:\s*\[([^\]]*)\]`)

// parseKeywordsJSON extracts the names array from the LLM JSON response.
// Tolerant of extra whitespace / surrounding markdown. Only the outer
// double-quotes are stripped — inner single quotes (e.g. names like
// O'Brien, "with 'quotes'") survive intact.
func parseKeywordsJSON(raw string) []string {
	m := keywordsJSONRe.FindStringSubmatch(raw)
	if len(m) < 2 {
		return nil
	}
	inner := m[1]
	var out []string
	for _, tok := range strings.Split(inner, ",") {
		raw := strings.TrimSpace(tok)
		// Length check happens on the *raw* token (still quoted) so that
		// a single-letter name like "X" (raw length 3) still passes —
		// same semantics as the upstream SDK parser.
		if raw == "" || len(raw) <= 1 {
			continue
		}
		cleaned := strings.Trim(raw, `"`) // outer double-quotes only
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}
		out = append(out, cleaned)
	}
	return out
}

// enumerationRe matches queries that ask to list/enumerate all of something.
// Mirrors entity_discovery._ENUMERATION_RE.
var enumerationRe = regexp.MustCompile(`(?i)\b(every|each|complete list|full list|list all|list of all|enumerate|name all|name every|all the|all of the)\b`)

// isEnumerationQuery returns true for "list every / name all / …" style queries.
func isEnumerationQuery(q string) bool { return enumerationRe.MatchString(q) }

// detectQuestionType returns an answer-format hint string for the LLM. The
// hint is prepended to the LLM context so it sees it first. Mirrors
// result_assembly.detect_question_type.
func detectQuestionType(query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	switch {
	case hasAnyPrefix(q, "is ", "are ", "was ", "were ", "did ", "does ", "do ",
		"has ", "had ", "have ", "can ", "could ", "will ", "would ", "should "):
		return "Answer format: This is a yes/no question — start with Yes or No, then explain briefly."
	case strings.HasPrefix(q, "who "):
		return "Answer format: Name the specific person(s) or character(s)."
	case strings.HasPrefix(q, "where "):
		return "Answer format: Name the specific place or location."
	case strings.HasPrefix(q, "when "):
		return "Answer format: Provide the specific time, date, or period."
	case strings.HasPrefix(q, "how many"), strings.HasPrefix(q, "how much"):
		return "Answer format: Provide a specific number or quantity."
	}
	return ""
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// scoredFact is a fact string with its cosine similarity score.
type scoredFact struct {
	Text  string
	Score float64
}

// filterFactsByRelevance ports result_assembly.filter_facts_by_relevance.
// Always keeps at least minKeep facts; applies the threshold to the rest.
func filterFactsByRelevance(in []scoredFact, minScore float64, maxFacts, minKeep int) []string {
	if len(in) == 0 {
		return nil
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].Score > in[j].Score })
	if len(in) > maxFacts {
		in = in[:maxFacts]
	}
	out := make([]string, 0, len(in))
	for _, f := range in {
		if len(out) < minKeep || f.Score >= minScore {
			out = append(out, f.Text)
		}
	}
	return out
}

// candidateChunk is one chunk in the retrieval pool, tagged with the path
// that found it (for logging / debugging).
type candidateChunk struct {
	ID     string
	Text   string
	Source string
	// Stored chunk embedding when we have one; nil otherwise. Used by
	// the rerank fast-path so we don't re-embed at query time.
	Embedding []float32
}

// chunkPool aggregates candidates across all retrieval paths, deduplicating
// by chunk id. First path to find a chunk wins the Source tag.
type chunkPool struct {
	items map[string]*candidateChunk
	order []string
}

func newChunkPool() *chunkPool { return &chunkPool{items: map[string]*candidateChunk{}} }

func (p *chunkPool) add(id, text, source string) {
	if id == "" || text == "" {
		return
	}
	if _, ok := p.items[id]; ok {
		return
	}
	p.items[id] = &candidateChunk{ID: id, Text: text, Source: source}
	p.order = append(p.order, id)
}

func (p *chunkPool) ids() []string {
	out := make([]string, len(p.order))
	copy(out, p.order)
	return out
}

func (p *chunkPool) attachEmbeddings(emb map[string][]float32) {
	for id, vec := range emb {
		if c, ok := p.items[id]; ok {
			c.Embedding = vec
		}
	}
}

// retrievedEntity is one entity in the discovery pool, tagged with its source.
type retrievedEntity struct {
	Name        string
	Description string
	Source      string // cypher_exact | cypher_contains | vector | edge_fact | sibling
}

// entityPool aggregates entities across paths, deduplicating by lowercase name.
type entityPool struct {
	items map[string]*retrievedEntity
	order []string
}

func newEntityPool() *entityPool { return &entityPool{items: map[string]*retrievedEntity{}} }

func (p *entityPool) add(name, desc, source string) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return
	}
	if _, ok := p.items[key]; ok {
		return
	}
	p.items[key] = &retrievedEntity{Name: name, Description: desc, Source: source}
	p.order = append(p.order, key)
}

func (p *entityPool) list(max int) []*retrievedEntity {
	if max <= 0 || max > len(p.order) {
		max = len(p.order)
	}
	out := make([]*retrievedEntity, 0, max)
	for _, k := range p.order[:max] {
		out = append(out, p.items[k])
	}
	return out
}

func (p *entityPool) names(max int) []string {
	es := p.list(max)
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = strings.ToLower(strings.TrimSpace(e.Name))
	}
	return out
}

// multiPathResult is the structured payload the LLM gets at the end.
type multiPathResult struct {
	HintSection         string
	EntitiesSection     string
	RelationshipsSection string
	FactsSection        string
	PassagesSection     string

	// Raw, for the response payload + visualisation.
	Entities             []*retrievedEntity
	RelationshipStrings  []string
	FactStrings          []string
	PassageTexts         []string
}

// runMultiPath is the orchestrator. Mirrors MultiPathRetrieval._execute.
func (p *Pipeline) runMultiPath(question string) (*multiPathResult, error) {
	// 1. Keywords
	simple, llmKw := p.extractKeywords(question)
	keywords := append([]string{}, llmKw...)
	if len(keywords) > 8 {
		keywords = keywords[:8]
	}
	keywords = append(keywords, simple...)
	log.Printf("multi-path [1/9]: %d keywords (%d llm, %d simple)", len(keywords), len(llmKw), len(simple))

	// 2. Embed the question
	qvec, err := p.llmClient.Embed(question)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// 3. Edge-fact vector search across all rel types we have embeddings for.
	relTypes := relationsWithEdgeEmbeddings(p.store)
	factsScored, factEntities := searchEdgeFacts(p.store, relTypes, qvec, 15)
	factStrings := filterFactsByRelevance(factsScored, 0.25, 12, 3)
	log.Printf("multi-path [3/9]: %d candidate facts → %d kept", len(factsScored), len(factStrings))

	// 4. Entity discovery — fulltext + Cypher CONTAINS + entity-vector + merge fact endpoints
	entities := newEntityPool()
	discoverEntitiesByFulltext(p.store, llmKw, entities)
	discoverEntitiesByContains(p.store, llmKw, entities)
	discoverEntitiesByVector(p.store, keywords, qvec, entities)
	for _, fe := range factEntities {
		entities.add(fe.Name, fe.Description, "edge_fact")
	}
	log.Printf("multi-path [4/9]: %d entities discovered", len(entities.order))

	// 4b. Sibling expansion (enumeration queries only)
	if isEnumerationQuery(question) {
		n := expandSiblings(p.store, entities, 20)
		if n > 0 {
			log.Printf("multi-path [4b/9]: +%d sibling entities", n)
		}
	}

	// 5. Relationship expansion (1-hop + 2-hop)
	top := entities.list(30)
	relStrings := expandRelationships(p.store, top, 20)
	log.Printf("multi-path [5/9]: %d relationship strings", len(relStrings))

	// 6. Chunk retrieval (4 paths: fulltext, vector, MENTIONED_IN cosine-ranked, 2-hop)
	chunks := newChunkPool()
	retrieveChunksByFulltext(p.store, question, keywords, chunks, 10)
	retrieveChunksByVector(p.store, qvec, chunks, 15)
	retrieveChunksByMentionedIn(p.store, entities.names(15), qvec, chunks, 3)
	retrieveChunksByTwoHop(p.store, entities.names(10), chunks, 20)
	storedEmb := fetchChunkEmbeddings(p.store, chunks.ids())
	chunks.attachEmbeddings(storedEmb)
	log.Printf("multi-path [6/9]: %d candidate chunks (%d with stored embedding)", len(chunks.order), len(storedEmb))

	// 7. Source document paths
	docMap := fetchChunkDocumentPaths(p.store, chunks.ids())

	// 8. Cosine rerank
	passages := rerankChunks(p, qvec, chunks, 15)
	// Tag passages with their source doc path.
	tagged := make([]string, len(passages))
	for i, c := range passages {
		if path := docMap[c.ID]; path != "" {
			tagged[i] = "[Source: " + path + "]\n" + c.Text
		} else {
			tagged[i] = c.Text
		}
	}
	log.Printf("multi-path [8/9]: %d passages after rerank", len(tagged))

	// 9. Assemble sections
	result := assembleSections(question, top, relStrings, factStrings, tagged)
	return result, nil
}

// relationsWithEdgeEmbeddings asks the graph which relation types actually
// have any edges with an :embedding property. Avoids querying empty/missing
// vector indexes.
func relationsWithEdgeEmbeddings(s *store.FalkorStore) []string {
	res, err := s.ROQuery(`CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType`)
	if err != nil {
		return nil
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, _ := arr[1].([]interface{})
	var all []string
	for _, row := range rows {
		cols, ok := row.([]interface{})
		if !ok || len(cols) < 1 {
			continue
		}
		if rt, ok := cols[0].(string); ok && rt != "" {
			all = append(all, rt)
		}
	}
	// Filter to only those where at least one edge actually has an embedding.
	var kept []string
	for _, rt := range all {
		q := fmt.Sprintf(`MATCH ()-[r:%s]->() WHERE r.embedding IS NOT NULL RETURN count(r) LIMIT 1`, rt)
		res, err := s.ROQuery(q)
		if err != nil {
			continue
		}
		if parseCount(res) > 0 {
			kept = append(kept, rt)
		}
	}
	return kept
}

// parseCount pulls an int out of a Cypher result whose first row, first cell
// is a count(...) value.
func parseCount(res interface{}) int64 {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return 0
	}
	rows, _ := arr[1].([]interface{})
	if len(rows) == 0 {
		return 0
	}
	cols, _ := rows[0].([]interface{})
	if len(cols) == 0 {
		return 0
	}
	switch v := cols[0].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

// searchEdgeFacts is the multi_path step 3: edge-vector search across all
// rel types with embeddings, returning the scored fact strings plus the set
// of endpoint entities (used as graph entry points by entity discovery).
func searchEdgeFacts(s *store.FalkorStore, relTypes []string, qvec []float32, topK int) ([]scoredFact, []*retrievedEntity) {
	if len(relTypes) == 0 {
		return nil, nil
	}
	res, err := s.FindSimilarEdgeFacts(relTypes, qvec, topK)
	if err != nil {
		log.Printf("multi-path: edge-fact vector search failed: %v", err)
		return nil, nil
	}
	facts := make([]scoredFact, 0, len(res))
	entSeen := map[string]bool{}
	var ents []*retrievedEntity
	for _, r := range res {
		if r.From == "" || r.To == "" || r.RelType == "" {
			continue
		}
		fact := r.Fact
		if fact == "" {
			fact = r.From + " —[" + r.RelType + "]→ " + r.To
		}
		facts = append(facts, scoredFact{Text: fact, Score: r.Score})
		for _, name := range []string{r.From, r.To} {
			key := strings.ToLower(strings.TrimSpace(name))
			if key == "" || entSeen[key] {
				continue
			}
			entSeen[key] = true
			ents = append(ents, &retrievedEntity{Name: name, Source: "edge_fact"})
		}
	}
	return facts, ents
}

// discoverEntitiesByFulltext uses the RediSearch fulltext index on
// Concept.name. Faster than CONTAINS for large graphs and supports
// partial-word matching. Falls back silently if the index doesn't exist yet.
func discoverEntitiesByFulltext(s *store.FalkorStore, kws []string, pool *entityPool) {
	for _, kw := range kws {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		names, err := s.FulltextSearch("Concept", "name", kw, 5)
		if err != nil {
			// Index may not exist yet — fall through to CONTAINS.
			return
		}
		for _, n := range names {
			pool.add(n, "", "fulltext")
		}
	}
}

// discoverEntitiesByContains is entity_discovery Path A — Cypher CONTAINS
// with per-keyword quota.
func discoverEntitiesByContains(s *store.FalkorStore, kws []string, pool *entityPool) {
	seen := map[string]bool{}
	var batch []string
	for _, kw := range kws {
		if len(batch) >= 8 {
			break
		}
		k := strings.ToLower(strings.TrimSpace(kw))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		batch = append(batch, kw)
	}
	if len(batch) == 0 {
		return
	}
	// Pass a1: exact name matches first (so they survive downstream caps).
	exact, err := s.ROQueryWithParams(
		"UNWIND $keywords AS kw "+
			"CALL { WITH kw MATCH (e:Concept) WHERE toLower(e.name) = toLower(kw) "+
			"RETURN e.name AS name LIMIT 3 } RETURN name",
		map[string]interface{}{"keywords": toInterfaceSlice(batch)},
	)
	if err == nil {
		for _, row := range parseStringRows(exact) {
			pool.add(row, "", "cypher_exact")
		}
	}
	// Pass a2: CONTAINS with shorter-name priority.
	res, err := s.ROQueryWithParams(
		"UNWIND $keywords AS kw "+
			"CALL { WITH kw MATCH (e:Concept) "+
			"WHERE toLower(e.name) CONTAINS toLower(kw) AND toLower(e.name) <> toLower(kw) "+
			"RETURN e.name AS name "+
			"ORDER BY size(e.name) ASC, toLower(e.name) ASC "+
			"LIMIT 5 } RETURN name",
		map[string]interface{}{"keywords": toInterfaceSlice(batch)},
	)
	if err != nil {
		log.Printf("multi-path: CONTAINS entity search failed: %v", err)
		return
	}
	for _, row := range parseStringRows(res) {
		pool.add(row, "", "cypher_contains")
	}
}

// discoverEntitiesByVector is the equivalent of entity_discovery Path B
// (upstream uses fulltext; we substitute entity-name vector similarity).
func discoverEntitiesByVector(s *store.FalkorStore, _ []string, qvec []float32, pool *entityPool) {
	names, err := s.FindSimilarEntities("Concept", "embedding", qvec, 10)
	if err != nil {
		return
	}
	for _, n := range names {
		pool.add(n, "", "vector")
	}
}

// expandSiblings is the enumeration-query expansion: pulls in graph siblings
// (other __Entity__ neighbours of hubs connected to ≥2 already-discovered
// entities). Adapted from entity_discovery.expand_sibling_entities — we use
// :Concept instead of :__Entity__.
func expandSiblings(s *store.FalkorStore, pool *entityPool, maxSiblings int) int {
	if len(pool.order) < 2 {
		return 0
	}
	known := make([]interface{}, 0, len(pool.order))
	for _, k := range pool.order {
		if e, ok := pool.items[k]; ok {
			known = append(known, strings.ToLower(strings.TrimSpace(e.Name)))
		}
	}
	res, err := s.ROQueryWithParams(
		"MATCH (e:Concept) WHERE toLower(e.name) IN $found "+
			"MATCH (e)--(hub:Concept) "+
			"WITH hub, collect(DISTINCT toLower(e.name)) AS via "+
			"WHERE size(via) >= 2 "+
			"MATCH (hub)--(sibling:Concept) "+
			"WHERE NOT toLower(sibling.name) IN $found "+
			"RETURN DISTINCT sibling.name AS name "+
			"ORDER BY sibling.name "+
			"LIMIT $limit",
		map[string]interface{}{"found": known, "limit": maxSiblings},
	)
	if err != nil {
		return 0
	}
	added := 0
	for _, n := range parseStringRows(res) {
		before := len(pool.order)
		pool.add(n, "", "sibling")
		if len(pool.order) > before {
			added++
		}
	}
	return added
}

// expandRelationships is multi_path step 5 — 1-hop + 2-hop relationship
// strings from the top entities. Adapted for our typed-edge graph (no
// :RELATES; we use any edge whose type is in the schema's relation index).
func expandRelationships(s *store.FalkorStore, ents []*retrievedEntity, maxRels int) []string {
	if len(ents) == 0 {
		return nil
	}
	names := make([]interface{}, 0, len(ents))
	for _, e := range ents {
		if len(names) >= 15 {
			break
		}
		names = append(names, strings.ToLower(strings.TrimSpace(e.Name)))
	}
	if len(names) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var out []string
	addLine := func(line, key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, line)
	}

	// 1-hop
	res, err := s.ROQueryWithParams(
		"UNWIND $names AS nm "+
			"MATCH (a:Concept {name: nm})-[r]->(b:Concept) "+
			"RETURN a.name AS src, type(r) AS rel, b.name AS tgt, COALESCE(r.fact, r.description, '') AS fact "+
			"LIMIT 150",
		map[string]interface{}{"names": names},
	)
	if err == nil {
		for _, row := range parseRelRows(res) {
			line := row.Src + " —[" + row.Rel + "]→ " + row.Tgt
			if row.Fact != "" && row.Fact != row.Rel {
				line += ": " + row.Fact
			}
			addLine(line, row.Src+"|"+row.Rel+"|"+row.Tgt)
		}
	}
	// 2-hop (top 5)
	hop2 := names
	if len(hop2) > 5 {
		hop2 = hop2[:5]
	}
	res, err = s.ROQueryWithParams(
		"UNWIND $names AS nm "+
			"MATCH (a:Concept {name: nm})-[r1]->(b:Concept)-[r2]->(c:Concept) "+
			"RETURN a.name, type(r1), b.name, type(r2), c.name "+
			"LIMIT 25",
		map[string]interface{}{"names": hop2},
	)
	if err == nil {
		for _, row := range parseTwoHopRows(res) {
			if row.A == "" || row.C == "" {
				continue
			}
			line := row.A + " —[" + row.R1 + "]→ " + row.B + " —[" + row.R2 + "]→ " + row.C
			addLine(line, row.A+"|"+row.R1+"|"+row.B+"|"+row.R2+"|"+row.C)
		}
	}
	if len(out) > maxRels {
		out = out[:maxRels]
	}
	return out
}

// retrieveChunksByVector is chunk_retrieval Path B — top-k chunks by cosine.
// retrieveChunksByFulltext uses the RediSearch fulltext index on Chunk.text.
// Searches using the question and individual keywords. Falls back silently
// if the index doesn't exist yet.
func retrieveChunksByFulltext(s *store.FalkorStore, question string, keywords []string, pool *chunkPool, k int) {
	// Search with the full question first.
	res, err := s.FulltextSearchChunks(question, k)
	if err != nil {
		return // index may not exist
	}
	for _, c := range res {
		pool.add(c.ID, c.Text, "fulltext")
	}
	// Then individual keywords for broader coverage.
	for _, kw := range keywords {
		if len(pool.order) >= k*2 {
			break
		}
		res, err := s.FulltextSearchChunks(kw, 3)
		if err != nil {
			continue
		}
		for _, c := range res {
			pool.add(c.ID, c.Text, "fulltext_kw")
		}
	}
}

func retrieveChunksByVector(s *store.FalkorStore, qvec []float32, pool *chunkPool, k int) {
	res, err := s.FindSimilarChunks(qvec, k)
	if err != nil {
		return
	}
	for _, c := range res {
		pool.add(c.ID, c.Text, "vector")
	}
}

// retrieveChunksByMentionedIn is chunk_retrieval Path C — for each top
// entity, take its MENTIONED_IN chunks ordered by cosine distance to the
// query vector (top perEntity per entity). Matches upstream's cosine-ranked
// MENTIONED_IN strategy directly.
func retrieveChunksByMentionedIn(s *store.FalkorStore, entityNames []string, qvec []float32, pool *chunkPool, perEntity int) {
	if len(entityNames) == 0 {
		return
	}
	names := make([]interface{}, len(entityNames))
	for i, n := range entityNames {
		names[i] = n
	}
	vecStr := store.EncodeCypherValue(float32sToInterface(qvec))
	cypher := fmt.Sprintf(
		"UNWIND $names AS nm "+
			"MATCH (e:Concept {name: nm})-[:MENTIONED_IN]->(c:Chunk) "+
			"WHERE c.embedding IS NOT NULL "+
			"WITH nm, c, vec.cosineDistance(c.embedding, vecf32(%s)) AS dist "+
			"ORDER BY nm, dist ASC "+
			"WITH nm, COLLECT(c)[..%d] AS chunks "+
			"UNWIND chunks AS c "+
			"RETURN c.id AS id, c.text AS text",
		vecStr, perEntity,
	)
	res, err := s.ROQueryWithParams(cypher, map[string]interface{}{"names": names})
	if err != nil {
		// Fallback: no cosine ranking, just take first MENTIONED_IN chunks.
		alt, err2 := s.ROQueryWithParams(
			"UNWIND $names AS nm "+
				"MATCH (e:Concept {name: nm})-[:MENTIONED_IN]->(c:Chunk) "+
				"RETURN c.id AS id, c.text AS text LIMIT 50",
			map[string]interface{}{"names": names},
		)
		if err2 != nil {
			return
		}
		res = alt
	}
	for _, row := range parseChunkRows(res) {
		pool.add(row.ID, row.Text, "mentioned_in")
	}
}

// retrieveChunksByTwoHop is chunk_retrieval Path D — entity → neighbour →
// MENTIONED_IN → chunk. Catches chunks about related entities not in the
// immediate keyword set.
func retrieveChunksByTwoHop(s *store.FalkorStore, entityNames []string, pool *chunkPool, limit int) {
	if len(entityNames) == 0 {
		return
	}
	names := make([]interface{}, len(entityNames))
	for i, n := range entityNames {
		names[i] = n
	}
	res, err := s.ROQueryWithParams(
		"UNWIND $names AS nm "+
			"MATCH (e:Concept {name: nm})-[]-(n:Concept)-[:MENTIONED_IN]->(c:Chunk) "+
			"RETURN DISTINCT c.id AS id, c.text AS text "+
			"LIMIT $limit",
		map[string]interface{}{"names": names, "limit": limit},
	)
	if err != nil {
		return
	}
	for _, row := range parseChunkRows(res) {
		pool.add(row.ID, row.Text, "2hop_mentioned")
	}
}

// fetchChunkEmbeddings batch-loads the stored embedding vector for each
// candidate chunk in one round-trip. Used by the rerank fast-path.
func fetchChunkEmbeddings(s *store.FalkorStore, chunkIDs []string) map[string][]float32 {
	out := map[string][]float32{}
	if len(chunkIDs) == 0 {
		return out
	}
	ids := make([]interface{}, len(chunkIDs))
	for i, id := range chunkIDs {
		ids[i] = id
	}
	res, err := s.ROQueryWithParams(
		"UNWIND $ids AS cid "+
			"MATCH (c:Chunk {id: cid}) WHERE c.embedding IS NOT NULL "+
			"RETURN c.id, c.embedding",
		map[string]interface{}{"ids": ids},
	)
	if err != nil {
		return out
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return out
	}
	rows, _ := arr[1].([]interface{})
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 2 {
			continue
		}
		id, _ := cols[0].(string)
		raw, ok := cols[1].([]interface{})
		if !ok {
			continue
		}
		vec := make([]float32, 0, len(raw))
		for _, v := range raw {
			switch x := v.(type) {
			case float64:
				vec = append(vec, float32(x))
			case float32:
				vec = append(vec, x)
			case int64:
				vec = append(vec, float32(x))
			}
		}
		if id != "" && len(vec) > 0 {
			out[id] = vec
		}
	}
	return out
}

// fetchChunkDocumentPaths batch-resolves each candidate chunk → its
// Document.path via PART_OF. Used for the [Source: ...] tag on passages.
func fetchChunkDocumentPaths(s *store.FalkorStore, chunkIDs []string) map[string]string {
	out := map[string]string{}
	if len(chunkIDs) == 0 {
		return out
	}
	ids := make([]interface{}, len(chunkIDs))
	for i, id := range chunkIDs {
		ids[i] = id
	}
	res, err := s.ROQueryWithParams(
		"UNWIND $ids AS cid "+
			"MATCH (c:Chunk {id: cid})-[:PART_OF]->(d:Document) "+
			"RETURN c.id, d.path",
		map[string]interface{}{"ids": ids},
	)
	if err != nil {
		return out
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return out
	}
	rows, _ := arr[1].([]interface{})
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 2 {
			continue
		}
		id, _ := cols[0].(string)
		path, _ := cols[1].(string)
		if id != "" && path != "" {
			out[id] = path
		}
	}
	return out
}

// rerankChunks ports result_assembly.rerank_chunks — when ≥90% of candidates
// already have a stored embedding, fast-path with zero API calls; otherwise
// re-embed everything at query time (slow but correct).
func rerankChunks(p *Pipeline, qvec []float32, pool *chunkPool, topK int) []*candidateChunk {
	candidates := make([]*candidateChunk, 0, len(pool.order))
	for _, id := range pool.order {
		candidates = append(candidates, pool.items[id])
	}
	if len(candidates) == 0 {
		return nil
	}
	withEmb := 0
	for _, c := range candidates {
		if len(c.Embedding) > 0 {
			withEmb++
		}
	}
	// Fast path: rerank using stored vectors. Threshold matches upstream.
	if float64(withEmb)/float64(len(candidates)) >= 0.9 {
		scored := make([]struct {
			c     *candidateChunk
			score float64
		}, len(candidates))
		for i, c := range candidates {
			if len(c.Embedding) == 0 {
				scored[i] = struct {
					c     *candidateChunk
					score float64
				}{c, -1.0}
				continue
			}
			scored[i] = struct {
				c     *candidateChunk
				score float64
			}{c, cosineSim(qvec, c.Embedding)}
		}
		sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
		out := make([]*candidateChunk, 0, topK)
		for i := 0; i < len(scored) && i < topK; i++ {
			out = append(out, scored[i].c)
		}
		return out
	}

	// Fallback: re-embed candidate texts concurrently.
	workers := p.cfg.Workers
	if workers <= 0 {
		workers = 8
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	scores := make([]float64, len(candidates))
	for i, c := range candidates {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int, c *candidateChunk) {
			defer wg.Done()
			defer func() { <-sem }()
			vec, err := p.llmClient.Embed(c.Text)
			if err != nil {
				scores[i] = -1.0
				return
			}
			scores[i] = cosineSim(qvec, vec)
		}(i, c)
	}
	wg.Wait()
	idx := make([]int, len(candidates))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return scores[idx[i]] > scores[idx[j]] })
	out := make([]*candidateChunk, 0, topK)
	for i := 0; i < len(idx) && i < topK; i++ {
		out = append(out, candidates[idx[i]])
	}
	return out
}

func cosineSim(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0.0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// assembleSections builds the structured payload the LLM gets — one section
// per category, the question-type hint prepended.
func assembleSections(
	question string,
	ents []*retrievedEntity,
	rels, facts, passages []string,
) *multiPathResult {
	hint := detectQuestionType(question)

	entLines := make([]string, 0, len(ents))
	seen := map[string]bool{}
	for _, e := range ents {
		k := strings.ToLower(strings.TrimSpace(e.Name))
		if seen[k] {
			continue
		}
		seen[k] = true
		if e.Description != "" {
			entLines = append(entLines, "- "+e.Name+": "+e.Description)
		} else {
			entLines = append(entLines, "- "+e.Name)
		}
		if len(entLines) >= 25 {
			break
		}
	}

	result := &multiPathResult{
		HintSection:         hint,
		Entities:            ents,
		RelationshipStrings: rels,
		FactStrings:         facts,
		PassageTexts:        passages,
	}
	if len(entLines) > 0 {
		result.EntitiesSection = "## Key Entities\n" + strings.Join(entLines, "\n")
	}
	if len(rels) > 0 {
		clipped := rels
		if len(clipped) > 20 {
			clipped = clipped[:20]
		}
		lines := make([]string, len(clipped))
		for i, r := range clipped {
			lines[i] = "- " + r
		}
		result.RelationshipsSection = "## Entity Relationships\n" + strings.Join(lines, "\n")
	}
	if len(facts) > 0 {
		clipped := facts
		if len(clipped) > 15 {
			clipped = clipped[:15]
		}
		lines := make([]string, len(clipped))
		for i, f := range clipped {
			lines[i] = "- " + f
		}
		result.FactsSection = "## Knowledge Graph Facts\n" + strings.Join(lines, "\n")
	}
	if len(passages) > 0 {
		clipped := passages
		if len(clipped) > 15 {
			clipped = clipped[:15]
		}
		result.PassagesSection = "## Source Document Passages\n" + strings.Join(clipped, "\n---\n")
	}
	return result
}

// assembledContext renders all populated sections into one string ready to
// hand to the answering LLM.
func (m *multiPathResult) assembledContext() string {
	parts := make([]string, 0, 5)
	for _, s := range []string{m.HintSection, m.EntitiesSection, m.RelationshipsSection, m.FactsSection, m.PassagesSection} {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// flatFacts merges the relationship strings and the fact strings into one
// list, for the `Facts` field on the API response (agent callers and the UI).
func (m *multiPathResult) flatFacts() []string {
	out := make([]string, 0, len(m.RelationshipStrings)+len(m.FactStrings))
	out = append(out, m.RelationshipStrings...)
	out = append(out, m.FactStrings...)
	return out
}

// ── Small parser helpers shared across phases ────────────────────

func toInterfaceSlice(ss []string) []interface{} {
	out := make([]interface{}, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func float32sToInterface(v []float32) []interface{} {
	out := make([]interface{}, len(v))
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

func parseStringRows(res interface{}) []string {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, _ := arr[1].([]interface{})
	var out []string
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 1 {
			continue
		}
		if s, ok := cols[0].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

type relRow struct{ Src, Rel, Tgt, Fact string }

func parseRelRows(res interface{}) []relRow {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, _ := arr[1].([]interface{})
	var out []relRow
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 4 {
			continue
		}
		s, _ := cols[0].(string)
		r, _ := cols[1].(string)
		t, _ := cols[2].(string)
		f, _ := cols[3].(string)
		if s == "" || r == "" || t == "" {
			continue
		}
		out = append(out, relRow{s, r, t, f})
	}
	return out
}

type twoHopRow struct{ A, R1, B, R2, C string }

func parseTwoHopRows(res interface{}) []twoHopRow {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, _ := arr[1].([]interface{})
	var out []twoHopRow
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 5 {
			continue
		}
		a, _ := cols[0].(string)
		r1, _ := cols[1].(string)
		b, _ := cols[2].(string)
		r2, _ := cols[3].(string)
		c, _ := cols[4].(string)
		out = append(out, twoHopRow{a, r1, b, r2, c})
	}
	return out
}

type chunkRow struct{ ID, Text string }

func parseChunkRows(res interface{}) []chunkRow {
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 2 {
		return nil
	}
	rows, _ := arr[1].([]interface{})
	var out []chunkRow
	for _, row := range rows {
		cols, _ := row.([]interface{})
		if len(cols) < 2 {
			continue
		}
		id, _ := cols[0].(string)
		text, _ := cols[1].(string)
		if id == "" {
			continue
		}
		out = append(out, chunkRow{id, text})
	}
	return out
}

// Silence the unused warnings for things we may want later when this file
// grows (LLM context plumbing across goroutines).
var (
	_ = context.Background
	_ = schema.PredefinedRelations
	_ = models.QueryResult{}
)
