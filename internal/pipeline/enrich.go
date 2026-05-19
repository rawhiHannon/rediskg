package pipeline

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"rediskg/pkg/models"
)

// ---------------------------------------------------------------------------
// Service canonicalization
// ---------------------------------------------------------------------------

// knownServiceSynonyms maps noisy service variants to a single canonical form.
// These are domain-stable singular/plural and phrasing collapses.
var knownServiceSynonyms = map[string]string{
	"blood test":                     "blood tests",
	"basic blood test":               "blood tests",
	"basic blood tests":              "blood tests",
	"routine blood test":             "blood tests",
	"routine blood tests":            "blood tests",
	"routine walk-in blood test":     "blood tests",
	"routine walk-in blood tests":    "blood tests",
	"corporate blood panel":          "blood tests",
	"corporate blood panels":         "blood tests",
	"corporate screening blood panel":  "blood tests",
	"corporate screening blood panels": "blood tests",
	"vaccination":                    "vaccinations",
	"routine vaccination":            "vaccinations",
	"routine vaccination administration": "vaccinations",
	"vaccine administration":         "vaccinations",
}

// addServiceCanonRules adds deterministic service-name collapse rules to the
// alias map BEFORE canonical selection and edge rewriting, so both entities and
// edges converge on a single canonical service node.
//
// Three deterministic passes, applied only to service-typed entities:
//  1. Known synonym table (e.g. "blood test" -> "blood tests").
//  2. Generic singular -> plural collapse when both forms were extracted.
//  3. Modifier-prefixed variants ("basic X", "corporate X") fold into the
//     bare service when the bare service also exists.
func addServiceCanonRules(entities []models.CandidateEntity, aliasMap map[string]string) {
	// Collect the set of service-typed entity names actually present.
	serviceNames := map[string]bool{}
	for _, e := range entities {
		name := canonName(e)
		if name == "" {
			continue
		}
		for _, bt := range e.BaseTypes {
			if bt.Type == "service" && bt.Score >= 0.5 {
				serviceNames[name] = true
				break
			}
		}
	}
	if len(serviceNames) == 0 {
		return
	}

	added := 0
	resolve := func(n string) string {
		// Follow alias chain to its terminal target.
		seen := map[string]bool{}
		for {
			next, ok := aliasMap[n]
			if !ok || next == n || seen[n] {
				return n
			}
			seen[n] = true
			n = next
		}
	}

	// Pass 1: known synonyms.
	for variant, canonical := range knownServiceSynonyms {
		if !serviceNames[variant] {
			continue
		}
		if variant == canonical {
			continue
		}
		if _, exists := aliasMap[variant]; !exists {
			aliasMap[variant] = canonical
			added++
		}
	}

	// Pass 2: generic singular -> plural collapse (only when both were extracted).
	for name := range serviceNames {
		if _, mapped := aliasMap[name]; mapped {
			continue
		}
		plural := name + "s"
		if strings.HasSuffix(name, "s") {
			continue
		}
		if serviceNames[plural] && resolve(plural) != name {
			aliasMap[name] = plural
			added++
		}
	}

	// Pass 3: modifier-prefixed variants fold into the bare service.
	modifiers := []string{"basic ", "routine ", "corporate ", "general ", "standard ", "walk-in ", "walk in ", "on-site ", "onsite "}
	for name := range serviceNames {
		if _, mapped := aliasMap[name]; mapped {
			continue
		}
		for _, m := range modifiers {
			if !strings.HasPrefix(name, m) {
				continue
			}
			bare := strings.TrimSpace(strings.TrimPrefix(name, m))
			if bare == "" || bare == name {
				continue
			}
			target := ""
			if serviceNames[bare] {
				target = bare
			} else if serviceNames[bare+"s"] {
				target = bare + "s"
			}
			if target != "" && resolve(target) != name {
				aliasMap[name] = target
				added++
			}
			break
		}
	}

	if added > 0 {
		log.Printf("  Service canonicalization: added %d collapse rules", added)
	}
}

func canonName(e models.CandidateEntity) string {
	n := strings.ToLower(strings.TrimSpace(e.CanonicalName))
	if n == "" {
		n = strings.ToLower(strings.TrimSpace(e.Mention))
	}
	return n
}

// ---------------------------------------------------------------------------
// Deterministic temporal extraction
// ---------------------------------------------------------------------------

var (
	reISODate   = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})\b`)
	reMonthDay  = regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{1,2},?\s+\d{4}\b`)
	reMonthYear = regexp.MustCompile(`(?i)\b(january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{4}\b`)
	reQuarter   = regexp.MustCompile(`(?i)\bq[1-4]\s+\d{4}\b`)
	reYear      = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	reSchedule  = regexp.MustCompile(`(?i)\b(every\s+\w+|daily|weekly|biweekly|monthly|quarterly|annually|twice\s+per\s+\w+|once\s+per\s+\w+)\b`)
)

// firstDate returns the first date-like token in s, preferring the most specific
// format (ISO > Month DD, YYYY > Month YYYY > Q# YYYY > bare year).
func firstDate(s string) string {
	for _, re := range []*regexp.Regexp{reISODate, reMonthDay, reMonthYear, reQuarter} {
		if m := re.FindString(s); m != "" {
			return strings.TrimSpace(strings.TrimRight(m, ","))
		}
	}
	if m := reYear.FindString(s); m != "" {
		return m
	}
	return ""
}

// temporalCue maps an evidence phrase to the temporal field it populates.
type temporalCue struct {
	key     string
	phrases []string
}

var temporalCues = []temporalCue{
	{"expected_opening", []string{"expected to open", "scheduled to open", "planned to open", "opening in", "will open", "set to open", "due to open", "to open in"}},
	{"opened_on", []string{"opened on", "opened in", "opened its", "launched on", "launched in", "operational since", "open since", "in operation since", "began operations", "started operations"}},
	{"valid_through", []string{"valid through", "valid until", "expires on", "expires", "expiration", "valid to", "in effect until", "through "}},
	{"start_date", []string{"effective from", "effective on", "effective ", "starting", "commenced on", "commencing", "began on", "start date", "in effect since", "signed on", "entered into on"}},
	{"end_date", []string{"ends on", "ending on", "terminates on", "terminated on", "termination date", "expired on"}},
	{"occurred_on", []string{"occurred on", "occurred at", "happened on", "took place on", "reported on", "was reported on", "detected on", "resolved on", "incident on"}},
}

// extractTemporalFacts populates edge.Temporal and branch-entity properties from
// evidence text deterministically. The persistence path (KGEdge -> EdgeRecord ->
// FalkorDB) is already wired; this fills the values that the LLM rarely emits.
func extractTemporalFacts(fg *models.FinalGraph) {
	entByName := map[string]*models.KGEntity{}
	for i := range fg.Entities {
		entByName[fg.Entities[i].CanonicalName] = &fg.Entities[i]
		if fg.Entities[i].Properties == nil {
			fg.Entities[i].Properties = map[string]interface{}{}
		}
	}

	edgeFilled, entFilled := 0, 0
	for i := range fg.Edges {
		e := &fg.Edges[i]
		if len(e.Evidence) == 0 {
			continue
		}
		text := e.Evidence[0].Text
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)

		if e.Temporal == nil {
			e.Temporal = map[string]string{}
		}

		// Recurring schedule (kept verbatim, not a calendar date).
		if e.RelationID == "OPERATES_SCHEDULE" || strings.Contains(lower, "schedule") || strings.Contains(lower, "every ") {
			if _, ok := e.Temporal["schedule"]; !ok {
				if sch := reSchedule.FindString(lower); sch != "" {
					e.Temporal["schedule"] = strings.TrimSpace(sch)
					edgeFilled++
				}
			}
		}

		date := firstDate(text)
		if date == "" {
			continue
		}

		for _, cue := range temporalCues {
			if _, already := e.Temporal[cue.key]; already {
				continue
			}
			if !containsAny(lower, cue.phrases) {
				continue
			}
			// Relation/status sanity: planned units get expected_opening, not opened_on.
			if cue.key == "opened_on" && (e.Status == "planned" || e.RelationID == "HAS_PLANNED_BRANCH") {
				continue
			}
			if cue.key == "expected_opening" && e.Status != "planned" && e.RelationID != "HAS_PLANNED_BRANCH" && !containsAny(lower, []string{"expected", "scheduled", "planned", "will open"}) {
				continue
			}
			e.Temporal[cue.key] = date
			edgeFilled++

			// Mirror branch open dates onto the branch entity so node-level
			// queries ("which branch opened first/newest") work directly.
			if cue.key == "opened_on" || cue.key == "expected_opening" {
				if be := entByName[e.To]; be != nil {
					if _, ok := be.Properties[cue.key]; !ok {
						be.Properties[cue.key] = date
						entFilled++
					}
				}
			}
		}

		if len(e.Temporal) == 0 {
			e.Temporal = nil
		}
	}

	if edgeFilled > 0 || entFilled > 0 {
		log.Printf("  Temporal extraction: %d edge fields, %d entity fields", edgeFilled, entFilled)
	}
}

// ---------------------------------------------------------------------------
// Deterministic HAS_BRANCH completion
// ---------------------------------------------------------------------------

var branchDomainHints = []string{"branch", "clinic", "hub", "center", "centre", "care", "site", "office"}

// completeBranchEdges deterministically recovers missing HAS_BRANCH edges.
//
// When an organization already has at least one HAS_BRANCH edge it is treated
// as a network "hub". Any active branch-typed entity whose name shares the
// network's leading token (e.g. "cedargate ...") and that is not already linked
// gets a synthetic HAS_BRANCH (or HAS_PLANNED_BRANCH for planned units) edge.
// This restores recall lost when strict constraints dropped under-evidenced
// branch edges, without inventing cross-network links.
func completeBranchEdges(edges []models.CandidateEdge, entities map[string]*models.CanonicalEntity) []models.CandidateEdge {
	// Networks = sources of an existing HAS_BRANCH / HAS_PLANNED_BRANCH edge.
	networks := map[string]bool{}
	existing := map[string]bool{} // "network|branch"
	for _, e := range edges {
		if e.RelationID == "HAS_BRANCH" || e.RelationID == "HAS_PLANNED_BRANCH" {
			networks[e.FromMention] = true
			existing[e.FromMention+"|"+e.ToMention] = true
		}
	}
	if len(networks) == 0 {
		return edges
	}

	isBranchEntity := func(ent *models.CanonicalEntity) bool {
		if ent == nil || !hasBaseType(ent.BaseTypes, "organization") {
			return false
		}
		for _, dt := range ent.DomainTypes {
			if containsAny(strings.ToLower(dt), branchDomainHints) {
				return true
			}
		}
		for _, r := range ent.FunctionalRoles {
			if r == "branch" || r == "operated_unit" {
				return true
			}
		}
		name := strings.ToLower(ent.CanonicalName)
		return containsAny(name, branchDomainHints)
	}

	added := 0
	for network := range networks {
		tokens := strings.Fields(strings.ToLower(network))
		if len(tokens) == 0 {
			continue
		}
		core := tokens[0]
		if len(core) < 4 {
			continue // too generic to match safely
		}
		for name, ent := range entities {
			if name == network {
				continue
			}
			if !strings.Contains(strings.ToLower(name), core) {
				continue
			}
			if !isBranchEntity(ent) {
				continue
			}
			if ent.Status == "historical" || ent.Status == "former" || ent.Status == "inactive" {
				continue
			}
			if existing[network+"|"+name] {
				continue
			}
			rel := "HAS_BRANCH"
			status := "active"
			if ent.IsPlanned() {
				rel = "HAS_PLANNED_BRANCH"
				status = "planned"
			}
			edges = append(edges, models.CandidateEdge{
				ID:             fmt.Sprintf("e_branchcomplete_%s_%s", core, name),
				FromMention:    network,
				RelationRaw:    rel,
				RelationID:     rel,
				ToMention:      name,
				EvidenceText:   fmt.Sprintf("%s operates %s as a branch.", network, name),
				EvidenceLang:   "en",
				EvidenceScore:  0.8,
				SchemaFitScore: 1.0,
				Confidence:     0.8,
				Status:         status,
			})
			existing[network+"|"+name] = true
			added++
		}
	}

	if added > 0 {
		log.Printf("  Branch completion: added %d HAS_BRANCH edges", added)
	}
	return edges
}
