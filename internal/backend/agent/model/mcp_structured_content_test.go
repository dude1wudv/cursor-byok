package modeladapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPStructuredContentSurvivesOpenAIChatNormalization(t *testing.T) {
	messages, err := normalizeOpenAIProviderMessages(mcpStructuredContentMessages(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("normalized message count = %d, want 2", len(messages))
	}
	content, ok := messages[1]["content"].(string)
	if !ok || !strings.Contains(content, `"structuredContent"`) {
		t.Fatalf("chat tool content lost structured content: %#v", messages[1]["content"])
	}
	assertStructuredContentJSON(t, content)
}

func TestMCPStructuredContentSurvivesOpenAIResponsesNormalization(t *testing.T) {
	_, input, err := normalizeOpenAIResponsesInput(mcpStructuredContentMessages())
	if err != nil {
		t.Fatal(err)
	}
	var output string
	for _, item := range input {
		if item["type"] == "function_call_output" {
			output, _ = item["output"].(string)
			break
		}
	}
	if output == "" {
		t.Fatalf("responses input has no function_call_output: %#v", input)
	}
	assertStructuredContentJSON(t, output)
}

func TestMCPStructuredContentSurvivesAnthropicNormalization(t *testing.T) {
	_, messages, err := normalizeAnthropicProviderMessages(mcpStructuredContentMessages(), false)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	for _, message := range messages {
		for _, block := range message.Content {
			if block["type"] == "tool_result" {
				content, _ = block["content"].(string)
			}
		}
	}
	if content == "" {
		t.Fatalf("anthropic messages have no tool_result: %#v", messages)
	}
	assertStructuredContentJSON(t, content)
}

func mcpStructuredContentMessages() []Message {
	return []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCallDescriptor{{
				ID:   "call-1",
				Type: "function",
				Function: ToolCallFunctionShape{
					Name:      "CallMcpTool",
					Arguments: `{"server":"demo","toolName":"lookup","arguments":{}}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call-1",
			Name:       "CallMcpTool",
			Content:    `{"content":[{"text":{"text":"ok"}}],"isError":false,"structuredContent":{"answer":"ok","items":[1,2]}}`,
		},
	}
}

func assertStructuredContentJSON(t *testing.T, raw string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("tool result is not JSON: %v (%s)", err, raw)
	}
	structured, ok := payload["structuredContent"].(map[string]any)
	if !ok || structured["answer"] != "ok" {
		t.Fatalf("structured content = %#v", payload["structuredContent"])
	}
}
