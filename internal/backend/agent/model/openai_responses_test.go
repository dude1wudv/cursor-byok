package modeladapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
