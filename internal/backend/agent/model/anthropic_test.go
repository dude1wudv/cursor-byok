package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAnthropicParallelToolUseEmitsOneEventPerBlock 验证同一条 assistant 消息中的
// 多个 tool_use block 各自产生独立的 ToolLikeCompleted 事件（Claude 并行工具调用的适配层前提）。
func TestAnthropicParallelToolUseEmitsOneEventPerBlock(t *testing.T) {
	sse := "" +
		"event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"Read","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.go\"}"}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_02","name":"Grep","input":{}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"x\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(sse))
	}))
	defer server.Close()

	adapter := &AnthropicAdapter{client: server.Client()}
	var toolEvents []ModelEvent
	err := adapter.Stream(context.Background(), StreamRequest{
		ModelID:  "claude-sonnet-5",
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(event ModelEvent) error {
		if event.Kind == ModelEventKindToolLikeCompleted {
			toolEvents = append(toolEvents, event)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(toolEvents) != 2 {
		t.Fatalf("tool events = %d, want 2 (one per tool_use block)", len(toolEvents))
	}
	first, second := toolEvents[0].ToolInvocation, toolEvents[1].ToolInvocation
	if first == nil || second == nil {
		t.Fatal("tool events missing invocations")
	}
	if first.CallID == second.CallID {
		t.Fatalf("parallel tool_use blocks share call ID %q", first.CallID)
	}
	if first.ToolName != "Read" || second.ToolName != "Grep" {
		t.Fatalf("tool names = %q,%q, want Read,Grep", first.ToolName, second.ToolName)
	}
	// 乱序 stop（index 0 先于 index 1 完成）也必须保留各自独立的参数。
	var firstArgs map[string]any
	if err := json.Unmarshal(first.ArgsJSON, &firstArgs); err != nil || firstArgs["path"] != "a.go" {
		t.Fatalf("first tool args = %s (err=%v)", first.ArgsJSON, err)
	}
}

func TestAnthropicMessageCacheBreakpointsPreserveAppendOnlyHistory(t *testing.T) {
	for size := 1; size <= 32; size++ {
		t.Run(fmt.Sprintf("size_%02d", size), func(t *testing.T) {
			previous := anthropicMessagesForAppendOnlyTest(size)
			current := anthropicMessagesForAppendOnlyTest(size + 1)

			applyAnthropicMessageCacheBreakpoints(previous)
			applyAnthropicMessageCacheBreakpoints(current)

			want := mustMarshalAnthropicMessagesForTest(t, previous)
			got := mustMarshalAnthropicMessagesForTest(t, current[:len(previous)])
			if got != want {
				t.Fatalf("historical message prefix changed after append\nwant: %s\ngot:  %s", want, got)
			}
		})
	}
}

func anthropicMessagesForAppendOnlyTest(count int) []anthropicMessage {
	messages := make([]anthropicMessage, 0, count)
	for index := 0; index < count; index++ {
		messages = append(messages, anthropicMessage{
			Role: "user",
			Content: []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("message-%02d", index),
			}},
		})
	}
	return messages
}

func mustMarshalAnthropicMessagesForTest(t *testing.T, messages []anthropicMessage) string {
	t.Helper()
	payload, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal anthropic messages: %v", err)
	}
	return string(payload)
}
