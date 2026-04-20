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
	claudeModel  = "claude-opus-4-5"  // using latest stable model
)

type ClaudeClient struct {
	apiKey     string
	httpClient *http.Client
}

// ClaudeResponse — adapted for album store: NodeCount instead of CartAPITaskCount.
type ClaudeResponse struct {
	Action     string  `json:"action"`     // "scale_up" | "scale_down" | "no_action"
	Reasoning  string  `json:"reasoning"`
	Changes    Changes `json:"changes"`
	Confidence float64 `json:"confidence"` // 0.0–1.0; applied only if >= 0.7
}

type Changes struct {
	NodeCount int `json:"node_count"` // desired number of EC2 nodes [1, 4]
}

type anthropicRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system"`
	Messages  []anthropicMsg `json:"messages"`
}

type anthropicMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicAPIResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func NewClaudeClient(apiKey string) *ClaudeClient {
	return &ClaudeClient{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *ClaudeClient) GetScalingDecision(ctx context.Context, current AggregatedMetric, history []AggregatedMetric, currentNodes int) (*ClaudeResponse, error) {
	systemPrompt := `You are an infrastructure scaling advisor for a distributed album store API
running on AWS EC2 nodes (c5n.large, 25 Gbps each) behind an Application Load Balancer.
The system serves photo uploads and album metadata requests.
Analyze the metrics and decide if scaling is needed.
Respond ONLY with valid JSON — no markdown, no explanation outside the JSON.`

	userPrompt := buildPrompt(current, history, currentNodes)

	body, _ := json.Marshal(anthropicRequest{
		Model:     claudeModel,
		MaxTokens: 512,
		System:    systemPrompt,
		Messages:  []anthropicMsg{{Role: "user", Content: userPrompt}},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeAPIURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp anthropicAPIResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	var text string
	for _, block := range apiResp.Content {
		if block.Type == "text" {
			text = block.Text
			break
		}
	}
	if text == "" {
		return nil, fmt.Errorf("empty Claude response")
	}

	log.Printf("Claude raw: %s", text)

	var decision ClaudeResponse
	if err := json.Unmarshal([]byte(text), &decision); err != nil {
		return nil, fmt.Errorf("parse decision JSON: %w (raw: %s)", err, text)
	}

	if err := validateDecision(&decision, currentNodes); err != nil {
		return nil, fmt.Errorf("invalid decision: %w", err)
	}
	return &decision, nil
}

func buildPrompt(current AggregatedMetric, history []AggregatedMetric, currentNodes int) string {
	histJSON, _ := json.MarshalIndent(history, "", "  ")
	curJSON, _ := json.MarshalIndent(current, "", "  ")
	return fmt.Sprintf(`Current EC2 node count: %d (max 4, min 1)

Current 60-second metrics window:
%s

Last %d metric windows (history, oldest first):
%s

Album store scaling rules:
- Scale UP (+1 node) if:
    p99 latency > 2000ms AND trend is "increasing"
    OR error rate > 3%%
    OR request rate > 200/s with increasing trend
- Scale DOWN (-1 node) if:
    avg latency < 50ms AND request rate < 10/s for 3+ consecutive windows
    AND no recent errors
- cache_hit_rate < 50%% suggests Redis issues, NOT a scaling signal
- Never scale below 1 or above 4 nodes (lab constraint)
- Maximum change per decision: ±1 node
- Prefer conservative changes; use no_action when uncertain

Respond with exactly this JSON (no markdown):
{
  "action": "scale_up|scale_down|no_action",
  "reasoning": "one-sentence explanation",
  "changes": {"node_count": <desired_total_nodes>},
  "confidence": <0.0 to 1.0>
}`, currentNodes, string(curJSON), len(history), string(histJSON))
}

func validateDecision(d *ClaudeResponse, currentNodes int) error {
	valid := map[string]bool{"scale_up": true, "scale_down": true, "no_action": true}
	if !valid[d.Action] {
		return fmt.Errorf("invalid action: %s", d.Action)
	}
	if d.Confidence < 0 || d.Confidence > 1 {
		return fmt.Errorf("confidence out of range: %.2f", d.Confidence)
	}
	if d.Action != "no_action" && (d.Changes.NodeCount < 1 || d.Changes.NodeCount > 4) {
		return fmt.Errorf("node_count out of bounds [1,4]: %d", d.Changes.NodeCount)
	}
	return nil
}
