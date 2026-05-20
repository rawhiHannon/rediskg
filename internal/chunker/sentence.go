package chunker

import (
	"strings"
	"unicode/utf8"

	"rediskg/pkg/models"

	"github.com/google/uuid"
)

// sentenceEnders are sentence-boundary patterns used for splitting.
var sentenceEnders = []string{". ", "? ", "! ", "؟ ", "。", ".\n", "?\n", "!\n"}

// SentenceChunker splits text on sentence boundaries and caps each chunk
// at a maximum token (word) count. This mirrors GraphRAG-SDK's
// TokenTextSplitter but uses sentence-aware boundaries instead of raw
// token offsets, producing chunks that always end at sentence boundaries.
type SentenceChunker struct{}

func (SentenceChunker) ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk {
	var chunks []*models.Chunk
	for _, doc := range docs {
		sentences := splitSentences(doc.Content)
		merged := mergeSentences(sentences, chunkSize, overlap)
		sections := extractSectionHeadings(doc.Content)

		for i, text := range merged {
			meta := copyMetadata(doc.Metadata)
			section := findSectionForChunk(text, sections, doc.Content)
			if section != "" {
				meta["section"] = section
				text = "Context: Section = " + section + "\n\n" + text
			}
			chunks = append(chunks, &models.Chunk{
				ID:       uuid.New().String()[:32],
				Text:     text,
				Source:   doc.Source,
				Index:    i,
				Metadata: meta,
			})
		}
	}
	return chunks
}

// splitSentences splits text into individual sentences.
func splitSentences(text string) []string {
	// First split on paragraph breaks
	paragraphs := strings.Split(text, "\n\n")
	var sentences []string

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		// Split paragraph into sentences
		remaining := para
		for remaining != "" {
			bestIdx := -1
			bestLen := 0
			for _, sep := range sentenceEnders {
				idx := strings.Index(remaining, sep)
				if idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
					bestIdx = idx
					bestLen = len(sep)
				}
			}
			if bestIdx < 0 {
				// No more sentence boundaries — take the rest
				s := strings.TrimSpace(remaining)
				if s != "" {
					sentences = append(sentences, s)
				}
				break
			}
			s := strings.TrimSpace(remaining[:bestIdx+bestLen])
			if s != "" {
				sentences = append(sentences, s)
			}
			remaining = remaining[bestIdx+bestLen:]
		}
	}
	return sentences
}

// mergeSentences groups sentences into chunks capped at chunkSize characters
// with overlap measured in characters from the tail of the previous chunk.
func mergeSentences(sentences []string, chunkSize, overlap int) []string {
	if len(sentences) == 0 {
		return nil
	}

	var chunks []string
	var current strings.Builder

	for _, sent := range sentences {
		sentLen := utf8.RuneCountInString(sent)
		curLen := utf8.RuneCountInString(current.String())

		if curLen+sentLen+1 > chunkSize && current.Len() > 0 {
			chunk := strings.TrimSpace(current.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			overlapText := getOverlap(chunk, overlap)
			current.Reset()
			current.WriteString(overlapText)
		}

		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(sent)
	}

	if current.Len() > 0 {
		chunk := strings.TrimSpace(current.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks
}
