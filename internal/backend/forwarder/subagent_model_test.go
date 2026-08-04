package forwarder

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	legacyruntime "cursor/internal/runtime"
)

func TestRequestedModelThinkingEffortPreservesParametersAndMax(t *testing.T) {
	model := &agentv1.RequestedModel{
		ModelId:    "model-x",
		MaxMode:    true,
		Parameters: []*agentv1.RequestedModel_ModelParameterValue{{Id: "thinking_effort", Value: "max"}},
	}
	if got := extractRuntimeThinkingEffortFromRequestedModel(model); got != "max" {
		t.Fatalf("effort=%q, want max", got)
	}
	parsed := parseSubagentModelOverrides([]*agentv1.SubagentModelOverride{{
		SubagentType: "explore",
		Selection:    &agentv1.SubagentModelOverride_Model{Model: model},
	}})
	selection := parsed.Overrides["explore"]
	if selection.ThinkingEffort != "max" || selection.Parameters["thinking_effort"] != "max" || !selection.MaxMode {
		t.Fatalf("selection lost model parameters: %+v", selection)
	}
}

func TestVariantThinkingEffortNormalizes(t *testing.T) {
	model, effort := splitRuntimeThinkingEffortVariantString("model-x:maximum")
	if model != "model-x" || effort != "max" {
		t.Fatalf("got model=%q effort=%q", model, effort)
	}
}

func TestSubagentDepthFromExecutionPrompt(t *testing.T) {
	if got := subagentDepthFromPrompt("<subagent_execution_contract>\nsubagent_depth=2\n</subagent_execution_contract>"); got != 2 {
		t.Fatalf("depth=%d, want 2", got)
	}
}

type subagentModelTestResolver struct {
	channel *legacyruntime.ResolvedChannel
	models  []modeladapter.SubagentModel
}

func (resolver subagentModelTestResolver) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	if resolver.channel != nil && modelID == resolver.channel.ID {
		copy := *resolver.channel
		return &copy, nil
	}
	return nil, nil
}

func (resolver subagentModelTestResolver) EnabledSubagentModels(context.Context) []modeladapter.SubagentModel {
	return resolver.models
}

func (subagentModelTestResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return time.Minute
}

func TestResolveEnabledSubagentModelIDAcceptsPublicNames(t *testing.T) {
	models := []modeladapter.SubagentModel{{
		ID: "e7bbe0a5c209c3e2", DisplayName: "gpt-5.6-luna", ModelID: "gpt-5.6-luna",
	}}
	for _, requested := range []string{"e7bbe0a5c209c3e2", "gpt-5.6-luna", "luna", "LUNA"} {
		if got, err := resolveEnabledSubagentModelID(models, requested); err != nil || got != "e7bbe0a5c209c3e2" {
			t.Fatalf("requested=%q got=%q err=%v", requested, got, err)
		}
	}
}

func TestResolveEnabledSubagentModelIDRejectsAmbiguousName(t *testing.T) {
	models := []modeladapter.SubagentModel{
		{ID: "channel-a", DisplayName: "gpt-5.6-luna", ModelID: "model-a"},
		{ID: "channel-b", DisplayName: "worker", ModelID: "gpt-5.6-luna"},
	}
	for _, requested := range []string{"gpt-5.6-luna", "luna"} {
		if _, err := resolveEnabledSubagentModelID(models, requested); err == nil {
			t.Fatalf("ambiguous model %q should be rejected", requested)
		}
	}
}

func TestReadableTaskModelLabelIncludesModelAndEffort(t *testing.T) {
	models := []modeladapter.SubagentModel{{
		ID: "e7bbe0a5c209c3e2", DisplayName: "Luna worker", ModelID: "gpt-5.6-luna",
	}}
	if got := readableTaskModelLabel(models, "e7bbe0a5c209c3e2:medium"); got != "gpt-5.6-luna (medium)" {
		t.Fatalf("label=%q", got)
	}
	if got := readableTaskModelLabel(models, "e7bbe0a5c209c3e2"); got != "gpt-5.6-luna" {
		t.Fatalf("label without effort=%q", got)
	}
}

func TestReadableTaskModelLabelPreservesUnknownModel(t *testing.T) {
	if got := readableTaskModelLabel(nil, "unknown:max"); got != "unknown:max" {
		t.Fatalf("label=%q", got)
	}
}

func TestTaskPartialAndStartedEventsUseSameReadableModel(t *testing.T) {
	modelID := "e7bbe0a5c209c3e2:medium"
	models := []modeladapter.SubagentModel{{
		ID: "e7bbe0a5c209c3e2", DisplayName: "gpt-5.6-luna", ModelID: "gpt-5.6-luna",
	}}
	service := &Service{resolver: subagentModelTestResolver{models: models}}
	stream := &ActiveStream{SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
		"explore": {SubagentType: "explore", Selection: "model", ModelID: modelID},
	}}
	toolCall := &agentv1.ToolCall{Tool: &agentv1.ToolCall_TaskToolCall{TaskToolCall: &agentv1.TaskToolCall{
		Args: &agentv1.TaskArgs{
			Model: &modelID,
			SubagentType: &agentv1.SubagentType{Type: &agentv1.SubagentType_Explore{
				Explore: &agentv1.SubagentTypeExplore{},
			}},
		},
	}}}
	partial := service.rewriteTaskToolCallModelForDisplay(stream, toolCall)
	started := service.rewriteTaskToolCallModelForDisplay(stream, toolCall)
	if partial.GetTaskToolCall().GetArgs().GetModel() != "gpt-5.6-luna (medium)" {
		t.Fatalf("partial model=%q", partial.GetTaskToolCall().GetArgs().GetModel())
	}
	if started.GetTaskToolCall().GetArgs().GetModel() != partial.GetTaskToolCall().GetArgs().GetModel() {
		t.Fatalf("started model=%q, partial model=%q", started.GetTaskToolCall().GetArgs().GetModel(), partial.GetTaskToolCall().GetArgs().GetModel())
	}
	if toolCall.GetTaskToolCall().GetArgs().GetModel() != modelID {
		t.Fatalf("execution model was mutated: %q", toolCall.GetTaskToolCall().GetArgs().GetModel())
	}
}

func TestTaskDisplayUsesExplicitModelBeforeOverride(t *testing.T) {
	models := []modeladapter.SubagentModel{
		{ID: "channel-luna", DisplayName: "gpt-5.6-luna", ModelID: "gpt-5.6-luna"},
		{ID: "channel-terra", DisplayName: "gpt-5.6-terra", ModelID: "gpt-5.6-terra"},
	}
	service := &Service{resolver: subagentModelTestResolver{models: models}}
	stream := &ActiveStream{SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
		"explore": {SubagentType: "explore", Selection: "model", ModelID: "channel-luna:medium"},
	}}
	modelID := "channel-terra:max"
	toolCall := &agentv1.ToolCall{Tool: &agentv1.ToolCall_TaskToolCall{TaskToolCall: &agentv1.TaskToolCall{
		Args: &agentv1.TaskArgs{
			Model: &modelID,
			SubagentType: &agentv1.SubagentType{Type: &agentv1.SubagentType_Explore{
				Explore: &agentv1.SubagentTypeExplore{},
			}},
		},
	}}}
	got := service.rewriteTaskToolCallModelForDisplay(stream, toolCall)
	if got.GetTaskToolCall().GetArgs().GetModel() != "gpt-5.6-terra (max)" {
		t.Fatalf("display model=%q", got.GetTaskToolCall().GetArgs().GetModel())
	}
}

func TestResolveTaskModelAcceptsNaturalAliasesAndEffort(t *testing.T) {
	service := &Service{resolver: subagentModelTestResolver{models: []modeladapter.SubagentModel{{
		ID: "e7bbe0a5c209c3e2", DisplayName: "gpt-5.6-luna", ModelID: "gpt-5.6-luna",
	}}}}
	tests := []struct {
		name   string
		model  string
		effort string
		want   string
	}{
		{name: "short alias and field", model: "luna", effort: "max", want: "e7bbe0a5c209c3e2:max"},
		{name: "colon suffix", model: "luna:xhigh", want: "e7bbe0a5c209c3e2:xhigh"},
		{name: "natural suffix", model: "luna maximum", want: "e7bbe0a5c209c3e2:max"},
		{name: "explicit effort wins", model: "luna:low", effort: "high", want: "e7bbe0a5c209c3e2:high"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := map[string]any{"model": test.model, "task_role": "complex_debug"}
			if test.effort != "" {
				args["thinking_effort"] = test.effort
			}
			argsJSON, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			got, err := service.resolveTaskModel(runtimecore.ToolInvocation{ToolName: "Task", ArgsJSON: argsJSON})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			var resolved map[string]any
			if err := json.Unmarshal(got.ArgsJSON, &resolved); err != nil {
				t.Fatal(err)
			}
			if resolved["model"] != test.want || resolved["_model_source"] != "explicit" {
				t.Fatalf("resolved=%v, want model=%q", resolved, test.want)
			}
		})
	}
}

func TestResolveTaskModelRejectsUnknownAlias(t *testing.T) {
	service := &Service{resolver: subagentModelTestResolver{models: []modeladapter.SubagentModel{{
		ID: "e7bbe0a5c209c3e2", DisplayName: "gpt-5.6-luna", ModelID: "gpt-5.6-luna",
	}}}}
	_, err := service.resolveTaskModel(runtimecore.ToolInvocation{
		ToolName: "Task",
		ArgsJSON: []byte(`{"model":"terra max","task_role":"complex_debug"}`),
	})
	if err == nil {
		t.Fatal("unknown alias should be rejected")
	}
}

func TestTaskToolSchemaSupportsNaturalModelRequestsInEveryMode(t *testing.T) {
	modes := []agentv1.AgentMode{
		agentv1.AgentMode_AGENT_MODE_AGENT,
		agentv1.AgentMode_AGENT_MODE_ASK,
		agentv1.AgentMode_AGENT_MODE_DEBUG,
		agentv1.AgentMode_AGENT_MODE_MULTITASK,
		agentv1.AgentMode_AGENT_MODE_PLAN,
	}
	wantEfforts := []string{"disabled", "low", "medium", "high", "xhigh", "max"}
	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			tools, names, err := NewToolCatalog().Load(mode, "")
			if err != nil {
				t.Fatal(err)
			}
			index := slices.Index(names, "Task")
			if index < 0 {
				t.Fatal("Task tool is missing")
			}
			var descriptor struct {
				Function struct {
					Parameters struct {
						Properties map[string]struct {
							Description string   `json:"description"`
							Enum        []string `json:"enum"`
						} `json:"properties"`
					} `json:"parameters"`
				} `json:"function"`
			}
			if err := json.Unmarshal(tools[index], &descriptor); err != nil {
				t.Fatal(err)
			}
			model := descriptor.Function.Parameters.Properties["model"]
			if !strings.Contains(model.Description, "luna max") || !strings.Contains(model.Description, "thinking_effort='max'") {
				t.Fatalf("model guidance is incomplete: %q", model.Description)
			}
			effort := descriptor.Function.Parameters.Properties["thinking_effort"]
			if !slices.Equal(effort.Enum, wantEfforts) {
				t.Fatalf("thinking efforts=%v", effort.Enum)
			}
		})
	}
}

func TestRequestedModelVariantParsesWithoutVariantFlag(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(effort, func(t *testing.T) {
			model := &agentv1.RequestedModel{ModelId: "channel-a:" + effort}
			if got := extractRequestedModelIDFromRequestedModel(model); got != "channel-a" {
				t.Fatalf("model id=%q, want channel-a", got)
			}
			if got := extractRuntimeThinkingEffortFromRequestedModel(model); got != effort {
				t.Fatalf("thinking effort=%q, want %q", got, effort)
			}
		})
	}
}

func TestLegacyModelDetailsVariantParsesWithoutRequestedModel(t *testing.T) {
	message := &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		ModelDetails: &agentv1.ModelDetails{ModelId: "channel-a:max"},
	}}}
	if got := extractRequestedModelID(message); got != "channel-a" {
		t.Fatalf("model id=%q, want channel-a", got)
	}
	if got := extractRuntimeThinkingEffort(message); got != "max" {
		t.Fatalf("thinking effort=%q, want max", got)
	}
}

func TestResolveRequestedModelNameUsesConfiguredChannelNameForVariantDisplayID(t *testing.T) {
	service := &Service{resolver: subagentModelTestResolver{channel: &legacyruntime.ResolvedChannel{
		ID: "e7bbe0a5c209c3e2", Name: "gpt-5.6-luna", Model: "gpt-5.6-luna",
	}}}
	message := &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		RequestedModel: &agentv1.RequestedModel{ModelId: "e7bbe0a5c209c3e2:max"},
		ModelDetails:   &agentv1.ModelDetails{ModelId: "e7bbe0a5c209c3e2:max", DisplayModelId: "e7bbe0a5c209c3e2:max", DisplayName: "e7bbe0a5c209c3e2:max"},
	}}}
	if got := service.resolveRequestedModelName(message, "e7bbe0a5c209c3e2"); got != "gpt-5.6-luna" {
		t.Fatalf("model name=%q, want gpt-5.6-luna", got)
	}
}
