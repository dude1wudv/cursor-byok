package execbridge

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenTaskNormalizesCapability(t *testing.T) {
	tests := []struct {
		name         string
		subagentType string
		model        string
		readonly     bool
		wantTaskMode agentv1.TaskMode
	}{
		{name: "readonly explore", subagentType: "explore", readonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "readonly long context", subagentType: runtimecore.SubagentTypeLongContextRead, model: "grok-channel", readonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "readonly general purpose", subagentType: "generalPurpose", readonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "writable general purpose", subagentType: "generalPurpose", readonly: false, wantTaskMode: agentv1.TaskMode_TASK_MODE_AGENT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]any{
				"subagent_type": tt.subagentType,
				"model":         tt.model,
				"readonly":      tt.readonly,
				"prompt":        "test",
			})
			if err != nil {
				t.Fatal(err)
			}
			message, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{ToolName: "Task", ArgsJSON: args})
			if err != nil {
				t.Fatal(err)
			}
			got := message.GetExecServerMessage().GetSubagentArgs()
			if got.GetReadonly() != tt.readonly || got.GetMode() != tt.wantTaskMode {
				t.Fatalf("capability = readonly %t, mode %s", got.GetReadonly(), got.GetMode())
			}
		})
	}
}

func TestOpenTaskRejectsInvalidCapability(t *testing.T) {
	for _, args := range []map[string]any{
		{"subagent_type": "explore", "readonly": false},
		{"subagent_type": runtimecore.SubagentTypeLongContextRead, "readonly": false, "model": "grok-channel"},
		{"subagent_type": runtimecore.SubagentTypeLongContextRead, "readonly": true},
		{"subagent_type": "unknown", "readonly": true},
		{"subagent_type": "generalPurpose"},
	} {
		payload, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{ToolName: "Task", ArgsJSON: payload}); err == nil {
			t.Fatalf("OpenExec(%v) succeeded", args)
		}
	}
}
