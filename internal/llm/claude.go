package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const claudeAPIURL = "https://api.anthropic.com/v1/messages"

// --- Claude request/response types ---

type claudeRequest struct {
	Model       string           `json:"model"`
	MaxTokens   int              `json:"max_tokens"`
	System      string           `json:"system,omitempty"`
	Messages    []claudeMessage  `json:"messages"`
	Temperature float64          `json:"temperature,omitempty"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []claudeContentBlock `json:"content"`
	Error   *claudeError         `json:"error,omitempty"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// --- Claude implementation ---

func (c *Client) claudeComplete(systemPrompt, userPrompt string) (string, error) {
	model := c.cfg.LLMModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Claude doesn't have a native JSON mode, so we append a JSON instruction
	// to the system prompt to ensure JSON output.
	system := systemPrompt + "\n\nIMPORTANT: Respond with valid JSON only. No markdown, no code fences, no extra text."

	reqBody := claudeRequest{
		Model:       model,
		MaxTokens:   8192,
		System:      system,
		Temperature: 0.1,
		Messages: []claudeMessage{
			{Role: "user", Content: userPrompt},
		},
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	apiKey := c.cfg.ClaudeAPIKey
	if apiKey == "" {
		apiKey = c.cfg.APIKey
	}

	req, err := http.NewRequest("POST", claudeAPIURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read claude response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude API error %d: %s", resp.StatusCode, string(body))
	}

	var result claudeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse claude response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("claude error: %s", result.Error.Message)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("empty response from claude")
	}

	// Concatenate all text blocks
	var texts []string
	for _, block := range result.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}

	return strings.Join(texts, ""), nil
}
