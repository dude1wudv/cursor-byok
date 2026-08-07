package modelchannel

import "testing"

func TestNormalizeOpenAIEndpointPath(t *testing.T) {
	valid, err := NormalizeOpenAIEndpointPath("/openai/deployments/demo/chat/completions")
	if err != nil || valid != "/openai/deployments/demo/chat/completions" {
		t.Fatalf("unexpected valid path: %q, %v", valid, err)
	}
	for _, input := range []string{"https://api.example.com/responses", "/v1/models", "/v1/../responses", "/v1/responses?debug=1"} {
		if _, err := NormalizeOpenAIEndpointPath(input); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}

func TestBuildChannelIDWithPathChangesIdentity(t *testing.T) {
	base := BuildChannelID("https://api.example.com", "model", "key", "name", OpenAIEndpointCustom)
	custom := BuildChannelIDWithPath("https://api.example.com", "model", "key", "name", OpenAIEndpointCustom, "/deployment/chat/completions")
	if base == custom {
		t.Fatal("custom endpoint path must participate in channel identity")
	}
}
