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
		args         map[string]any
		wantReadonly bool
		wantTaskMode agentv1.TaskMode
	}{
		{name: "inspect explore", args: map[string]any{"subagent_type": "explore", "access_mode": "inspect"}, wantReadonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "inspect general purpose", args: map[string]any{"subagent_type": "generalPurpose", "access_mode": "inspect"}, wantReadonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "act general purpose", args: map[string]any{"subagent_type": "generalPurpose", "access_mode": "act"}, wantReadonly: false, wantTaskMode: agentv1.TaskMode_TASK_MODE_AGENT},
		{name: "legacy readonly general purpose", args: map[string]any{"subagent_type": "generalPurpose", "readonly": true}, wantReadonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
		{name: "legacy explore normalization", args: map[string]any{"subagent_type": "explore", "readonly": false}, wantReadonly: true, wantTaskMode: agentv1.TaskMode_TASK_MODE_PLAN},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args["prompt"] = "test"
			args, err := json.Marshal(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			message, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{ToolName: "Task", ArgsJSON: args})
			if err != nil {
				t.Fatal(err)
			}
			got := message.GetExecServerMessage().GetSubagentArgs()
			if got.GetReadonly() != tt.wantReadonly || got.GetMode() != tt.wantTaskMode {
				t.Fatalf("capability = readonly %t, mode %s", got.GetReadonly(), got.GetMode())
			}
		})
	}
}

func TestOpenTaskRejectsInvalidCapability(t *testing.T) {
	for _, args := range []map[string]any{
		{"subagent_type": "generalPurpose", "access_mode": "act", "readonly": true},
		{"subagent_type": "generalPurpose", "access_mode": "inspect", "readonly": false},
		{"subagent_type": "explore", "access_mode": "act"},
		{"subagent_type": "unknown", "access_mode": "inspect"},
		{"subagent_type": "generalPurpose", "access_mode": "unknown"},
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
