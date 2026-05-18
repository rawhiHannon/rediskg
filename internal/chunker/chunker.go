package chunker

import (
	"strings"
	"unicode/utf8"

	"rediskg/pkg/models"

	"github.com/google/uuid"
)

// separators defines the split hierarchy, similar to LangChain's RecursiveCharacterTextSplitter.
// Includes Arabic (، and ؟) and CJK (。and 、) punctuation for multilingual support.
var separators = []string{"\n\n", "\n", ". ", "? ", "! ", "؟ ", "。", "، ", "; ", ", ", "、", " "}

// ChunkDocuments splits documents into overlapping chunks.
// Each chunk carries section context from the nearest preceding heading.
func ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk {
	var chunks []*models.Chunk

	for _, doc := range docs {
		docChunks := chunkText(doc.Content, chunkSize, overlap)
		sections := extractSectionHeadings(doc.Content)

		for i, text := range docChunks {
			meta := copyMetadata(doc.Metadata)
			// Find the section heading that applies to this chunk
			section := findSectionForChunk(text, sections, doc.Content)
			if section != "" {
				meta["section"] = section
				// Prepend section context to chunk text so the LLM knows which section it's extracting from
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

// extractSectionHeadings finds markdown-style headings in text.
// Returns heading text and byte offset pairs.
type sectionHeading struct {
	Text   string
	Offset int
}

func extractSectionHeadings(text string) []sectionHeading {
	var headings []sectionHeading
	lines := strings.Split(text, "\n")
	offset := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// Strip leading '#' characters
			heading := strings.TrimLeft(trimmed, "# ")
			if heading != "" {
				headings = append(headings, sectionHeading{Text: heading, Offset: offset})
			}
		}
		offset += len(line) + 1 // +1 for the newline
	}
	return headings
}

// findSectionForChunk returns the most recent heading that precedes or is in the chunk.
func findSectionForChunk(chunkText string, headings []sectionHeading, fullText string) string {
	if len(headings) == 0 {
		return ""
	}

	// Find where the chunk starts in the full text
	chunkStart := strings.Index(fullText, chunkText[:min(len(chunkText), 80)])
	if chunkStart < 0 {
		return headings[0].Text // fallback to first heading
	}

	// Find the last heading before or at chunk start
	best := ""
	for _, h := range headings {
		if h.Offset <= chunkStart {
			best = h.Text
		}
	}
	return best
}

func copyMetadata(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// chunkText recursively splits text into chunks of the target size with overlap.
func chunkText(text string, chunkSize, overlap int) []string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= chunkSize {
		if len(text) == 0 {
			return nil
		}
		return []string{text}
	}

	// Find the best separator
	parts := splitWithBestSeparator(text, chunkSize)

	// Merge parts into chunks with overlap
	return mergeWithOverlap(parts, chunkSize, overlap)
}

// splitWithBestSeparator tries each separator in order and returns the split parts.
func splitWithBestSeparator(text string, chunkSize int) []string {
	for _, sep := range separators {
		parts := strings.Split(text, sep)
		if len(parts) > 1 {
			// Re-attach the separator to each part (except the last)
			result := make([]string, 0, len(parts))
			for i, part := range parts {
				if i < len(parts)-1 {
					result = append(result, part+sep)
				} else {
					result = append(result, part)
				}
			}
			return result
		}
	}

	// No separator worked — hard split by rune (safe for multi-byte UTF-8)
	runes := []rune(text)
	var parts []string
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[i:end]))
	}
	return parts
}

// mergeWithOverlap combines text parts into chunks, ensuring overlap between consecutive chunks.
func mergeWithOverlap(parts []string, chunkSize, overlap int) []string {
	var chunks []string
	var current strings.Builder

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if utf8.RuneCountInString(current.String())+utf8.RuneCountInString(part) > chunkSize && current.Len() > 0 {
			chunk := strings.TrimSpace(current.String())
			if chunk != "" {
				chunks = append(chunks, chunk)
			}

			// Start next chunk with overlap from the end of current chunk
			overlapText := getOverlap(chunk, overlap)
			current.Reset()
			current.WriteString(overlapText)
		}

		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(part)
	}

	// Flush remaining
	if current.Len() > 0 {
		chunk := strings.TrimSpace(current.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}

	return chunks
}

// getOverlap returns the last `size` runes of the text for chunk overlap.
func getOverlap(text string, size int) string {
	runes := []rune(text)
	if size <= 0 || len(runes) <= size {
		return text
	}

	overlap := string(runes[len(runes)-size:])
	// Try to start at a word boundary
	if idx := strings.Index(overlap, " "); idx > 0 && idx < len(overlap)/2 {
		overlap = overlap[idx+1:]
	}
	return overlap
}
