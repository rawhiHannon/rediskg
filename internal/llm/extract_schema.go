package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/internal/schema"
	"rediskg/pkg/models"
)

// Two-phase extraction, adapted from FalkorDB GraphRAG-SDK's GraphExtraction
// strategy to our schema-constrained shape.
//
//   Pass 1 (extractNER): one LLM call per chunk, entities only. The LLM has
//   the controlled vocabulary of base types / functional roles / statuses
//   but is asked NOT to produce relationships yet.
//
//   Pass 2 (verifyAndExtractRelations): a second LLM call per chunk that
//   receives the NER output PLUS the chunk text. The LLM verifies the
//   entities (drops hallucinations, fixes names, adds anything missed),
//   then extracts relationships using only the predefined relation IDs.
//
// Graceful fallback: if pass 2 fails or returns no verified entities, we
// fall back to pass 1's entities (plus whatever edges pass 2 managed to
// produce). No chunk loses its NER work to a verify-pass failure.

// NERSystemPrompt is the pass-1 prompt — entity extraction only.
func NERSystemPrompt() string {
	baseTypes := schema.FormatBaseTypesForPrompt()
	roles := schema.FormatFunctionalRolesForPrompt()
	statuses := schema.FormatStatusesForPrompt()

	return fmt.Sprintf(`You are an expert named-entity recognition system for a knowledge graph pipeline. Extract candidate entities from the given text chunk.

RULES:
1. You are extracting CANDIDATES, not final entities. Include confidence scores for the base types you assign.
2. The text may be in ANY language. Preserve original entity names exactly as they appear.
3. You MUST use the predefined base types and functional roles below. Do NOT invent new ones.
4. Branches, subsidiaries, sites, and operational units of organizations are base_type "organization", NOT "location". Only true geographic places (cities, countries, neighborhoods) get "location".
5. Status assignment:
   - "planned" + functional_role "planned_unit" for any entity described as planned, upcoming, or future.
   - "historical" for past events / incidents already resolved.
   - "active" for currently operating entities. "unknown" if unclear.
6. Extract aliases when text uses patterns like "X may be written as A, B, or C", "X is shortened to Y", "internal aliases include A, B, C". Place them in the entity's aliases list.
7. Do NOT extract pronouns or generic references (he, she, they, the man, the woman, people, person, narrator, author, story, chapter, etc.).
8. Do NOT extract raw temporal/quantitative values (dates, times, durations, numbers) as standalone entities. Those will be carried as edge properties in pass 2.
9. Copy evidence text verbatim from the chunk; do not paraphrase.

## PREDEFINED BASE TYPES (you MUST use these):
%s

## FUNCTIONAL ROLES (assign when evidence supports):
%s

## ENTITY STATUSES (assign one):
%s

## OUTPUT FORMAT (JSON ONLY):
{
  "entities": [
    {
      "mention": "exact text as it appears in the chunk",
      "canonical_candidate": "normalized/canonical form of the name",
      "base_type_candidates": [
        {"type": "organization", "score": 0.92},
        {"type": "location", "score": 0.35}
      ],
      "domain_type_candidates": [
        {"type": "branch_office", "score": 0.88}
      ],
      "functional_roles": ["branch", "operated_unit"],
      "status": "active",
      "aliases": [{"text": "short name", "lang": "en"}],
      "evidence": "exact sentence from text mentioning this entity"
    }
  ]
}

Return ONLY valid JSON. No commentary.`, baseTypes, roles, statuses)
}

// VerifyAndExtractSystemPrompt is the pass-2 prompt — verify entities +
// extract relationships using the controlled relation vocabulary.
func VerifyAndExtractSystemPrompt() string {
	relations := schema.FormatRelationsForPrompt()
	statuses := schema.FormatStatusesForPrompt()

	return fmt.Sprintf(`You are an expert knowledge graph builder. Given a text chunk plus a list of entities a prior NER pass found, do two things:

A. VERIFY the entities:
   - Drop any entity that is not actually grounded in the text.
   - Fix naming errors (typos, wrong capitalization, partial names).
   - Add any relevant entity the NER pass missed.
   - For each verified entity, return the same fields the NER pass produced (mention, canonical_candidate, base_type_candidates, domain_type_candidates, functional_roles, status, aliases, evidence).

B. EXTRACT relationships among the verified entities, using ONLY the relation IDs listed below.

RULES FOR RELATIONSHIPS:
1. Use ONLY the relation IDs in the ALLOWED RELATIONS list. If no relation fits a fact, skip it. Do not invent new IDs.
2. NEGATIVE FACTS: When the text says "X does NOT do Y", "X no longer at Y", "X does not handle Y / process / offer / etc.", use the DOES_NOT_* / NO_CONTRACT_WITH variant when one exists in the list. Do NOT convert negations into positive relations.
3. CONDITIONAL / BACKUP FACTS: If a relation only applies "during X downtime", "if Y is unavailable", "when Z", "in case of …", "as a fallback", set status to "conditional" (or "backup" for partner-fallback service relations like PROCESSES_TESTS_FOR, TRANSPORTS_SAMPLES_FOR) and put the trigger phrase verbatim in the `+"`condition`"+` field.
4. STATUS-AWARE RELATIONS:
   - An entity with status "planned" uses PLANNED_SERVICE (not OFFERS) for services it will offer, and HAS_PLANNED_BRANCH (not HAS_BRANCH) when it appears as the target of a network/operator.
5. TEMPORAL FACTS: Put dates and time intervals under the `+"`temporal`"+` field as a map, using keys like `+"`opened_on`, `start_date`, `end_date`, `valid_through`, `occurred_on`, `expected_opening`, `schedule`"+`. Do NOT create standalone date entities — dates belong on edges.
6. DIRECTION MATTERS. Relation IDs imply (source → target). Pick the right direction; the schema rejects backwards relations.
7. Copy evidence_text verbatim from the chunk; do not paraphrase.
8. Confidence: 0.90+ explicit, 0.70–0.89 strongly implied, 0.50–0.69 reasonable inference. Below 0.50 do not include.

## ALLOWED RELATION IDs (use only these):
%s

## EDGE STATUSES (assign one for each relationship):
%s

## OUTPUT FORMAT (JSON ONLY):
{
  "entities": [
    {
      "mention": "...",
      "canonical_candidate": "...",
      "base_type_candidates": [{"type": "...", "score": 0.9}],
      "domain_type_candidates": [{"type": "...", "score": 0.8}],
      "functional_roles": ["..."],
      "status": "active",
      "aliases": [{"text": "...", "lang": "en"}],
      "evidence": "..."
    }
  ],
  "relationships": [
    {
      "from_mention": "verified entity name",
      "relation_id": "RELATION_ID_FROM_LIST_ABOVE",
      "to_mention": "verified entity name",
      "evidence_text": "exact sentence supporting this fact",
      "evidence_language": "en",
      "confidence": 0.85,
      "status": "active",
      "condition": "",
      "temporal": {}
    }
  ]
}

Return ONLY valid JSON. No commentary.`, relations, statuses)
}

// ── JSON shapes ───────────────────────────────────────────────────

// nerJSON is the pass-1 response shape.
type nerJSON struct {
	Entities []entityJSON `json:"entities"`
}

// verifyJSON is the pass-2 response shape (verified entities + relationships).
type verifyJSON struct {
	Entities      []entityJSON `json:"entities"`
	Relationships []edgeJSON   `json:"relationships"`
}

// entityJSON is the per-entity shape shared by both passes.
type entityJSON struct {
	Mention            string `json:"mention"`
	CanonicalCandidate string `json:"canonical_candidate"`
	BaseTypeCandidates []struct {
		Type  string  `json:"type"`
		Score float64 `json:"score"`
	} `json:"base_type_candidates"`
	DomainTypeCandidates []struct {
		Type  string  `json:"type"`
		Score float64 `json:"score"`
	} `json:"domain_type_candidates"`
	FunctionalRoles []string `json:"functional_roles"`
	Status          string   `json:"status"`
	Aliases         []struct {
		Text string `json:"text"`
		Lang string `json:"lang"`
	} `json:"aliases"`
	Evidence string `json:"evidence"`
}

type edgeJSON struct {
	FromMention      string            `json:"from_mention"`
	RelationID       string            `json:"relation_id"`
	ToMention        string            `json:"to_mention"`
	EvidenceText     string            `json:"evidence_text"`
	EvidenceLanguage string            `json:"evidence_language"`
	Confidence       float64           `json:"confidence"`
	Status           string            `json:"status"`
	Condition        string            `json:"condition"`
	Temporal         map[string]string `json:"temporal"`
}

// ── Helpers — JSON entity → CandidateEntity ──────────────────────

// toCandidateEntity converts a parsed entity from either pass into the
// internal CandidateEntity, applying schema validation on roles, status,
// and base types.
func toCandidateEntity(e entityJSON, chunkID string) (models.CandidateEntity, bool) {
	mention := strings.TrimSpace(e.Mention)
	if mention == "" {
		return models.CandidateEntity{}, false
	}
	ce := models.CandidateEntity{
		Mention:       strings.ToLower(mention),
		CanonicalName: strings.ToLower(strings.TrimSpace(e.CanonicalCandidate)),
		ChunkID:       chunkID,
	}
	if ce.CanonicalName == "" {
		ce.CanonicalName = ce.Mention
	}
	for _, role := range e.FunctionalRoles {
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "" && schema.IsValidFunctionalRole(role) {
			ce.FunctionalRoles = append(ce.FunctionalRoles, role)
		}
	}
	status := strings.ToLower(strings.TrimSpace(e.Status))
	if status != "" && schema.IsValidStatus(status) {
		ce.Status = status
	}
	for _, bt := range e.BaseTypeCandidates {
		t := strings.ToLower(strings.TrimSpace(bt.Type))
		if t != "" && schema.IsValidBaseType(t) {
			ce.BaseTypes = append(ce.BaseTypes, models.ScoredType{Type: t, Score: bt.Score})
		}
	}
	for _, dt := range e.DomainTypeCandidates {
		t := strings.ToLower(strings.TrimSpace(dt.Type))
		if t != "" {
			ce.DomainTypes = append(ce.DomainTypes, models.ScoredType{Type: t, Score: dt.Score})
		}
	}
	for _, a := range e.Aliases {
		if a.Text != "" {
			ce.Aliases = append(ce.Aliases, models.LangText{Text: strings.ToLower(a.Text), Lang: a.Lang})
		}
	}
	if e.Evidence != "" {
		ce.Evidence = append(ce.Evidence, models.EvidenceRef{
			Text:    e.Evidence,
			ChunkID: chunkID,
		})
	}
	return ce, true
}

// toCandidateEdge converts a parsed relationship into the internal
// CandidateEdge, normalising the relation ID via the schema alias index.
// When the raw relation was an *inverse* alias (e.g. the LLM emitted
// "MANAGED_BY"), the from/to endpoints are swapped so the canonical
// relation direction is preserved.
func toCandidateEdge(e edgeJSON, chunkID string, idx int) (models.CandidateEdge, bool) {
	from := strings.ToLower(strings.TrimSpace(e.FromMention))
	to := strings.ToLower(strings.TrimSpace(e.ToMention))
	rawRelation := strings.ToUpper(strings.TrimSpace(e.RelationID))
	if from == "" || to == "" || rawRelation == "" {
		return models.CandidateEdge{}, false
	}
	relationID, known, flip := schema.ResolveRelationWithFlip(rawRelation)
	if relationID == "" {
		return models.CandidateEdge{}, false
	}
	if flip {
		from, to = to, from
	}
	schemaFit := 0.3
	if known {
		schemaFit = 1.0
	}
	edge := models.CandidateEdge{
		ID:             fmt.Sprintf("e_%s_%d", chunkID, idx),
		FromMention:    from,
		RelationRaw:    rawRelation,
		RelationID:     relationID,
		ToMention:      to,
		EvidenceText:   e.EvidenceText,
		EvidenceLang:   e.EvidenceLanguage,
		ChunkID:        chunkID,
		EvidenceScore:  e.Confidence,
		SchemaFitScore: schemaFit,
		Confidence:     e.Confidence,
		Temporal:       e.Temporal,
	}
	status := strings.ToLower(strings.TrimSpace(e.Status))
	if status != "" && schema.IsValidEdgeStatus(status) {
		edge.Status = status
	}
	if cond := strings.TrimSpace(e.Condition); cond != "" {
		edge.Condition = cond
	}
	return edge, true
}

// ── Pass 1: NER only ─────────────────────────────────────────────

// extractNER asks the LLM for entities only, returning a slice of
// schema-validated CandidateEntities. Empty slice + nil err means the LLM
// produced no usable entities (vs. an LLM transport error).
func extractNER(client *Client, text, chunkID string) ([]models.CandidateEntity, error) {
	resp, err := client.Complete(NERSystemPrompt(), fmt.Sprintf("```%s```", text))
	if err != nil {
		return nil, fmt.Errorf("NER call: %w", err)
	}
	var parsed nerJSON
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		log.Printf("Warning: NER JSON parse failed for chunk %s: %v\n  response: %.200s", chunkID, err, resp)
		return nil, nil
	}
	out := make([]models.CandidateEntity, 0, len(parsed.Entities))
	for _, e := range parsed.Entities {
		if ce, ok := toCandidateEntity(e, chunkID); ok {
			out = append(out, ce)
		}
	}
	return out, nil
}

// ── Pass 2: verify + extract relations ───────────────────────────

// nerSummaryForPrompt strips a CandidateEntity slice to the field set the
// verify-pass LLM needs to see — mention + canonical + types + status. We
// don't ship evidence text back (the LLM already has the chunk).
type nerSummary struct {
	Mention            string   `json:"mention"`
	CanonicalCandidate string   `json:"canonical_candidate"`
	BaseTypes          []string `json:"base_types"`
	DomainTypes        []string `json:"domain_types"`
	FunctionalRoles    []string `json:"functional_roles,omitempty"`
	Status             string   `json:"status,omitempty"`
}

func nerSummaryList(ents []models.CandidateEntity) []nerSummary {
	out := make([]nerSummary, 0, len(ents))
	for _, e := range ents {
		s := nerSummary{
			Mention:            e.Mention,
			CanonicalCandidate: e.CanonicalName,
			FunctionalRoles:    e.FunctionalRoles,
			Status:             e.Status,
		}
		for _, bt := range e.BaseTypes {
			s.BaseTypes = append(s.BaseTypes, bt.Type)
		}
		for _, dt := range e.DomainTypes {
			s.DomainTypes = append(s.DomainTypes, dt.Type)
		}
		out = append(out, s)
	}
	return out
}

// verifyAndExtractRelations runs pass 2: the LLM gets the chunk text + the
// NER pass's entities, returns (verified entities, relationships, error).
// An LLM transport error returns err; a JSON parse failure logs and
// returns empty slices (so the caller falls back to NER entities).
func verifyAndExtractRelations(client *Client, text, chunkID string, ners []models.CandidateEntity) ([]models.CandidateEntity, []models.CandidateEdge, error) {
	nerJSONBytes, _ := json.Marshal(nerSummaryList(ners))
	userPrompt := fmt.Sprintf(
		"## PRE-EXTRACTED ENTITIES (from NER pass):\n%s\n\n## TEXT CHUNK:\n```%s```",
		string(nerJSONBytes), text,
	)
	resp, err := client.Complete(VerifyAndExtractSystemPrompt(), userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("verify+extract call: %w", err)
	}
	var parsed verifyJSON
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		log.Printf("Warning: verify+extract JSON parse failed for chunk %s: %v\n  response: %.200s", chunkID, err, resp)
		return nil, nil, nil
	}
	entities := make([]models.CandidateEntity, 0, len(parsed.Entities))
	for _, e := range parsed.Entities {
		if ce, ok := toCandidateEntity(e, chunkID); ok {
			entities = append(entities, ce)
		}
	}
	edges := make([]models.CandidateEdge, 0, len(parsed.Relationships))
	for i, r := range parsed.Relationships {
		if ed, ok := toCandidateEdge(r, chunkID, i); ok {
			edges = append(edges, ed)
		}
	}
	return entities, edges, nil
}

// ── Hybrid NER → LLM verify ──────────────────────────────────────

// VerifyAndExtractFromNER runs only the verify+relations LLM pass using
// entity spans from an external NER service (GLiNER, spaCy, etc.) instead
// of a prior LLM NER pass. This cuts LLM calls in half per chunk.
//
// nerEntitiesJSON should be a JSON string of [{mention, base_type, ner_label, ...}].
func VerifyAndExtractFromNER(client *Client, text, chunkID string, nerEntitiesJSON string) ([]models.CandidateEntity, []models.CandidateEdge, error) {
	userPrompt := fmt.Sprintf(
		"## PRE-EXTRACTED ENTITIES (from local NER model — verify and enrich these):\n%s\n\n## TEXT CHUNK:\n```%s```",
		nerEntitiesJSON, text,
	)
	resp, err := client.Complete(VerifyAndExtractSystemPrompt(), userPrompt)
	if err != nil {
		return nil, nil, fmt.Errorf("hybrid verify+extract call: %w", err)
	}
	var parsed verifyJSON
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		log.Printf("Warning: hybrid verify+extract JSON parse failed for chunk %s: %v\n  response: %.200s", chunkID, err, resp)
		return nil, nil, nil
	}
	entities := make([]models.CandidateEntity, 0, len(parsed.Entities))
	for _, e := range parsed.Entities {
		if ce, ok := toCandidateEntity(e, chunkID); ok {
			entities = append(entities, ce)
		}
	}
	edges := make([]models.CandidateEdge, 0, len(parsed.Relationships))
	for i, r := range parsed.Relationships {
		if ed, ok := toCandidateEdge(r, chunkID, i); ok {
			edges = append(edges, ed)
		}
	}
	return entities, edges, nil
}

// ── Orchestrator ─────────────────────────────────────────────────

// ExtractWithSchema runs the two-pass extraction strategy adapted from
// GraphRAG-SDK's GraphExtraction. Pass 1 produces NER candidates; pass 2
// verifies them and extracts relationships. Fallback semantics match
// upstream: a failed pass 2 returns the NER entities (plus any edges that
// were parsed) instead of dropping the chunk's work.
func ExtractWithSchema(client *Client, text string, chunkID string) ([]models.CandidateEntity, []models.CandidateEdge, error) {
	// Pass 1: NER
	ners, err := extractNER(client, text, chunkID)
	if err != nil {
		// Transport-level failure — propagate so the caller can log.
		// Returning nil entities/edges means this chunk contributes nothing.
		log.Printf("Warning: NER pass failed for chunk %s: %v", chunkID, err)
		return nil, nil, nil
	}
	if len(ners) == 0 {
		// LLM produced no parseable entities — nothing to verify or relate.
		return nil, nil, nil
	}

	// Pass 2: Verify + extract relations. Falls back to NER entities on any
	// failure so we never lose pass-1 work.
	verified, edges, err := verifyAndExtractRelations(client, text, chunkID, ners)
	if err != nil {
		log.Printf("Warning: verify+extract failed for chunk %s, keeping NER entities: %v", chunkID, err)
		return ners, nil, nil
	}
	if len(verified) == 0 {
		// Pass 2 returned no entities (JSON parse failure or LLM dropped
		// everything). Keep NER entities + any edges pass 2 did manage.
		log.Printf("Warning: verify+extract returned 0 entities for chunk %s, keeping NER entities", chunkID)
		return ners, edges, nil
	}
	return verified, edges, nil
}
