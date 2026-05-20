package pipeline

import (
	"encoding/json"
	"log"
	"strings"
	"sync"

	"rediskg/internal/llm"
	"rediskg/pkg/models"
)

// CorefResolver resolves coreferences (pronouns, definite descriptions) in
// chunk text before entity extraction. Replaces "he", "she", "it", "the
// company", "they" etc. with the actual entity names from context.
//
// This mirrors GraphRAG-SDK's FastCorefResolver but uses LLM instead of a
// local transformer model — appropriate for Go where no Python coref libs
// are available.
type CorefResolver struct {
	LLM     *llm.Client
	Workers int // concurrent LLM calls, default 4
}

const corefSystemPrompt = `You are a coreference resolution expert. Given a text passage, replace ALL pronouns and definite descriptions with the specific entity names they refer to.

Rules:
- Replace pronouns (he, she, it, they, him, her, them, his, its, their, we, our) with the actual entity name
- Replace definite descriptions ("the company", "the branch", "the service", "the manager") with the specific entity name
- Keep the text grammatically correct after replacement
- Do NOT add new information — only resolve references
- If a pronoun's referent is unclear, leave it unchanged
- Preserve all other text exactly as-is

Respond as JSON: {"resolved_text": "the text with pronouns replaced"}`

// ResolveCoref processes chunks to resolve coreferences before extraction.
// Each chunk gets its text rewritten with pronouns replaced by entity names.
// Returns the same chunks with modified Text fields.
func (cr *CorefResolver) ResolveCoref(chunks []*models.Chunk) []*models.Chunk {
	if cr.LLM == nil {
		return chunks
	}

	workers := cr.Workers
	if workers <= 0 {
		workers = 4
	}

	// Only process chunks that likely contain pronouns.
	pronouns := []string{" he ", " she ", " it ", " they ", " him ", " her ",
		" them ", " his ", " its ", " their ", " we ", " our ",
		" the company", " the branch", " the organization", " the service"}

	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	resolved := 0
	var mu sync.Mutex

	for _, chunk := range chunks {
		lower := strings.ToLower(chunk.Text)
		hasPronouns := false
		for _, p := range pronouns {
			if strings.Contains(lower, p) {
				hasPronouns = true
				break
			}
		}
		if !hasPronouns {
			continue
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(c *models.Chunk) {
			defer wg.Done()
			defer func() { <-sem }()

			resolvedText := cr.resolveChunk(c.Text)
			if resolvedText != "" && resolvedText != c.Text {
				c.Text = resolvedText
				mu.Lock()
				resolved++
				mu.Unlock()
			}
		}(chunk)
	}

	wg.Wait()
	if resolved > 0 {
		log.Printf("  Coreference resolution: rewrote %d/%d chunks", resolved, len(chunks))
	}
	return chunks
}

func (cr *CorefResolver) resolveChunk(text string) string {
	userPrompt := "Resolve coreferences in the following text:\n\n" + text

	resp, err := cr.LLM.Complete(corefSystemPrompt, userPrompt)
	if err != nil {
		log.Printf("  Coref resolution failed: %v", err)
		return ""
	}

	var result struct {
		ResolvedText string `json:"resolved_text"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		// Try to extract the text directly if JSON parsing fails.
		resp = strings.TrimSpace(resp)
		if len(resp) > 0 {
			return resp
		}
		return ""
	}
	return result.ResolvedText
}
