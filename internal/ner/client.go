package ner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Span represents a single named-entity span returned by a NER service.
type Span struct {
	Text  string `json:"text"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Label string `json:"label"` // e.g. "ORG", "PERSON", "LOC", "GPE", "MISC"
}

// Response is the expected JSON shape from any NER service endpoint.
type Response struct {
	Entities []Span `json:"entities"`
}

// Client calls a local NER HTTP service (GLiNER, spaCy, or any service
// that accepts POST /ner with {"text":"..."} and returns {"entities":[...]}).
type Client struct {
	URL        string
	HTTPClient *http.Client
}

// NewClient creates a NER client pointing at the given service URL.
// The URL should be the base (e.g. "http://localhost:9000"); the client
// appends /ner for the extraction endpoint.
func NewClient(url string) *Client {
	return &Client{
		URL: url,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Extract sends text to the NER service and returns entity spans.
func (c *Client) Extract(text string) ([]Span, error) {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", c.URL+"/ner", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NER service unreachable at %s: %w", c.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("NER service returned %d: %s", resp.StatusCode, string(b))
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("NER response parse error: %w", err)
	}
	return result.Entities, nil
}

// Healthy returns true if the NER service is reachable.
func (c *Client) Healthy() bool {
	resp, err := c.HTTPClient.Get(c.URL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// LabelToBaseType maps common NER labels (spaCy/GLiNER conventions) to
// RedisKG base types. Returns empty string for unknown labels.
func LabelToBaseType(label string) string {
	switch label {
	case "PERSON", "PER":
		return "person"
	case "ORG", "ORGANIZATION":
		return "organization"
	case "GPE", "LOC", "LOCATION":
		return "location"
	case "PRODUCT":
		return "product"
	case "EVENT":
		return "event"
	case "FAC", "FACILITY":
		return "facility"
	case "NORP":
		return "organization" // nationalities/groups → organization
	case "WORK_OF_ART":
		return "concept"
	default:
		return ""
	}
}
