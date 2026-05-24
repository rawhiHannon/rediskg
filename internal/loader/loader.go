package loader

import (
	"encoding/csv"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ledongthuc/pdf"

	"rediskg/pkg/models"
)

// supportedExtensions maps file extensions to their loader functions.
var supportedExtensions = map[string]func(string) (*models.Document, error){
	".txt":  loadText,
	".md":   loadMarkdown,
	".csv":  loadCSV,
	".json": loadPlain,
	".html": loadHTML,
	".xml":  loadPlain,
	".pdf":  loadPDF,
}

// LoadFile reads a single file using the appropriate format-specific loader.
func LoadFile(path string) (*models.Document, error) {
	ext := strings.ToLower(filepath.Ext(path))
	loader, ok := supportedExtensions[ext]
	if !ok {
		return nil, fmt.Errorf("unsupported file type: %s", ext)
	}
	return loader(path)
}

// LoadDirectory recursively loads all supported files from a directory.
func LoadDirectory(dirPath string) ([]*models.Document, error) {
	var docs []*models.Document

	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if _, ok := supportedExtensions[ext]; !ok {
			return nil
		}

		doc, err := LoadFile(path)
		if err != nil {
			fmt.Printf("Warning: skipping %s: %v\n", path, err)
			return nil
		}

		docs = append(docs, doc)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", dirPath, err)
	}

	return docs, nil
}

// LoadText creates a Document from a raw text string.
func LoadText(text string, source string) *models.Document {
	return &models.Document{
		Content: text,
		Source:  source,
		Metadata: map[string]string{
			"type": "raw_text",
		},
	}
}

// loadPlain reads a file as-is with no special processing.
func loadPlain(path string) (*models.Document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}
	return &models.Document{
		Content: string(content),
		Source:  path,
		Metadata: map[string]string{
			"filename": filepath.Base(path),
			"ext":      filepath.Ext(path),
		},
	}, nil
}

// loadText reads a plain text file.
func loadText(path string) (*models.Document, error) {
	return loadPlain(path)
}

// loadPDF extracts text from a PDF file using a pure-Go PDF reader.
func loadPDF(path string) (*models.Document, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open PDF %s: %w", path, err)
	}
	defer f.Close()

	var sb strings.Builder
	totalPages := r.NumPage()

	for i := 1; i <= totalPages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.TrimSpace(text))
	}

	content := sb.String()
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("PDF %s contains no extractable text (may be scanned/image-based)", path)
	}

	return &models.Document{
		Content: content,
		Source:  path,
		Metadata: map[string]string{
			"filename": filepath.Base(path),
			"ext":      ".pdf",
			"pages":    fmt.Sprintf("%d", totalPages),
			"type":     "pdf",
		},
	}, nil
}

// loadMarkdown parses a Markdown file, extracting structural metadata
// (title, headings) while preserving the full content for chunking.
func loadMarkdown(path string) (*models.Document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read markdown %s: %w", path, err)
	}

	text := string(content)
	meta := map[string]string{
		"filename": filepath.Base(path),
		"ext":      ".md",
		"type":     "markdown",
	}

	// Extract title from first H1
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			meta["title"] = strings.TrimPrefix(trimmed, "# ")
			break
		}
	}

	// Extract heading structure for metadata
	headingRe := regexp.MustCompile(`(?m)^(#{1,6})\s+(.+)$`)
	matches := headingRe.FindAllStringSubmatch(text, -1)
	var headings []string
	for _, m := range matches {
		level := len(m[1])
		headings = append(headings, fmt.Sprintf("h%d:%s", level, strings.TrimSpace(m[2])))
	}
	if len(headings) > 0 {
		meta["headings"] = strings.Join(headings, "|")
	}

	return &models.Document{
		Content:  text,
		Source:   path,
		Metadata: meta,
	}, nil
}

// loadCSV reads a CSV file and converts it into a structured text format
// suitable for entity extraction. Each row becomes a record with column
// headers as field names.
func loadCSV(path string) (*models.Document, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV %s: %w", path, err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("failed to parse CSV %s: %w", path, err)
	}

	if len(records) < 2 {
		return nil, fmt.Errorf("CSV %s has no data rows", path)
	}

	headers := records[0]
	var sb strings.Builder

	for i, row := range records[1:] {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("Record %d:\n", i+1))
		for j, val := range row {
			if j < len(headers) && strings.TrimSpace(val) != "" {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", headers[j], strings.TrimSpace(val)))
			}
		}
	}

	return &models.Document{
		Content: sb.String(),
		Source:  path,
		Metadata: map[string]string{
			"filename": filepath.Base(path),
			"ext":      ".csv",
			"type":     "csv",
			"columns":  strings.Join(headers, ","),
			"rows":     fmt.Sprintf("%d", len(records)-1),
		},
	}, nil
}

// loadHTML strips HTML tags and extracts text content. Preserves paragraph
// breaks and heading structure.
func loadHTML(path string) (*models.Document, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read HTML %s: %w", path, err)
	}

	text := stripHTMLTags(string(content))

	return &models.Document{
		Content: text,
		Source:  path,
		Metadata: map[string]string{
			"filename": filepath.Base(path),
			"ext":      ".html",
			"type":     "html",
		},
	}, nil
}

// stripHTMLTags removes HTML tags, decodes common entities, and normalizes
// whitespace. Inserts newlines for block-level elements.
func stripHTMLTags(html string) string {
	// Insert newlines before block elements
	blockRe := regexp.MustCompile(`(?i)<(p|div|br|h[1-6]|li|tr|blockquote|section|article)[^>]*>`)
	html = blockRe.ReplaceAllString(html, "\n")

	// Remove script and style blocks
	scriptRe := regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</\1>`)
	html = scriptRe.ReplaceAllString(html, "")

	// Strip all remaining tags
	tagRe := regexp.MustCompile(`<[^>]+>`)
	html = tagRe.ReplaceAllString(html, "")

	// Decode common HTML entities
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")
	html = strings.ReplaceAll(html, "&nbsp;", " ")

	// Normalize whitespace: collapse runs of blank lines
	multiNewline := regexp.MustCompile(`\n{3,}`)
	html = multiNewline.ReplaceAllString(html, "\n\n")

	return strings.TrimSpace(html)
}
