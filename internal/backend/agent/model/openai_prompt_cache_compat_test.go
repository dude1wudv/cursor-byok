package modeladapter

import "testing"

func TestOpenAIResponsesUsesUpstreamCompatiblePromptCacheKeyOnly(t *testing.T) {
	body := map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "one"}}},
		},
	}
	req := StreamRequest{
		ConversationID:     "conversation-1",
		StableMessageCount: 1,
		Messages:           []Message{{Role: "user", Content: "one"}},
		RequestKnobs:       map[string]any{},
	}
	applyOpenAIPromptCacheKeyOverride(body, req, "gpt-5.6-sol")
	applyOpenAICompatiblePromptCaching(body, req)

	if got := body["prompt_cache_key"]; got != "cursor:conversation-1" {
		t.Fatalf("prompt_cache_key = %#v", got)
	}
	if _, ok := body["prompt_cache_options"]; ok {
		t.Fatalf("prompt_cache_options must not be emitted: %#v", body)
	}
	if containsOpenAICacheControl(body) {
		t.Fatalf("cache_control must not be emitted: %#v", body)
	}
}

func TestOpenAINonGPTModelsDoNotReceivePromptCacheKey(t *testing.T) {
	for _, modelID := range []string{"deepseek-v4-flash", "deepseek-chat", "minimax-m2.5"} {
		body := map[string]any{"prompt_cache_key": "stale", "input": []any{map[string]any{"role": "user", "content": "hello"}}}
		applyOpenAIPromptCacheKeyOverride(body, StreamRequest{ConversationID: "conversation-1"}, modelID)
		if _, ok := body["prompt_cache_key"]; ok {
			t.Fatalf("model %q received prompt_cache_key: %#v", modelID, body)
		}
	}
}

func containsOpenAICacheControl(value any) bool {
	switch current := value.(type) {
	case map[string]any:
		if _, ok := current["cache_control"]; ok {
			return true
		}
		for _, nested := range current {
			if containsOpenAICacheControl(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if containsOpenAICacheControl(nested) {
				return true
			}
		}
	case []map[string]any:
		for _, nested := range current {
			if containsOpenAICacheControl(nested) {
				return true
			}
		}
	}
	return false
}
