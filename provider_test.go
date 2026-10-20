package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Each workshop entrypoint is standalone: go test chat.go provider_test.go.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mockClient(t *testing.T, handle func(*http.Request) string) anthropic.Client {
	t.Helper()
	t.Setenv("ANTHROPIC_API_KEY", "test-only")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	client := newAnthropicClient()
	client.Messages.Options = append(client.Messages.Options, option.WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(handle(r))),
				Request:    r,
			}, nil
		}),
	}))
	return client
}

const finalResponse = `{"id":"msg_test","type":"message","role":"assistant","model":"MiniMax-M3.1-Flash","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func requestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	if r.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", r.Method)
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"thinking", "output_config", "reasoning_effort", "service_tier"} {
		if _, present := body[field]; present {
			t.Errorf("unexpected %s override: %v", field, body[field])
		}
	}
	return body
}

func TestProviderRequest(t *testing.T) {
	for _, tc := range []struct{ name, baseURL, model, wantURL, wantModel string }{
		{"default", "", "", "https://api.minimax.io/anthropic/v1/messages", "MiniMax-M3.1-Flash"},
		{"international", minimaxAnthropicBaseURL, "MiniMax-M3.1-Flash", "https://api.minimax.io/anthropic/v1/messages", "MiniMax-M3.1-Flash"},
		{"china", minimaxChinaAnthropicBaseURL, "", "https://api.minimaxi.com/anthropic/v1/messages", "MiniMax-M3.1-Flash"},
		{"m3", minimaxChinaAnthropicBaseURL, "MiniMax-M3", "https://api.minimaxi.com/anthropic/v1/messages", "MiniMax-M3"},
		{"saved-legacy", "", "MiniMax-M2.7", "https://api.minimax.io/anthropic/v1/messages", "MiniMax-M2.7"},
		{"custom", "https://gateway.example/compatible/", "custom-model", "https://gateway.example/compatible/v1/messages", "custom-model"},
		{"original-provider", "https://api.anthropic.com", "claude-opus-4-6", "https://api.anthropic.com/v1/messages", "claude-opus-4-6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_BASE_URL", tc.baseURL)
			t.Setenv("ANTHROPIC_MODEL", tc.model)
			calls := 0
			client := mockClient(t, func(r *http.Request) string {
				calls++
				if r.URL.String() != tc.wantURL {
					t.Errorf("URL = %s, want %s", r.URL, tc.wantURL)
				}
				body := requestBody(t, r)
				if body["model"] != tc.wantModel || body["max_tokens"] != float64(1024) {
					t.Errorf("unexpected model or token limit: %v", body)
				}
				return finalResponse
			})
			agent := Agent{client: &client}
			if _, err := agent.runInference(context.Background(), []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("hello")),
			}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("request count = %d, want 1", calls)
			}
		})
	}
}

func TestInferencePreservesContent(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	const input = `[{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1hZ2U="}}]},{"role":"assistant","content":[{"type":"thinking","thinking":"inspect the file","signature":"test-signature"},{"type":"tool_use","id":"tool_test","name":"read_file","input":{"path":"riddle.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_test","content":"file contents"}]}]`
	var messages []anthropic.MessageParam
	if err := json.Unmarshal([]byte(input), &messages); err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := mockClient(t, func(r *http.Request) string {
		calls++
		body := requestBody(t, r)
		if !reflect.DeepEqual(body["messages"], want) {
			t.Errorf("conversation changed: got %v, want %v", body["messages"], want)
		}
		return finalResponse
	})
	agent := Agent{client: &client}
	if _, err := agent.runInference(context.Background(), messages); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("request count = %d, want 1", calls)
	}
}
