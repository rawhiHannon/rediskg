package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"rediskg/pkg/config"
)

// Client provides LLM completion and embedding functionality.
type Client struct {
	cfg        *config.Config
	httpClient *http.Client
}

// NewClient creates a new LLM client based on the config provider.
func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

// Complete sends a system+user prompt to the LLM and returns the response text.
func (c *Client) Complete(systemPrompt, userPrompt string) (string, error) {
	switch c.cfg.LLMProvider {
	case "openai":
		return c.openaiComplete(systemPrompt, userPrompt)
	case "gemini":
		return c.geminiComplete(systemPrompt, userPrompt)
	case "claude":
		return c.claudeComplete(systemPrompt, userPrompt)
	case "ollama":
		return c.ollamaComplete(systemPrompt, userPrompt)
	default:
		return c.openaiComplete(systemPrompt, userPrompt)
	}
}

// Embed generates an embedding vector for the given text.
func (c *Client) Embed(text string) ([]float32, error) {
	provider := c.cfg.EmbeddingProvider
	if provider == "" {
		provider = c.cfg.LLMProvider
	}

	switch provider {
	case "openai":
		return c.openaiEmbed(text)
	case "gemini":
		return c.geminiEmbed(text)
	case "ollama":
		return c.ollamaEmbed(text)
	case "claude":
		// Claude doesn't have an embedding API; fall back to OpenAI embeddings
		return c.openaiEmbed(text)
	default:
		return c.openaiEmbed(text)
	}
}

// --- OpenAI ---

func (c *Client) openaiComplete(systemPrompt, userPrompt string) (string, error) {
	body := map[string]interface{}{
		"model": c.cfg.LLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.1,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}

	resp, err := c.openaiRequest("https://api.openai.com/v1/chat/completions", body)
	if err != nil {
		return "", err
	}

	choices, ok := resp["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	msg, ok := choices[0].(map[string]interface{})["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid message format")
	}
	content, ok := msg["content"].(string)
	if !ok {
		return "", fmt.Errorf("invalid content format")
	}
	return content, nil
}

func (c *Client) openaiEmbed(text string) ([]float32, error) {
	model := c.cfg.EmbeddingModel
	if model == "" {
		model = "text-embedding-3-small"
	}

	body := map[string]interface{}{
		"model": model,
		"input": text,
	}

	resp, err := c.openaiRequest("https://api.openai.com/v1/embeddings", body)
	if err != nil {
		return nil, err
	}

	data, ok := resp["data"].([]interface{})
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}
	embObj, ok := data[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid embedding format")
	}
	embArr, ok := embObj["embedding"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid embedding array")
	}

	vec := make([]float32, len(embArr))
	for i, v := range embArr {
		if f, ok := v.(float64); ok {
			vec[i] = float32(f)
		}
	}
	return vec, nil
}

func (c *Client) openaiRequest(url string, body map[string]interface{}) (map[string]interface{}, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return result, nil
}

// --- Ollama ---

func (c *Client) ollamaComplete(systemPrompt, userPrompt string) (string, error) {
	url := c.cfg.OllamaURL + "/api/chat"

	body := map[string]interface{}{
		"model": c.cfg.LLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"stream": false,
		"format": "json",
		"options": map[string]interface{}{
			"temperature": 0.1,
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama error %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}

	msg, ok := result["message"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("invalid ollama response format")
	}
	content, ok := msg["content"].(string)
	if !ok {
		return "", fmt.Errorf("invalid ollama content format")
	}
	return content, nil
}

func (c *Client) ollamaEmbed(text string) ([]float32, error) {
	url := c.cfg.OllamaURL + "/api/embed"

	model := c.cfg.EmbeddingModel
	if model == "" {
		model = "nomic-embed-text"
	}

	body := map[string]interface{}{
		"model": model,
		"input": text,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("ollama embed request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	embeddings, ok := result["embeddings"].([]interface{})
	if !ok || len(embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings in ollama response")
	}
	embArr, ok := embeddings[0].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid ollama embedding format")
	}

	vec := make([]float32, len(embArr))
	for i, v := range embArr {
		if f, ok := v.(float64); ok {
			vec[i] = float32(f)
		}
	}
	return vec, nil
}
