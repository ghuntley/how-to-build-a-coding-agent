package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// Include this file for tool entrypoints, for example:
// go test read.go provider_test.go provider_tools_test.go.
func TestToolLoopPreservesThinking(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	calls, toolCalls := 0, 0
	client := mockClient(t, func(r *http.Request) string {
		calls++
		body := requestBody(t, r)
		tools := body["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["name"] != "read_file" {
			t.Fatalf("unexpected tools: %v", tools)
		}
		if calls == 1 {
			return `{"id":"msg_tool","type":"message","role":"assistant","model":"MiniMax-M3.1-Flash","content":[{"type":"thinking","thinking":"inspect the file","signature":"test-signature"},{"type":"tool_use","id":"tool_test","name":"read_file","input":{"path":"riddle.txt"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
		}
		messages := body["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("message count = %d, want 3", len(messages))
		}
		assistant := messages[1].(map[string]any)["content"].([]any)
		thinking := assistant[0].(map[string]any)
		if thinking["thinking"] != "inspect the file" || thinking["signature"] != "test-signature" {
			t.Errorf("thinking was not replayed: %v", thinking)
		}
		toolUse := assistant[1].(map[string]any)
		if toolUse["id"] != "tool_test" || toolUse["name"] != "read_file" {
			t.Errorf("tool call was not replayed: %v", toolUse)
		}
		result := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
		if result["tool_use_id"] != "tool_test" || result["type"] != "tool_result" {
			t.Errorf("unexpected tool result: %v", result)
		}
		return finalResponse
	})
	inputs := 0
	agent := NewAgent(&client, func() (string, bool) {
		inputs++
		return "inspect the file", inputs == 1
	}, []ToolDefinition{{
		Name: "read_file", Description: "Read a file", InputSchema: anthropic.ToolInputSchemaParam{},
		Function: func(input json.RawMessage) (string, error) {
			toolCalls++
			var args map[string]string
			if err := json.Unmarshal(input, &args); err != nil || args["path"] != "riddle.txt" {
				t.Fatalf("unexpected tool input: %s", input)
			}
			return "file contents", nil
		},
	}}, false)
	if err := agent.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || toolCalls != 1 {
		t.Errorf("requests = %d, tool executions = %d; want 2 and 1", calls, toolCalls)
	}
}
