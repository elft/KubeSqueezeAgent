package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDraftPolicyValidatesProviderOutput(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantError bool
	}{
		{
			name:    "safe typed draft",
			content: `{"timezone":"America/Chicago","environments":{"production":{"allowedActions":[],"minimumReplicas":3},"development":{"allowedActions":["scale-replicas"],"minimumReplicas":1}},"exclusions":[{"labels":{"customer-demo":"true"},"reason":"protected"}],"minimumMetricCoverage":0.8,"requireHumanApproval":true,"neverModifyStatefulSets":true}`,
		},
		{
			name:      "production mutation is rejected",
			content:   `{"timezone":"America/Chicago","environments":{"production":{"allowedActions":["scale-replicas"],"minimumReplicas":1}},"exclusions":[],"minimumMetricCoverage":0.8,"requireHumanApproval":true,"neverModifyStatefulSets":true}`,
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			responseBody, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": test.content}}}})
			client := New("http://provider.test/v1", "test-key", "test-model")
			client.http = &http.Client{Transport: llmRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "Bearer test-key" {
					t.Fatal("provider request did not include configured bearer token")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(responseBody))), Header: make(http.Header)}, nil
			})}
			_, err := client.DraftPolicy(context.Background(), "keep production safe and require approval")
			if (err != nil) != test.wantError {
				t.Fatalf("DraftPolicy() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}

func TestDraftPolicyStreamForwardsDeltasAndValidatesFinalDraft(t *testing.T) {
	parts := []string{
		`{"timezone":"America/Chicago","environments":`,
		`{"production":{"allowedActions":[],"minimumReplicas":3},`,
		`"development":{"allowedActions":["scale-replicas"],"minimumReplicas":1}},`,
		`"exclusions":[],"minimumMetricCoverage":0.8,"requireHumanApproval":true,"neverModifyStatefulSets":true}`,
	}
	var stream strings.Builder
	for _, part := range parts {
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": part}}}})
		fmt.Fprintf(&stream, "data: %s\n\n", chunk)
	}
	stream.WriteString("data: [DONE]\n\n")

	client := New("http://provider.test/v1", "", "test-model")
	client.http = &http.Client{Transport: llmRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v, want true", body["stream"])
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream.String())), Header: http.Header{"Content-Type": []string{"text/event-stream"}}}, nil
	})}

	var received strings.Builder
	draft, err := client.DraftPolicyStream(context.Background(), "keep production safe and require approval", func(delta string) error {
		received.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if received.String() != strings.Join(parts, "") {
		t.Fatalf("received stream %q", received.String())
	}
	if draft.Timezone != "America/Chicago" || !draft.RequireHumanApproval {
		t.Fatalf("unexpected draft: %#v", draft)
	}
}

type llmRoundTripFunc func(*http.Request) (*http.Response, error)

func (function llmRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
