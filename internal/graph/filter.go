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
	return stopwords[name]
}
