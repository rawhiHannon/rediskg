package loader

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"rediskg/pkg/models"
)

// supportedExtensions defines which file types we can load.
var supportedExtensions = map[string]bool{
	".txt":  true,
	".md":   true,
	".csv":  true,
	".json": true,
	".html": true,
	".xml":  true,
	// PDF support will be added separately
}

// LoadFile reads a single file and returns a Document.
func LoadFile(path string) (*models.Document, error) {
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
		if !supportedExtensions[ext] {
			return nil
		}

		doc, err := LoadFile(path)
		if err != nil {
			// Log and skip unreadable files
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
