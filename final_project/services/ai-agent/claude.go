package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	claudeAPIURL = "https://api.anthropic.com/v1/messages"
	claudeModel  = "claude-sonnet-4-20250514"
)

// ClaudeClient communicates with the Anthropic Messages API via raw HTTP.
type ClaudeClient struct {
	apiKey     string
	httpClient *http.Client
}

// ClaudeResponse is the parsed scaling decision from Claude.
type ClaudeResponse struct {
	Action     string  `json:"action"`
	Reasoning  string  `json:"reasoning"`
	Changes    Changes `json:"changes"`
	Confidence float64 `json:"confidence"`
}

// Changes holds the desired infrastructure changes.
type Changes struct {
	CartAPITaskCount int `json:"cart_api_task_count"`
}

// anthropicRequest is the request body for the Messages API.
type anthropicRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    string            `json:"system"`
	Messages  []anthropicMsg    `json:"messages"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicAPIResponse is the raw API response structure.
type anthropicAPIResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// NewClaudeClient creates a new Claude API client.
func NewClaudeClient(apiKey string) *ClaudeClient {
	return &ClaudeClient{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetScalingDecision sends current metrics and history to Claude and returns
// a validated scaling decision.
func (c *ClaudeClient) GetScalingDecision(ctx context.Context, current AggregatedMetric, history []AggregatedMetric, currentTaskCount int) (*ClaudeResponse, error) {
	systemPrompt := `You are an infrastructure scaling advisor for an ECS-based shopping cart API.
Analyze the metrics and decide if scaling is needed.
Respond ONLY with valid JSON, no markdown, no explanation outside the JSON.`

	userPrompt := buildUserPrompt(current, history, currentTaskCount)

	reqBody := anthropicRequest{
		Model:     claudeModel,
		MaxTokens: 512,
		System:    systemPrompt,
		Messages: []anthropicMsg{
			{Role: "user", Content: userPrompt},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal API response: %w", err)
	}

	if len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("empty response from Claude")
	}

	// Extract text content
	var textContent string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			textContent = block.Text
			break
		}
	}
	if textContent == "" {
		return nil, fmt.Errorf("no text content in Claude response")
	}

	log.Printf("Claude raw response: %s", textContent)

	var decision ClaudeResponse
	if err := json.Unmarshal([]byte(textContent), &decision); err != nil {
		return nil, fmt.Errorf("parse Claude decision JSON: %w (raw: %s)", err, textContent)
	}

	// Validate the decision
	if err := validateDecision(&decision, currentTaskCount); err != nil {
		return nil, fmt.Errorf("invalid decision: %w", err)
	}

	return &decision, nil
}

// buildUserPrompt constructs the user message with current metrics, history,
// and scaling rules.
func buildUserPrompt(current AggregatedMetric, history []AggregatedMetric, currentTaskCount int) string {
	historyJSON, _ := json.MarshalIndent(history, "", "  ")
	currentJSON, _ := json.MarshalIndent(current, "", "  ")

	return fmt.Sprintf(`Current ECS task count: %d

Current metrics window:
%s

Last %d metric windows (history):
%s

Scaling rules:
- Scale UP if avg CPU > 70%% with increasing trend
- Scale DOWN if avg CPU < 30%% for 3+ consecutive windows
- Never scale below 1 or above 8 tasks
- Never decrease Kafka partitions
- Prefer conservative changes (+-1-2 tasks per cycle)
- Maximum change per decision: +-2 tasks

Respond with exactly this JSON structure:
{
  "action": "scale_up|scale_down|no_action",
  "reasoning": "brief explanation",
  "changes": {"cart_api_task_count": <new_count>},
  "confidence": <0.0 to 1.0>
}`, currentTaskCount, string(currentJSON), len(history), string(historyJSON))
}

// validateDecision checks that the decision meets safety constraints.
func validateDecision(d *ClaudeResponse, currentTaskCount int) error {
	validActions := map[string]bool{"scale_up": true, "scale_down": true, "no_action": true}
	if !validActions[d.Action] {
		return fmt.Errorf("invalid action: %s", d.Action)
	}

	if d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("confidence out of range: %.2f", d.Confidence)
	}

	if d.Action != "no_action" {
		if d.Changes.CartAPITaskCount < 1 || d.Changes.CartAPITaskCount > 8 {
			return fmt.Errorf("task count out of bounds [1,8]: %d", d.Changes.CartAPITaskCount)
		}
	}

	return nil
}
