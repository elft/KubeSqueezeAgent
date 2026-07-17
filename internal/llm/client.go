package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kubesqueeze/kubesqueezeagent/internal/policy"
)

type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (client *Client) Enabled() bool { return client.baseURL != "" && client.model != "" }

func (client *Client) Model() string { return client.model }

func (client *Client) DraftPolicy(ctx context.Context, requirements string) (policy.Draft, error) {
	if !client.Enabled() {
		return policy.Draft{}, fmt.Errorf("no LLM provider is configured")
	}
	request, err := client.completionRequest(ctx, requirements, false)
	if err != nil {
		return policy.Draft{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return policy.Draft{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return policy.Draft{}, fmt.Errorf("LLM provider returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(response.Body).Decode(&completion); err != nil {
		return policy.Draft{}, err
	}
	if len(completion.Choices) == 0 {
		return policy.Draft{}, fmt.Errorf("LLM provider returned no choices")
	}
	return decodeDraft(completion.Choices[0].Message.Content)
}

// DraftPolicyStream streams provider text as it arrives, then applies the same
// strict decoding and safety validation as DraftPolicy before returning a draft.
func (client *Client) DraftPolicyStream(ctx context.Context, requirements string, onDelta func(string) error) (policy.Draft, error) {
	if !client.Enabled() {
		return policy.Draft{}, fmt.Errorf("no LLM provider is configured")
	}
	request, err := client.completionRequest(ctx, requirements, true)
	if err != nil {
		return policy.Draft{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return policy.Draft{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return policy.Draft{}, fmt.Errorf("LLM provider returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}

	var content strings.Builder
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return policy.Draft{}, fmt.Errorf("decode LLM stream: %w", err)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			content.WriteString(choice.Delta.Content)
			if onDelta != nil {
				if err := onDelta(choice.Delta.Content); err != nil {
					return policy.Draft{}, err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return policy.Draft{}, fmt.Errorf("read LLM stream: %w", err)
	}
	if content.Len() == 0 {
		return policy.Draft{}, fmt.Errorf("LLM provider returned no streamed content")
	}
	return decodeDraft(content.String())
}

func (client *Client) completionRequest(ctx context.Context, requirements string, stream bool) (*http.Request, error) {
	prompt := `Convert the customer requirements into one JSON object with exactly this shape:
{"timezone":"IANA timezone","environments":{"production":{"allowedActions":[],"minimumReplicas":1},"development":{"allowedActions":["scale-replicas","schedule-sleep"],"minimumReplicas":1,"sleepAfter":"19:00","wakeAt":"07:00"},"preview":{"allowedActions":["scale-to-zero"],"minimumReplicas":0}},"exclusions":[{"labels":{"customer-demo":"true"},"reason":"customer requirement"}],"minimumMetricCoverage":0.8,"requireHumanApproval":true,"neverModifyStatefulSets":true}
Safety constraints: production allowedActions must be empty; requireHumanApproval and neverModifyStatefulSets must be true; minimumMetricCoverage must be at least 0.8. Return JSON only. Customer requirements: ` + requirements
	body, err := json.Marshal(map[string]any{
		"model": client.model,
		"messages": []map[string]string{
			{"role": "system", "content": "You translate Kubernetes operating requirements into a typed policy draft. You never activate or execute policy."},
			{"role": "user", "content": prompt},
		},
		"temperature": 0,
		"stream":      stream,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if client.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+client.apiKey)
	}
	return request, nil
}

func decodeDraft(content string) (policy.Draft, error) {
	start, end := strings.Index(content, "{"), strings.LastIndex(content, "}")
	if start < 0 || end < start {
		return policy.Draft{}, fmt.Errorf("LLM response did not contain a JSON policy")
	}
	var draft policy.Draft
	decoder := json.NewDecoder(strings.NewReader(content[start : end+1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return policy.Draft{}, fmt.Errorf("decode typed policy: %w", err)
	}
	if err := draft.Validate(); err != nil {
		return policy.Draft{}, fmt.Errorf("unsafe policy draft rejected: %w", err)
	}
	return draft, nil
}
