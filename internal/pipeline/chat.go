package pipeline

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"rediskg/pkg/models"
)

// ChatMessage represents a single message in a conversation.
type ChatMessage struct {
	Role    string `json:"role"`    // "user", "assistant", or "system"
	Content string `json:"content"`
}

// ChatRequest holds the input for a multi-turn chat query.
type ChatRequest struct {
	Question string        `json:"question"`
	History  []ChatMessage `json:"history,omitempty"`

	// RewriteQuestion controls whether the question is rewritten using
	// conversation history for better retrieval. Default true when
	// history is non-empty.
	RewriteQuestion *bool `json:"rewrite_question,omitempty"`
}

const questionRewritePrompt = `Given the following conversation history and a follow-up question, rewrite the follow-up question to be a standalone question that captures all necessary context.

Rules:
- The rewritten question must be self-contained (understandable without the history)
- Preserve the original intent and specificity
- Resolve pronouns and references using the conversation context
- If the follow-up is already standalone, return it unchanged
- Respond as JSON: {"rewritten_question": "..."}`

// Chat handles a multi-turn conversation query. It optionally rewrites the
// question using conversation history for better retrieval, then delegates
// to the standard Query pipeline.
func (p *Pipeline) Chat(req ChatRequest) (*models.QueryResult, error) {
	question := req.Question
	if question == "" {
		return nil, fmt.Errorf("question is required")
	}

	// Rewrite question with history context if history is provided.
	shouldRewrite := len(req.History) > 0
	if req.RewriteQuestion != nil {
		shouldRewrite = *req.RewriteQuestion && len(req.History) > 0
	}

	if shouldRewrite {
		rewritten := p.rewriteQuestionWithHistory(question, req.History)
		if rewritten != "" && rewritten != question {
			log.Printf("  Chat: rewrote question: %q -> %q", question, rewritten)
			question = rewritten
		}
	}

	// Run the standard multi-path retrieval + answer generation.
	result, err := p.Query(question, true)
	if err != nil {
		return nil, err
	}

	// If history contains a system prompt, use it to re-generate the answer.
	systemPrompt := ""
	for _, msg := range req.History {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			break
		}
	}

	if systemPrompt != "" && result != nil && len(result.Facts) > 0 {
		// Re-generate answer with custom system prompt.
		mp, _ := p.runMultiPath(question)
		context := ""
		if mp != nil {
			context = mp.assembledContext()
		}
		if context == "" {
			context = strings.Join(result.Facts, "\n")
		}

		// Build message list with history for richer context.
		messages := buildChatMessages(systemPrompt, req.History, question, context)
		answer, err := p.llmClient.CompleteMessages(messages)
		if err != nil {
			log.Printf("Warning: chat completion with history failed: %v", err)
		} else {
			// Strip JSON wrapper if present.
			var answerObj struct {
				Answer string `json:"answer"`
			}
			if json.Unmarshal([]byte(answer), &answerObj) == nil && answerObj.Answer != "" {
				answer = answerObj.Answer
			}
			result.Answer = answer
		}
	}

	return result, nil
}

// rewriteQuestionWithHistory uses the LLM to rewrite a follow-up question
// into a standalone question using conversation history.
func (p *Pipeline) rewriteQuestionWithHistory(question string, history []ChatMessage) string {
	// Build a compact history summary (last 6 messages to stay within context).
	var histLines []string
	start := 0
	if len(history) > 6 {
		start = len(history) - 6
	}
	for _, msg := range history[start:] {
		if msg.Role == "system" {
			continue
		}
		content := msg.Content
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		histLines = append(histLines, fmt.Sprintf("%s: %s", msg.Role, content))
	}

	if len(histLines) == 0 {
		return question
	}

	userPrompt := fmt.Sprintf("Conversation history:\n%s\n\nFollow-up question: %s",
		strings.Join(histLines, "\n"), question)

	resp, err := p.llmClient.Complete(questionRewritePrompt, userPrompt)
	if err != nil {
		log.Printf("  Question rewrite failed: %v", err)
		return question
	}

	var result struct {
		RewrittenQuestion string `json:"rewritten_question"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return question
	}
	if result.RewrittenQuestion == "" {
		return question
	}
	return result.RewrittenQuestion
}

// buildChatMessages constructs the message list for the LLM, interleaving
// history with the new question and retrieved context.
func buildChatMessages(systemPrompt string, history []ChatMessage, question, context string) []map[string]string {
	var messages []map[string]string

	// System prompt.
	if systemPrompt != "" {
		messages = append(messages, map[string]string{"role": "system", "content": systemPrompt})
	} else {
		messages = append(messages, map[string]string{"role": "system", "content": answerPrompt})
	}

	// History (skip system messages, already handled).
	for _, msg := range history {
		if msg.Role == "system" {
			continue
		}
		messages = append(messages, map[string]string{"role": msg.Role, "content": msg.Content})
	}

	// Current question with context.
	userMsg := fmt.Sprintf("Question: %s\n\nKnowledge graph context:\n<context>\n%s\n</context>", question, context)
	messages = append(messages, map[string]string{"role": "user", "content": userMsg})

	return messages
}
