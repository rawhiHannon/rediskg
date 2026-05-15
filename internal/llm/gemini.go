package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	geminiBaseURL          = "https://generativelanguage.googleapis.com/v1beta/models/"
	geminiGenerateEndpoint = ":generateContent"
	geminiEmbedEndpoint    = ":embedContent"
)

// --- Gemini request/response types ---

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature      float64 `json:"temperature,omitempty"`
	MaxOutputTokens  int     `json:"maxOutputTokens,omitempty"`
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *geminiError      `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type geminiEmbeddingRequest struct {
	Model   string        `json:"model"`
	Content geminiContent `json:"content"`
}

type geminiEmbeddingResponse struct {
	Embedding struct {
		Values []float64 `json:"values"`
	} `json:"embedding"`
	Error *geminiError `json:"error,omitempty"`
}

// --- Gemini implementation ---

func (c *Client) geminiComplete(systemPrompt, userPrompt string) (string, error) {
	model := c.cfg.LLMModel
	if model == "" {
		model = "gemini-3-pro"
	}

	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: userPrompt}}},
		},
		SystemInstruction: &geminiContent{
			Parts: []geminiPart{{Text: systemPrompt}},
		},
		GenerationConfig: &geminiGenerationConfig{
			Temperature:      0.1,
			MaxOutputTokens:  8192,
			ResponseMimeType: "application/json",
		},
	}

	url := geminiBaseURL + model + geminiGenerateEndpoint + "?key=" + c.cfg.GeminiAPIKey

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read gemini response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
	}

	var result geminiResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse gemini response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("gemini error: %s", result.Error.Message)
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from gemini")
	}

	return result.Candidates[0].Content.Parts[0].Text, nil
}

func (c *Client) geminiEmbed(text string) ([]float32, error) {
	model := c.cfg.EmbeddingModel
	if model == "" {
		model = "text-embedding-004"
	}

	reqBody := geminiEmbeddingRequest{
		Model: "models/" + model,
		Content: geminiContent{
			Parts: []geminiPart{{Text: text}},
		},
	}

	url := geminiBaseURL + model + geminiEmbedEndpoint + "?key=" + c.cfg.GeminiAPIKey

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gemini embed request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini embed error %d: %s", resp.StatusCode, string(body))
	}

	var result geminiEmbeddingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("gemini embed error: %s", result.Error.Message)
	}

	if len(result.Embedding.Values) == 0 {
		return nil, fmt.Errorf("empty embedding from gemini")
	}

	vec := make([]float32, len(result.Embedding.Values))
	for i, v := range result.Embedding.Values {
		vec[i] = float32(v)
	}
	return vec, nil
}
