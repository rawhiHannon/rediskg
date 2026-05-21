package ner

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuiltinExtractor is a pure-Go rule-based NER engine that requires no
// external services or model downloads. It uses heuristic patterns
// (capitalization, known suffixes, titles, context clues) to detect
// named entities in text. It won't match a trained ML model on accuracy,
// but it's fast, free, and works out of the box.
type BuiltinExtractor struct{}

// NewBuiltinExtractor creates a new rule-based NER extractor.
func NewBuiltinExtractor() *BuiltinExtractor {
	return &BuiltinExtractor{}
}

// Extract runs rule-based NER on the given text and returns entity spans.
func (b *BuiltinExtractor) Extract(text string) ([]Span, error) {
	spans := b.extractCapitalizedSequences(text)
	spans = deduplicateSpans(spans)
	spans = filterNoise(spans)
	return spans, nil
}

// orgSuffixes are common organization suffixes (case-insensitive match
// on the last word of a capitalized sequence).
var orgSuffixes = map[string]bool{
	"corp":         true,
	"corp.":        true,
	"corporation":  true,
	"inc":          true,
	"inc.":         true,
	"incorporated": true,
	"ltd":          true,
	"ltd.":         true,
	"limited":      true,
	"llc":          true,
	"l.l.c.":       true,
	"gmbh":         true,
	"ag":           true,
	"plc":          true,
	"co":           true,
	"co.":          true,
	"company":      true,
	"group":        true,
	"holdings":     true,
	"partners":     true,
	"associates":   true,
	"foundation":   true,
	"institute":    true,
	"university":   true,
	"college":      true,
	"bank":         true,
	"labs":         true,
	"technologies": true,
	"solutions":    true,
	"systems":      true,
	"industries":   true,
	"enterprises":  true,
	"services":     true,
	"consulting":   true,
}

// personTitles are prefixes that strongly indicate a person name follows.
var personTitles = map[string]bool{
	"mr":        true,
	"mr.":       true,
	"mrs":       true,
	"mrs.":      true,
	"ms":        true,
	"ms.":       true,
	"dr":        true,
	"dr.":       true,
	"prof":      true,
	"prof.":     true,
	"professor": true,
	"sir":       true,
	"lord":      true,
	"lady":      true,
	"rev":       true,
	"rev.":      true,
	"gen":       true,
	"gen.":      true,
	"sgt":       true,
	"sgt.":      true,
	"cpl":       true,
	"cpl.":      true,
	"pvt":       true,
	"pvt.":      true,
	"capt":      true,
	"capt.":     true,
	"col":       true,
	"col.":      true,
	"maj":       true,
	"maj.":      true,
	"lt":        true,
	"lt.":       true,
	"ceo":       true,
	"cto":       true,
	"cfo":       true,
	"coo":       true,
	"president": true,
	"chairman":  true,
	"director":  true,
	"senator":   true,
	"governor":  true,
	"mayor":     true,
	"king":      true,
	"queen":     true,
	"prince":    true,
	"princess":  true,
}

// locationIndicators are words that, when preceding a capitalized name,
// suggest a location.
var locationIndicators = map[string]bool{
	"in":    true,
	"at":    true,
	"from":  true,
	"near":  true,
	"of":    true,
	"city":  true,
	"state": true,
	"port":  true,
}

// locationSuffixes on the last word suggest a location.
var locationSuffixes = map[string]bool{
	"city":     true,
	"county":   true,
	"state":    true,
	"province": true,
	"region":   true,
	"district": true,
	"island":   true,
	"islands":  true,
	"lake":     true,
	"river":    true,
	"mountain": true,
	"valley":   true,
	"bay":      true,
	"harbor":   true,
	"harbour":  true,
	"strait":   true,
	"park":     true,
	"forest":   true,
	"desert":   true,
	"ocean":    true,
	"sea":      true,
	"creek":    true,
}

// stopWords are common English words that should not be standalone entities,
// even if capitalized at the start of a sentence.
var stopWords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true,
	"but": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "do": true, "does": true, "did": true, "will": true,
	"would": true, "could": true, "should": true, "may": true, "might": true,
	"shall": true, "can": true, "need": true, "must": true,
	"it": true, "its": true, "he": true, "she": true, "they": true,
	"them": true, "their": true, "this": true, "that": true, "these": true,
	"those": true, "which": true, "who": true, "whom": true, "whose": true,
	"what": true, "where": true, "when": true, "how": true, "why": true,
	"if": true, "then": true, "else": true, "than": true,
	"not": true, "no": true, "nor": true, "so": true, "too": true,
	"very": true, "just": true, "also": true, "only": true,
	"with": true, "from": true, "into": true, "about": true, "for": true,
	"on": true, "by": true, "to": true, "of": true, "in": true, "at": true,
	"as": true, "up": true, "out": true, "off": true, "over": true,
	"after": true, "before": true, "between": true, "under": true,
	"again": true, "further": true, "here": true, "there": true,
	"all": true, "each": true, "every": true, "both": true, "few": true,
	"more": true, "most": true, "other": true, "some": true, "such": true,
	"own": true, "same": true,
	"however": true, "therefore": true, "although": true, "because": true,
	"since": true, "while": true, "during": true, "through": true,
	"according": true, "based": true, "including": true, "following": true,
	"see": true, "note": true, "example": true,
	"i": true, "we": true, "you": true, "me": true, "my": true, "our": true,
	"your": true, "his": true, "her": true,
}

// sentenceStartVerbs are common verbs that appear capitalized at sentence
// starts but aren't entities.
var sentenceStartVerbs = map[string]bool{
	"said": true, "told": true, "asked": true, "added": true,
	"noted": true, "stated": true, "announced": true, "reported": true,
	"explained": true, "described": true, "revealed": true,
	"according": true, "founded": true, "established": true,
	"created": true, "developed": true, "built": true,
	"made": true, "started": true, "began": true,
	"received": true, "published": true, "released": true,
	"continued": true, "remained": true, "became": true,
	"signed": true, "launched": true, "introduced": true,
}

// extractCapitalizedSequences scans text for sequences of capitalized words
// and classifies them using heuristics.
func (b *BuiltinExtractor) extractCapitalizedSequences(text string) []Span {
	var spans []Span
	runes := []rune(text)
	n := len(runes)

	i := 0
	for i < n {
		// Skip non-letter characters
		if !unicode.IsLetter(runes[i]) {
			i++
			continue
		}

		// Check if this word starts with an uppercase letter
		if !unicode.IsUpper(runes[i]) {
			// Skip to end of word
			for i < n && !unicode.IsSpace(runes[i]) {
				i++
			}
			continue
		}

		// Found an uppercase letter — collect the word
		wordStart := i
		for i < n && !unicode.IsSpace(runes[i]) && runes[i] != ',' && runes[i] != ';' && runes[i] != ':' && runes[i] != '(' && runes[i] != ')' && runes[i] != '"' && runes[i] != '\'' && runes[i] != '\n' {
			i++
		}
		firstWord := string(runes[wordStart:i])
		firstWordClean := stripTrailingPunct(firstWord)

		// Check for person title
		if personTitles[strings.ToLower(firstWordClean)] {
			// Collect the name after the title
			titleStart := wordStart
			// Skip whitespace
			for i < n && unicode.IsSpace(runes[i]) {
				i++
			}
			// Collect subsequent capitalized words as the person name
			seqEnd := i
			for i < n && unicode.IsUpper(runes[i]) {
				ws := i
				for i < n && !unicode.IsSpace(runes[i]) && runes[i] != ',' && runes[i] != ';' && runes[i] != ':' && runes[i] != '(' && runes[i] != ')' && runes[i] != '"' && runes[i] != '\n' {
					i++
				}
				w := string(runes[ws:i])
				wClean := stripTrailingPunct(w)
				if stopWords[strings.ToLower(wClean)] {
					break
				}
				seqEnd = i
				// skip whitespace
				for i < n && unicode.IsSpace(runes[i]) && runes[i] != '\n' {
					i++
				}
			}
			if seqEnd > titleStart {
				entityText := strings.TrimSpace(string(runes[titleStart:seqEnd]))
				entityText = stripTrailingPunct(entityText)
				if len(entityText) > 2 {
					byteStart := len(string(runes[:titleStart]))
					byteEnd := byteStart + len(entityText)
					spans = append(spans, Span{
						Text:  entityText,
						Start: byteStart,
						End:   byteEnd,
						Label: "PERSON",
					})
				}
			}
			continue
		}

		// Check if this is at the start of a sentence (preceded by . ! ? or start of text)
		isSentenceStart := isSentenceStartPos(runes, wordStart)

		// If single capitalized word at sentence start, might just be a regular word
		// We'll still collect a sequence to see if it forms a multi-word entity

		// Collect consecutive capitalized words (allow small linking words like "of", "and", "the")
		seqStart := wordStart
		seqEnd := i
		words := []string{firstWordClean}

		// Skip whitespace and look for more capitalized words
		j := i
		for j < n && unicode.IsSpace(runes[j]) && runes[j] != '\n' {
			j++
		}

		for j < n && unicode.IsLetter(runes[j]) {
			ws := j
			for j < n && !unicode.IsSpace(runes[j]) && runes[j] != ',' && runes[j] != ';' && runes[j] != ':' && runes[j] != '(' && runes[j] != ')' && runes[j] != '"' && runes[j] != '\n' {
				j++
			}
			w := string(runes[ws:j])
			wClean := stripTrailingPunct(w)
			wLower := strings.ToLower(wClean)

			if unicode.IsUpper(runes[ws]) {
				// Another capitalized word — extend the sequence
				words = append(words, wClean)
				seqEnd = ws + len([]rune(wClean))
				i = j
				// skip whitespace
				for j < n && unicode.IsSpace(runes[j]) && runes[j] != '\n' {
					j++
				}
			} else if (wLower == "of" || wLower == "the" || wLower == "and" || wLower == "for" || wLower == "de" || wLower == "von" || wLower == "van" || wLower == "del" || wLower == "la" || wLower == "le") && j < n {
				// Small linking word — peek ahead to see if next word is capitalized
				k := j
				for k < n && unicode.IsSpace(runes[k]) {
					k++
				}
				if k < n && unicode.IsUpper(runes[k]) {
					words = append(words, wClean)
					// skip whitespace
					for j < n && unicode.IsSpace(runes[j]) && runes[j] != '\n' {
						j++
					}
				} else {
					break
				}
			} else {
				break
			}
		}

		// Now classify the collected sequence
		entityText := strings.TrimSpace(string(runes[seqStart:seqEnd]))
		entityText = stripTrailingPunct(entityText)

		if len(entityText) < 2 {
			continue
		}

		// Skip single-word entities that are likely just sentence starters
		if len(words) == 1 {
			wLower := strings.ToLower(words[0])
			if stopWords[wLower] || sentenceStartVerbs[wLower] {
				continue
			}
			// Single word at sentence start with no other signal — skip it
			// unless it looks like a proper noun (not a common English word)
			if isSentenceStart && !isLikelyProperNoun(words[0]) {
				continue
			}
		}

		label := classifyEntity(words, runes, seqStart)
		byteStart := len(string(runes[:seqStart]))
		byteEnd := byteStart + len(entityText)

		spans = append(spans, Span{
			Text:  entityText,
			Start: byteStart,
			End:   byteEnd,
			Label: label,
		})
	}

	return spans
}

// classifyEntity determines the NER label for a sequence of words.
func classifyEntity(words []string, runes []rune, startPos int) string {
	if len(words) == 0 {
		return "MISC"
	}

	lastWord := strings.ToLower(words[len(words)-1])

	// Check organization suffixes
	if orgSuffixes[lastWord] {
		return "ORG"
	}

	// Check location suffixes
	if locationSuffixes[lastWord] {
		return "LOC"
	}

	// Check for acronyms (all caps, 2-5 chars) — likely organization
	if len(words) == 1 && len(words[0]) >= 2 && len(words[0]) <= 5 && isAllCaps(words[0]) {
		return "ORG"
	}

	// Check context before the entity for location indicators
	prevWord := getPreviousWord(runes, startPos)
	if locationIndicators[strings.ToLower(prevWord)] {
		return "LOC"
	}

	// 2-3 capitalized words with no org suffix → likely person
	if len(words) >= 2 && len(words) <= 3 {
		allSimple := true
		for _, w := range words {
			wLower := strings.ToLower(w)
			if orgSuffixes[wLower] || locationSuffixes[wLower] {
				allSimple = false
				break
			}
		}
		if allSimple {
			return "PERSON"
		}
	}

	// Single proper noun — default to MISC (the LLM verify pass will refine)
	return "MISC"
}

// isLikelyProperNoun returns true if a single capitalized word looks like a
// proper noun rather than just a sentence-starting common word. We check
// if it's a known common English word.
func isLikelyProperNoun(word string) bool {
	lower := strings.ToLower(word)
	// Common words that appear at sentence starts but aren't entities
	commonWords := map[string]bool{
		"after": true, "although": true, "another": true, "any": true,
		"because": true, "before": true, "between": true, "both": true,
		"certain": true, "current": true, "different": true, "during": true,
		"early": true, "even": true, "eventually": true, "every": true,
		"finally": true, "first": true, "following": true, "former": true,
		"further": true, "general": true, "given": true, "great": true,
		"here": true, "high": true, "however": true, "important": true,
		"initial": true, "known": true, "large": true, "last": true,
		"late": true, "later": true, "least": true, "little": true,
		"local": true, "long": true, "main": true, "major": true,
		"many": true, "modern": true, "much": true, "national": true,
		"new": true, "next": true, "now": true, "number": true,
		"once": true, "original": true, "own": true, "part": true,
		"particular": true, "past": true, "present": true, "previous": true,
		"public": true, "recent": true, "second": true, "several": true,
		"significant": true, "similar": true, "since": true, "single": true,
		"small": true, "specific": true, "still": true, "such": true,
		"then": true, "there": true, "third": true, "three": true,
		"today": true, "total": true, "two": true, "under": true,
		"until": true, "upon": true, "various": true, "well": true,
		"when": true, "where": true, "while": true, "within": true,
		"without": true, "yet": true,
	}
	return !commonWords[lower] && !stopWords[lower] && !sentenceStartVerbs[lower]
}

func isAllCaps(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) && !unicode.IsUpper(r) {
			return false
		}
	}
	return true
}

func stripTrailingPunct(s string) string {
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r == '.' || r == ',' || r == ';' || r == ':' || r == '!' || r == '?' || r == ')' || r == '\'' || r == '"' {
			s = s[:len(s)-size]
		} else {
			break
		}
	}
	return s
}

func isSentenceStartPos(runes []rune, pos int) bool {
	if pos == 0 {
		return true
	}
	// Walk backwards skipping whitespace
	j := pos - 1
	for j >= 0 && unicode.IsSpace(runes[j]) {
		j--
	}
	if j < 0 {
		return true
	}
	return runes[j] == '.' || runes[j] == '!' || runes[j] == '?' || runes[j] == '\n'
}

func getPreviousWord(runes []rune, pos int) string {
	if pos == 0 {
		return ""
	}
	// Skip whitespace backwards
	j := pos - 1
	for j >= 0 && unicode.IsSpace(runes[j]) {
		j--
	}
	if j < 0 {
		return ""
	}
	// Collect the word backwards
	end := j + 1
	for j >= 0 && !unicode.IsSpace(runes[j]) {
		j--
	}
	return string(runes[j+1 : end])
}

// deduplicateSpans removes exact duplicate spans (same text, same label).
func deduplicateSpans(spans []Span) []Span {
	type key struct {
		text  string
		label string
	}
	seen := map[key]bool{}
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		k := key{strings.ToLower(s.Text), s.Label}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

// filterNoise removes spans that are too short or clearly not entities.
func filterNoise(spans []Span) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		text := strings.TrimSpace(s.Text)
		if len(text) < 2 {
			continue
		}
		lower := strings.ToLower(text)
		if stopWords[lower] {
			continue
		}
		out = append(out, s)
	}
	return out
}
