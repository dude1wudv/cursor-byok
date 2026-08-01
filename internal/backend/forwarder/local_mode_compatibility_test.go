package forwarder

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestModeAliasesAndPromptModes(t *testing.T) {
	tests := []struct {
		name      string
		mode      agentv1.AgentMode
		canonical agentv1.AgentMode
		alias     string
	}{
		{name: "triage", mode: agentv1.AgentMode_AGENT_MODE_TRIAGE, canonical: agentv1.AgentMode_AGENT_MODE_DEBUG, alias: "debug"},
		{name: "project", mode: agentv1.AgentMode_AGENT_MODE_PROJECT, canonical: agentv1.AgentMode_AGENT_MODE_PLAN, alias: "plan"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeMode(tt.mode); got != tt.canonical {
				t.Fatalf("normalizeMode(%s) = %s, want %s", tt.mode, got, tt.canonical)
			}
			if got, err := validateSupportedActiveMode(tt.mode); err != nil || got != tt.canonical {
				t.Fatalf("validateSupportedActiveMode(%s) = %s, %v; want %s", tt.mode, got, err, tt.canonical)
			}
			if got, err := modeAlias(tt.mode); err != nil || got != tt.alias {
				t.Fatalf("modeAlias(%s) = %q, %v; want %q", tt.mode, got, err, tt.alias)
			}
		})
	}

	for _, item := range []struct {
		alias string
		mode  agentv1.AgentMode
	}{
		{alias: "triage", mode: agentv1.AgentMode_AGENT_MODE_TRIAGE},
		{alias: "project", mode: agentv1.AgentMode_AGENT_MODE_PROJECT},
	} {
		got, err := parseModeAlias(item.alias)
		if err != nil || got != item.mode {
			t.Fatalf("parseModeAlias(%q) = %s, %v; want %s", item.alias, got, err, item.mode)
		}
		got, err = parseTargetModeID(item.alias)
		if err != nil || got != item.mode {
			t.Fatalf("parseTargetModeID(%q) = %s, %v; want %s", item.alias, got, err, item.mode)
		}
	}
}

func TestFilterToolsForPromptCompileOptions(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"function":{"name":"GenerateImage"}}`),
		json.RawMessage(`{"function":{"name":"SubagentAwait"}}`),
		json.RawMessage(`{"function":{"name":"Await"}}`),
		json.RawMessage(`{"function":{"name":"Read"}}`),
	}
	filtered, names, err := filterToolsForPromptCompileOptions(items, PromptCompileOptions{
		ClientSupportsInlineImagesSet:      true,
		ClientSupportsInlineImages:         false,
		SuppressSubagentProgressUpdateTool: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names, ","), "Await,Read"; got != want {
		t.Fatalf("filtered names = %q, want %q", got, want)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered descriptor count = %d, want 2", len(filtered))
	}
}

func TestInlineImageCapabilityDegradesReplayMessages(t *testing.T) {
	filtered := filterInlineImageContentParts([]modeladapter.Message{
		{
			Role:    "user",
			Content: "describe this",
			ContentParts: []modeladapter.ContentPart{
				{Type: "text", Text: "describe this"},
				{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Data: []byte("png")}},
			},
		},
		{
			Role: "user",
			ContentParts: []modeladapter.ContentPart{
				{Type: "image", Image: &modeladapter.ImageContent{MIMEType: "image/png", Data: []byte("png")}},
			},
		},
	})
	if len(filtered) != 1 || len(filtered[0].ContentParts) != 1 || filtered[0].ContentParts[0].Type != "text" {
		t.Fatalf("filtered image messages = %#v", filtered)
	}
}

func TestPromptCompileOptionsKeepLatestContextOutOfStablePrefix(t *testing.T) {
	compiler := NewPromptCompiler(NewHistoryProjector(), NewToolCatalog(), NewReminderInjector(), NewUserRuleStore(t.TempDir()))
	entry, ok, err := newModelMessageEntry(1, "request-1", modeladapter.Message{Role: "user", Content: "prior request"})
	if err != nil || !ok {
		t.Fatalf("newModelMessageEntry() = ok=%t err=%v", ok, err)
	}
	conversation := &ConversationFile{
		ConversationID: "conversation",
		Mode:           "plan",
		CurrentTurnSeq: 2,
		NextTurnSeq:    3,
		Entries:        []HistoryEntry{entry},
	}
	base, err := compiler.CompileWithOptions(conversation, agentv1.AgentMode_AGENT_MODE_PLAN, "", "test-model", PromptCompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	withLatest, err := compiler.CompileWithOptions(conversation, agentv1.AgentMode_AGENT_MODE_PLAN, "", "test-model", PromptCompileOptions{
		ExcludeWorkspaceContext: true,
		LatestRequestContext: &agentv1.RequestContext{
			UserIntentSummary: stringPtr("latest intent"),
			FileContents:      map[string]string{"main.go": "workspace must not be sent"},
		},
		TransientPromptContexts: []PromptContextMessage{
			newPromptContextMessage("transient-recovery", modeladapter.Message{Role: "user", Content: "recover this pass"}, false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if withLatest.StableMessageCount != base.StableMessageCount {
		t.Fatalf("stable message count = %d, want %d", withLatest.StableMessageCount, base.StableMessageCount)
	}
	if len(withLatest.Messages) <= len(base.Messages) {
		t.Fatalf("latest-only request did not add a suffix: base=%d latest=%d", len(base.Messages), len(withLatest.Messages))
	}
	if !reflect.DeepEqual(withLatest.Messages[:len(base.Messages)], base.Messages) {
		t.Fatal("latest-only context changed the stable message prefix")
	}
	joined := compiledMessageText(withLatest.Messages)
	if !strings.Contains(joined, "latest intent") || !strings.Contains(joined, "recover this pass") {
		t.Fatalf("latest-only suffix missing: %q", joined)
	}
	if strings.Contains(joined, "workspace must not be sent") {
		t.Fatal("workspace file contents survived exclude_workspace_context")
	}

	contexts, err := compiler.DerivePromptContexts(conversation, agentv1.AgentMode_AGENT_MODE_PLAN, "please implement the plan")
	if err != nil {
		t.Fatal(err)
	}
	var durablePlan, transientRequest bool
	for _, context := range contexts {
		switch context.Source {
		case promptContextSourcePlanTurnContract:
			durablePlan = context.Persist
		case promptContextSourceCurrentUserRequest:
			transientRequest = !context.Persist
		}
	}
	if !durablePlan || !transientRequest {
		t.Fatalf("unexpected derived context persistence: durable_plan=%t transient_request=%t", durablePlan, transientRequest)
	}
}

func TestRunRequestMetadataPreservesModelCapabilities(t *testing.T) {
	metadata := buildRunRequestMetadata(InboundIntent{
		ModelID:         "resolved-model",
		ModelName:       "Resolved Model",
		DevRawModelSlug: "raw-model-slug",
		RequestedModelPayload: map[string]any{
			"model_id": "requested-model",
			"max_mode": true,
			"parameters": []map[string]string{
				{"id": "reasoning_effort", "value": "high"},
				{"id": "custom_parameter", "value": "kept"},
			},
		},
		ExcludeWorkspaceContext:            true,
		ClientSupportsInlineImagesSet:      true,
		ClientSupportsInlineImages:         false,
		SuppressSubagentProgressUpdateTool: true,
	})
	if metadata["dev_raw_model_slug"] != "raw-model-slug" {
		t.Fatalf("dev_raw_model_slug = %v", metadata["dev_raw_model_slug"])
	}
	if metadata["max_mode"] != true {
		t.Fatalf("max_mode = %v", metadata["max_mode"])
	}
	if metadata["exclude_workspace_context"] != true || metadata["client_supports_inline_images"] != false || metadata["suppress_subagent_progress_update_tool"] != true {
		t.Fatalf("capability metadata = %#v", metadata)
	}
	parameters, ok := metadata["requested_model_parameters"].([]any)
	var second map[string]any
	var secondOK bool
	if ok && len(parameters) > 1 {
		second, secondOK = parameters[1].(map[string]any)
	}
	if !ok || len(parameters) != 2 || !secondOK || second["value"] != "kept" {
		t.Fatalf("requested_model_parameters = %#v", metadata["requested_model_parameters"])
	}
}

func compiledMessageText(messages []modeladapter.Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}
