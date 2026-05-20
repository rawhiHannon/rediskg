package chunker

import (
	"strings"
	"unicode/utf8"

	"rediskg/pkg/models"

	"github.com/google/uuid"
)

// StructuralChunker splits documents by heading hierarchy (markdown # headings).
// Each section under a heading becomes a chunk. If a section exceeds chunkSize,
// it falls back to recursive character splitting within that section.
// This preserves document structure and ensures each chunk has its heading context.
type StructuralChunker struct{}

func (StructuralChunker) ChunkDocuments(docs []*models.Document, chunkSize, overlap int) []*models.Chunk {
	var chunks []*models.Chunk
	for _, doc := range docs {
		sections := splitByHeadings(doc.Content)
		idx := 0
		for _, sec := range sections {
			body := strings.TrimSpace(sec.body)
			if body == "" {
				continue
			}

			meta := copyMetadata(doc.Metadata)
			if sec.heading != "" {
				meta["section"] = sec.heading
			}
			if sec.parentHeading != "" {
				meta["parent_section"] = sec.parentHeading
			}

			prefix := ""
			if sec.heading != "" {
				prefix = "Context: Section = " + sec.heading + "\n\n"
			}

			if utf8.RuneCountInString(body) <= chunkSize {
				chunks = append(chunks, &models.Chunk{
					ID:       uuid.New().String()[:32],
					Text:     prefix + body,
					Source:   doc.Source,
					Index:    idx,
					Metadata: meta,
				})
				idx++
			} else {
				// Section too large — sub-chunk with recursive splitter
				subChunks := chunkText(body, chunkSize, overlap)
				for _, sub := range subChunks {
					m := copyMetadata(meta)
					chunks = append(chunks, &models.Chunk{
						ID:       uuid.New().String()[:32],
						Text:     prefix + sub,
						Source:   doc.Source,
						Index:    idx,
						Metadata: m,
					})
					idx++
				}
			}
		}
	}
	return chunks
}

type section struct {
	heading       string
	parentHeading string
	level         int
	body          string
}

// splitByHeadings parses markdown headings and splits content into sections.
func splitByHeadings(text string) []section {
	lines := strings.Split(text, "\n")
	var sections []section
	var currentBody strings.Builder
	currentHeading := ""
	currentLevel := 0
	parentHeading := ""

	// Track heading hierarchy for parent resolution
	headingStack := make([]string, 7) // levels 1-6

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		level := headingLevel(trimmed)
		if level > 0 {
			// Flush previous section
			if currentBody.Len() > 0 || currentHeading != "" {
				sections = append(sections, section{
					heading:       currentHeading,
					parentHeading: parentHeading,
					level:         currentLevel,
					body:          currentBody.String(),
				})
				currentBody.Reset()
			}

			heading := strings.TrimLeft(trimmed, "# ")
			// Update stack
			headingStack[level] = heading
			// Clear deeper levels
			for i := level + 1; i < len(headingStack); i++ {
				headingStack[i] = ""
			}
			// Parent is the nearest shallower heading
			parentHeading = ""
			for i := level - 1; i >= 1; i-- {
				if headingStack[i] != "" {
					parentHeading = headingStack[i]
					break
				}
			}
			currentHeading = heading
			currentLevel = level
		} else {
			if currentBody.Len() > 0 {
				currentBody.WriteString("\n")
			}
			currentBody.WriteString(line)
		}
	}

	// Flush last section
	if currentBody.Len() > 0 || currentHeading != "" {
		sections = append(sections, section{
			heading:       currentHeading,
			parentHeading: parentHeading,
			level:         currentLevel,
			body:          currentBody.String(),
		})
	}

	return sections
}

// headingLevel returns the markdown heading level (1-6) or 0 if not a heading.
func headingLevel(line string) int {
	if !strings.HasPrefix(line, "#") {
		return 0
	}
	level := 0
	for _, r := range line {
		if r == '#' {
			level++
		} else {
			break
		}
	}
	if level > 6 {
		return 0
	}
	// Must be followed by a space
	if level < len(line) && line[level] == ' ' {
		return level
	}
	return 0
}
