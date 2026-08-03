package config

import "testing"

func TestNormalizeModelAdapterSubagentRoles(t *testing.T) {
	items, err := NormalizeModelAdapterConfigs([]ModelAdapterConfig{{
		DisplayName:     "worker",
		Type:            "openai",
		BaseURL:         "https://api.example.com",
		APIKey:          "test-key",
		TooltipData:     "worker model",
		SubagentEnabled: true,
		SubagentRoles:   []string{"medium_explore", "unknown", "medium_explore", "complex_debug"},
		ModelID:         "model",
		ReasoningEffort: "medium",
		OpenAIEndpoint:  "/v1/responses",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].SubagentRoles) != 2 {
		t.Fatalf("unexpected roles: %#v", items)
	}
	if items[0].SubagentRoles[0] != "medium_explore" || items[0].SubagentRoles[1] != "complex_debug" {
		t.Fatalf("role order was not preserved: %#v", items[0].SubagentRoles)
	}
}