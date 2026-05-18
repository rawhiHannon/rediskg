// Deprecated: Old stop-word filtering from pre-schema pipeline. Active pipeline uses pipeline/ingest.go filterRawValueEntities.
package graph

import (
	"strings"
	"unicode/utf8"

	"rediskg/pkg/models"
)

// stopwords are generic terms that produce noisy graph nodes.
// These are checked case-insensitively against normalized (lowercased) node names.
var stopwords = map[string]bool{
	// English pronouns & determiners
	"he": true, "she": true, "it": true, "they": true, "we": true, "you": true, "i": true,
	"this": true, "that": true, "these": true, "those": true, "the": true, "a": true, "an": true,
	"his": true, "her": true, "its": true, "their": true, "our": true, "my": true, "your": true,
	// Generic status words
	"active": true, "not active": true, "inactive": true, "enabled": true, "disabled": true,
	"yes": true, "no": true, "true": true, "false": true, "none": true, "null": true, "n/a": true,
	"planned": true, "unavailable": true, "available": true, "approved": true, "pending": true,
	// Generic roles/nouns (too vague as standalone graph nodes)
	"other": true, "unknown": true, "general": true, "default": true, "various": true,
	"branch": true, "manager": true, "partner": true, "service": true, "services": true,
	"booking": true, "appointments": true, "appointment": true,
	"phone": true, "online": true, "remote": true, "on-site": true,
	"samples": true, "stock": true, "data": true, "system": true, "note": true, "notes": true,
	// Arabic pronouns
	"هو": true, "هي": true, "هم": true, "نحن": true, "أنا": true, "أنت": true,
}

// FilterTriples removes triples with stopword nodes or nodes that are too short/generic.
func FilterTriples(triples []models.Triple) []models.Triple {
	filtered := make([]models.Triple, 0, len(triples))
	for _, t := range triples {
		if isStopNode(t.Node1) || isStopNode(t.Node2) {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// FilterEntities removes entities with stopword or noise names.
func FilterEntities(entities []models.Entity) []models.Entity {
	filtered := make([]models.Entity, 0, len(entities))
	for _, e := range entities {
		if isStopNode(e.Name) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

// FilterEdges removes edges with stopword nodes.
func FilterEdges(edges []models.EdgeRecord) []models.EdgeRecord {
	filtered := make([]models.EdgeRecord, 0, len(edges))
	for _, e := range edges {
		if isStopNode(e.Node1) || isStopNode(e.Node2) {
			continue
		}
		filtered = append(filtered, e)
	}
	return filtered
}

func isStopNode(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return true
	}
	// Single character nodes are noise
	if utf8.RuneCountInString(name) <= 1 {
		return true
	}
	if stopwords[name] {
		return true
	}
	// Reject abstract/operational concepts that the LLM sometimes extracts as entities.
	// These are generic activity phrases, not proper nouns.
	if isAbstractNoise(name) {
		return true
	}
	return false
}

// isAbstractNoise detects generic operational/abstract phrases that shouldn't be graph nodes.
// A KG should store facts about named things, not generic activity descriptions.
func isAbstractNoise(lower string) bool {
	// Phrases ending in noise suffixes are usually abstract activities, not entities
	noiseSuffixes := []string{
		" strategy", " hiring", " approvals", " approval", " reporting",
		" review", " reviews", " roadmap", " planning", " management",
		" operations", " intelligence", " meetings", " procedures",
		" guidelines", " standards", " compliance", " optimization",
		" improvement", " assessment", " evaluation",
		" concerns", " disputes", " refunds", " issues", " protocols",
		" escalation", " resolution", " tracking", " oversight",
	}
	for _, s := range noiseSuffixes {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}

	// Phrases starting with noise prefixes
	noisePrefixes := []string{
		"internal ", "external ", "major ", "monthly ", "weekly ", "daily ",
		"annual ", "quarterly ", "ongoing ", "current ", "future ",
		"operational ", "administrative ",
	}
	for _, p := range noisePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}

	// Multi-word phrases that are purely abstract (no proper noun anchor)
	// If ALL words are common English words and 4+, it's likely noise
	words := strings.Fields(lower)
	if len(words) >= 4 {
		hasProperNounAnchor := false
		for _, w := range words {
			// If any word looks like it could be a proper noun (not a common word),
			// keep the entity. We check against a small set of structural words.
			if !isCommonWord(w) {
				hasProperNounAnchor = true
				break
			}
		}
		if !hasProperNounAnchor {
			return true
		}
	}

	return false
}

// isCommonWord returns true if a word is a common English structural/operational word
// (not a proper noun). Used to detect "all generic words" phrases.
func isCommonWord(w string) bool {
	common := map[string]bool{
		// operational
		"company": true, "team": true, "desk": true, "office": true, "department": true,
		"executive": true, "vendor": true, "payment": true, "financial": true,
		"clinical": true, "medical": true, "health": true, "care": true,
		"quality": true, "policy": true, "digital": true, "operations": true,
		"intelligence": true, "knowledge": true, "base": true, "graph": true,
		"rag": true, "internal": true, "external": true, "major": true,
		"monthly": true, "weekly": true, "daily": true, "annual": true,
		// connectors
		"the": true, "a": true, "an": true, "of": true, "for": true, "and": true,
		"in": true, "on": true, "at": true, "to": true, "with": true, "by": true,
		"from": true, "or": true, "not": true, "no": true,
		// adjectives
		"new": true, "old": true, "current": true, "future": true, "planned": true,
		"active": true, "inactive": true, "general": true, "specific": true,
		"routine": true, "individual": true, "preventive": true,
	}
	return common[w]
}
