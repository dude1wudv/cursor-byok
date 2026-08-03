package modeladapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

func TestCodexDesktopVersionFromReader(t *testing.T) {
	input := strings.Repeat("x", 256*1024+20) + "0.146.0-alpha.10" + codexBackendURLMarker
	if got := codexDesktopVersionFromReader(strings.NewReader(input)); got != "0.146.0-alpha.10" {
		t.Fatalf("version = %q, want %q", got, "0.146.0-alpha.10")
	}
}

func TestOpenAIRequestsUseCodexDesktopUserAgent(t *testing.T) {
	for _, endpoint := range []string{"/v1/chat/completions", "/v1/responses"} {
		t.Run(endpoint, func(t *testing.T) {
			requestUserAgent := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requestUserAgent <- request.UserAgent()
				http.Error(writer, "stop after capture", http.StatusBadRequest)
			}))
			defer server.Close()

			adapter := &OpenAIAdapter{client: server.Client()}
			_ = adapter.Stream(context.Background(), StreamRequest{
				ModelID:        "gpt-5.6-sol",
				BaseURL:        server.URL,
				APIKey:         "test-key",
				OpenAIEndpoint: endpoint,
				Messages:       []Message{{Role: "user", Content: "hello"}},
			}, func(ModelEvent) error { return nil })

			if got, want := <-requestUserAgent, CodexDesktopUserAgent(); got != want {
				t.Fatalf("User-Agent = %q, want %q", got, want)
			}
		})
	}
}

func TestOpenAIResponsesParallelToolCallsGate(t *testing.T) {
	toolDescriptor := json.RawMessage(`{"function":{"name":"Read","description":"read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}`)
	tests := []struct {
		name    string
		modelID string
		tools   []json.RawMessage
		want    bool
	}{
		{name: "known gpt-5.6 with tools sends flag", modelID: "gpt-5.6-sol", tools: []json.RawMessage{toolDescriptor}, want: true},
		{name: "known gpt-5.6 without tools omits flag", modelID: "gpt-5.6-sol"},
		{name: "unknown compat model omits flag", modelID: "compat-llm-1", tools: []json.RawMessage{toolDescriptor}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				requestBody <- body
				http.Error(writer, "stop after capture", http.StatusBadRequest)
			}))
			defer server.Close()

			adapter := &OpenAIAdapter{client: server.Client()}
			_ = adapter.Stream(context.Background(), StreamRequest{
				ModelID:        tt.modelID,
				BaseURL:        server.URL,
				APIKey:         "test-key",
				OpenAIEndpoint: "/v1/responses",
				Messages:       []Message{{Role: "user", Content: "hello"}},
				Tools:          tt.tools,
			}, func(ModelEvent) error { return nil })

			body := <-requestBody
			value, present := body["parallel_tool_calls"]
			if tt.want && (!present || value != true) {
				t.Fatalf("parallel_tool_calls = %v (present=%t), want true", value, present)
			}
			if !tt.want && present {
				t.Fatalf("parallel_tool_calls unexpectedly present: %v", value)
			}
		})
	}
}

func TestOpenAIResponsesOmitsUnsupportedGPTMaxOutputTokens(t *testing.T) {
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		requestBody <- body
		http.Error(writer, "stop after capture", http.StatusBadRequest)
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	_ = adapter.Stream(context.Background(), StreamRequest{
		ModelID:        "gpt-5.6-sol",
		BaseURL:        server.URL,
		APIKey:         "test-key",
		OpenAIEndpoint: "/v1/responses",
		MaxTokens:      65536,
		Messages:       []Message{{Role: "user", Content: "hello"}},
	}, func(ModelEvent) error { return nil })

	body := <-requestBody
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("request contains unsupported max_output_tokens: %#v", body["max_output_tokens"])
	}
}

func TestOpenAIResponsesCacheFrontierTracksStablePrefix(t *testing.T) {
	body1 := map[string]any{"model": "gpt-5.6-sol", "prompt_cache_key": "cursor:root", "instructions": "stable", "input": []any{map[string]any{"role": "user", "content": "hello"}}, "tools": []any{map[string]any{"type": "function", "name": "Read"}}, "reasoning": map[string]any{"effort": "medium"}}
	knobs1 := annotateOpenAIResponsesCacheFrontier(map[string]any{}, body1, "/v1/responses")
	frontier1 := knobs1["cache_frontier"].(map[string]any)
	body2 := map[string]any{"model": "gpt-5.6-sol", "prompt_cache_key": "cursor:root", "instructions": "stable", "input": []any{map[string]any{"role": "user", "content": "hello"}, map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"}}, "tools": []any{map[string]any{"type": "function", "name": "Read"}}, "reasoning": map[string]any{"effort": "medium"}}
	knobs2 := annotateOpenAIResponsesCacheFrontier(map[string]any{"previous_cache_frontier": map[string]any{"segment_hashes": frontier1["segment_hashes"]}}, body2, "/v1/responses")
	frontier2 := knobs2["cache_frontier"].(map[string]any)
	if frontier2["first_changed_path"] != "input[1]" {
		t.Fatalf("first_changed_path=%v", frontier2["first_changed_path"])
	}
	if frontier2["expected_cache_read"] != true {
		t.Fatalf("expected_cache_read=%v", frontier2["expected_cache_read"])
	}
	if frontier2["prefix_match_bytes"].(int) <= 0 {
		t.Fatalf("prefix bytes=%v", frontier2["prefix_match_bytes"])
	}
}

func TestOpenAIResponsesCanonicalToolOrder(t *testing.T) {
	tools := []map[string]any{{"type": "function", "name": "Write"}, {"type": "function", "name": "Read"}}
	sort.SliceStable(tools, func(i, j int) bool {
		return openAIResponsesCanonicalToolName(tools[i]) < openAIResponsesCanonicalToolName(tools[j])
	})
	if got := openAIResponsesCanonicalToolName(tools[0]); got != "Read" {
		t.Fatalf("first tool=%q", got)
	}
}

func TestOpenAIPromptCacheKeyExplicitOverrideWins(t *testing.T) {
	body := map[string]any{"prompt_cache_key": "custom:key"}
	applyOpenAIPromptCacheKeyOverride(body, StreamRequest{ConversationID: "conversation"}, "gpt-5.6-sol")
	if body["prompt_cache_key"] != "custom:key" {
		t.Fatalf("override lost: %#v", body)
	}
}
