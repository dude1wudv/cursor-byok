package modeladapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
