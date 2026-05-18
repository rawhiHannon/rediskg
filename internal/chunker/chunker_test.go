package chunker

import (
	"strings"
	"testing"

	"rediskg/pkg/models"
)

func TestChunkDocuments_PreservesSectionContext(t *testing.T) {
	content := `# Overview
This is the overview section.

## Branch: North Office
The North Office provides primary care services.
It has 50 staff members.

## Branch: South Office
The South Office handles emergency services.
`

	docs := []*models.Document{
		{Content: content, Source: "test.md"},
	}

	chunks := ChunkDocuments(docs, 120, 20)

	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	// At least one chunk should have section context
	foundSection := false
	for _, c := range chunks {
		if c.Metadata["section"] != "" {
			foundSection = true
		}
		if strings.Contains(c.Text, "Context: Section =") {
			foundSection = true
		}
	}
	if !foundSection {
		t.Error("expected at least one chunk to have section context")
	}
}

func TestExtractSectionHeadings(t *testing.T) {
	text := `# Main Title
Some text.

## Section A
Content A.

### Subsection A.1
Content A.1.

## Section B
Content B.
`

	headings := extractSectionHeadings(text)

	if len(headings) != 4 {
		t.Fatalf("expected 4 headings, got %d", len(headings))
	}

	expected := []string{"Main Title", "Section A", "Subsection A.1", "Section B"}
	for i, h := range headings {
		if h.Text != expected[i] {
			t.Errorf("heading %d: expected %q, got %q", i, expected[i], h.Text)
		}
	}
}

func TestFindSectionForChunk(t *testing.T) {
	fullText := `# Overview
Overview text here.

## Branch: North
North branch details here with services.
`

	headings := extractSectionHeadings(fullText)

	section := findSectionForChunk("North branch details here with services.", headings, fullText)
	if section != "Branch: North" {
		t.Errorf("expected section 'Branch: North', got %q", section)
	}
}

func TestFilterRawValueEntities_Integration(t *testing.T) {
	// Verify chunks don't lose content when section context is added
	content := "Short document with no headings."
	docs := []*models.Document{
		{Content: content, Source: "test.txt"},
	}

	chunks := ChunkDocuments(docs, 500, 50)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "Short document") {
		t.Error("chunk text should contain original content")
	}
}
