package execbridge

import (
	"strings"
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenTaskAppliesExplicitMaxAndWritableContract(t *testing.T) {
	bridge := NewBridge()
	message, _, err := bridge.OpenExec(OpenExecContext{ModelID: "parent", ThinkingEffort: "high"}, runtimecore.ToolInvocation{
		ToolName: "Task",
		CallID:   "task-1",
		ArgsJSON: []byte(`{"subagent_type":"explore","model":"child","thinking_effort":"max","task_role":"medium_explore","readonly":false,"prompt":"fix it"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	args := message.GetExecServerMessage().GetSubagentArgs()
	if args.GetModelId() != "child:max" {
		t.Fatalf("model=%q", args.GetModelId())
	}
	if !strings.Contains(args.GetPrompt(), "readonly=false") || !strings.Contains(args.GetPrompt(), "subagent_depth=1") || !strings.Contains(args.GetPrompt(), "fix it") {
		t.Fatalf("missing writable contract: %q", args.GetPrompt())
	}
}

func TestOpenTaskOverrideMaxBeatsParentHigh(t *testing.T) {
	bridge := NewBridge()
	message, _, err := bridge.OpenExec(OpenExecContext{
		ModelID:        "parent",
		ThinkingEffort: "high",
		SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
			"explore": {SubagentType: "explore", Selection: "model", ModelID: "child", MaxMode: true, ThinkingEffort: "max"},
		},
	}, runtimecore.ToolInvocation{
		ToolName: "Task", CallID: "task-2",
		ArgsJSON: []byte(`{"subagent_type":"explore","task_role":"medium_explore","readonly":true,"prompt":"inspect"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	args := message.GetExecServerMessage().GetSubagentArgs()
	if args.GetModelId() != "child:max" {
		t.Fatalf("model=%q", args.GetModelId())
	}
	if !strings.Contains(args.GetPrompt(), "readonly=true") {
		t.Fatalf("missing readonly contract: %q", args.GetPrompt())
	}
}

func TestOpenTaskExplicitModelBeatsOverride(t *testing.T) {
	bridge := NewBridge()
	message, _, err := bridge.OpenExec(OpenExecContext{
		ModelID: "parent", ThinkingEffort: "high",
		SubagentModelOverrides: map[string]runtimecore.SubagentModelOverrideSelection{
			"explore": {SubagentType: "explore", Selection: "model", ModelID: "override", ThinkingEffort: "low"},
		},
	}, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-explicit", ArgsJSON: []byte(`{"subagent_type":"explore","model":"explicit:max","_model_source":"explicit","task_role":"medium_explore","readonly":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := message.GetExecServerMessage().GetSubagentArgs().GetModelId(); got != "explicit:max" {
		t.Fatalf("model=%q, want explicit:max", got)
	}
}
