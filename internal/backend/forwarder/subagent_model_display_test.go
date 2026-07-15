package forwarder

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	legacyruntime "cursor/internal/runtime"
)

type displayModelResolver struct{}

func (displayModelResolver) SelectChannelForModel(_ context.Context, modelID string) (*legacyruntime.ResolvedChannel, error) {
	if modelID != "b169b656217cef18" {
		return nil, legacyruntime.ErrChannelNotAvailable
	}
	return &legacyruntime.ResolvedChannel{
		ID:    modelID,
		Name:  "gpt-5.6-terra",
		Model: "gpt-5.6-terra",
	}, nil
}

func (displayModelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return 0
}

func TestTaskModelDisplayUsesChannelNameWithoutChangingExecutionModel(t *testing.T) {
	const channelID = "b169b656217cef18"
	argsJSON, err := json.Marshal(map[string]any{
		"subagent_type": "explore",
	})
	if err != nil {
		t.Fatal(err)
	}

	execution := rewriteTaskInvocationModelForExecution(runtimecore.ToolInvocation{
		ToolName: "Task",
		ArgsJSON: argsJSON,
	}, "parent-model", map[string]runtimecore.SubagentModelOverrideSelection{
		"explore": {
			SubagentType: "explore",
			Selection:    "model",
			ModelID:      channelID,
		},
	})

	var executionArgs map[string]any
	if err := json.Unmarshal(execution.ArgsJSON, &executionArgs); err != nil {
		t.Fatal(err)
	}
	if got := executionArgs["model"]; got != channelID {
		t.Fatalf("execution model = %v, want %q", got, channelID)
	}

	service := &Service{resolver: displayModelResolver{}}
	display := service.buildTaskToolCallForDisplay(execution)
	if got := display.GetTaskToolCall().GetArgs().GetModel(); got != "gpt-5.6-terra" {
		t.Fatalf("display model = %q, want %q", got, "gpt-5.6-terra")
	}
}

func TestTaskModelDisplayPreservesThinkingEffort(t *testing.T) {
	service := &Service{resolver: displayModelResolver{}}
	model := "b169b656217cef18 · High"
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_TaskToolCall{
			TaskToolCall: &agentv1.TaskToolCall{
				Args: &agentv1.TaskArgs{Model: stringPtr(model)},
			},
		},
	}

	display := service.rewriteTaskToolCallModelForResolvedID(toolCall, model)
	if got := display.GetTaskToolCall().GetArgs().GetModel(); got != "gpt-5.6-terra · High" {
		t.Fatalf("display model = %q, want %q", got, "gpt-5.6-terra · High")
	}
	if got := toolCall.GetTaskToolCall().GetArgs().GetModel(); got != model {
		t.Fatalf("source model mutated to %q", got)
	}
}

func TestCheckpointTaskModelDisplayUsesChannelName(t *testing.T) {
	model := "b169b656217cef18 · High"
	stepPayload, err := proto.Marshal(&agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_ToolCall{
			ToolCall: &agentv1.ToolCall{
				Tool: &agentv1.ToolCall_TaskToolCall{
					TaskToolCall: &agentv1.TaskToolCall{Args: &agentv1.TaskArgs{Model: stringPtr(model)}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turnPayload, err := proto.Marshal(&agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{Steps: [][]byte{stepPayload}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := &agentv1.ConversationStateStructure{Turns: [][]byte{turnPayload}}

	(&Service{resolver: displayModelResolver{}}).rewriteCheckpointTaskModelsForDisplay(state)

	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(state.Turns[0], turn); err != nil {
		t.Fatal(err)
	}
	step := &agentv1.ConversationStep{}
	if err := proto.Unmarshal(turn.GetAgentConversationTurn().GetSteps()[0], step); err != nil {
		t.Fatal(err)
	}
	if got := step.GetToolCall().GetTaskToolCall().GetArgs().GetModel(); got != "gpt-5.6-terra · High" {
		t.Fatalf("checkpoint model = %q, want %q", got, "gpt-5.6-terra · High")
	}
}
