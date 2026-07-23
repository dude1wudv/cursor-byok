package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

type capabilityTestModelDirectory struct{}

func (capabilityTestModelDirectory) EnabledSubagentModels() []modeladapter.SubagentModel {
	return []modeladapter.SubagentModel{{ID: "test-adapter", DisplayName: "Test", ModelID: "test-model", Roles: []string{"simple_explore"}}}
}

func TestReadonlySubagentToolsMatchPreDispatchPolicy(t *testing.T) {
	_, names, err := NewToolCatalog().Load(agentv1.AgentMode_AGENT_MODE_PLAN, "generalPurpose")
	if err != nil {
		t.Fatal(err)
	}
	loaded := make(map[string]bool, len(names))
	for _, name := range names {
		loaded[name] = true
		if err := validateSubagentToolInvocation(agentv1.AgentMode_AGENT_MODE_PLAN, "generalPurpose", name, []byte("{}")); err != nil {
			t.Fatalf("loaded readonly tool %q is rejected: %v", name, err)
		}
	}
	for _, name := range []string{"Write", "PatchEdit", "Delete", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "CallMcpTool", "Task"} {
		if loaded[name] || isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_PLAN, "generalPurpose", name) {
			t.Fatalf("readonly child can use %q", name)
		}
	}
	if !loaded["FetchMcpResource"] {
		t.Fatal("readonly child is missing FetchMcpResource")
	}

	downloadArgs, err := json.Marshal(map[string]any{"server": "test", "uri": "test://resource", "downloadPath": "output.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSubagentToolInvocation(agentv1.AgentMode_AGENT_MODE_PLAN, "generalPurpose", "FetchMcpResource", downloadArgs); err == nil {
		t.Fatal("readonly downloadPath was accepted")
	}
}

func TestTaskAccessModeControlsFirstDispatchTools(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantMode  agentv1.AgentMode
		wantShell bool
	}{
		{name: "inspect", args: `{"subagent_type":"generalPurpose","access_mode":"inspect"}`, wantMode: agentv1.AgentMode_AGENT_MODE_PLAN},
		{name: "act", args: `{"subagent_type":"generalPurpose","access_mode":"act"}`, wantMode: agentv1.AgentMode_AGENT_MODE_AGENT, wantShell: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := runtimecore.DecodeArgsMap([]byte(tt.args))
			if err != nil {
				t.Fatal(err)
			}
			capability, err := runtimecore.ResolveTaskSubagentCapabilityFromArgs(args)
			if err != nil {
				t.Fatal(err)
			}
			mode := agentv1.AgentMode_AGENT_MODE_AGENT
			if capability.Readonly {
				mode = agentv1.AgentMode_AGENT_MODE_PLAN
			}
			if mode != tt.wantMode {
				t.Fatalf("mode = %s, want %s", mode, tt.wantMode)
			}
			_, names, err := NewToolCatalog().Load(mode, capability.Type)
			if err != nil {
				t.Fatal(err)
			}
			loaded := make(map[string]bool, len(names))
			for _, name := range names {
				loaded[name] = true
			}
			for _, name := range []string{"Write", "PatchEdit", "Delete", "Shell"} {
				if loaded[name] != tt.wantShell {
					t.Fatalf("tool %q loaded = %t, want %t", name, loaded[name], tt.wantShell)
				}
			}
		})
	}
}

func TestTaskCapabilityPreDispatchValidation(t *testing.T) {
	for _, payload := range []string{
		`{"subagent_type":"generalPurpose","access_mode":"act","readonly":true}`,
		`{"subagent_type":"generalPurpose","access_mode":"inspect","readonly":false}`,
		`{"subagent_type":"explore","access_mode":"act"}`,
	} {
		if err := validateTaskSubagentCapability([]byte(payload)); err == nil {
			t.Fatalf("invalid capability was accepted: %s", payload)
		}
	}
	for _, payload := range []string{
		`{"subagent_type":"generalPurpose","readonly":true}`,
		`{"subagent_type":"explore","readonly":false}`,
		`{"subagent_type":"generalPurpose","access_mode":"act"}`,
	} {
		if err := validateTaskSubagentCapability([]byte(payload)); err != nil {
			t.Fatalf("valid capability was rejected: %s: %v", payload, err)
		}
	}
}

func TestTaskSchemaUsesAccessMode(t *testing.T) {
	raw, err := promptassets.ReadTools(promptassets.ModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	assertTaskAccessModeSchema(t, raw, []string{"inspect", "act"})

	tools, _, err := NewToolCatalog(capabilityTestModelDirectory{}).Load(agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	assertTaskAccessModeSchema(t, encoded, []string{"inspect", "act"})

	nested, _, err := NewToolCatalog(capabilityTestModelDirectory{}).LoadForConversation(agentv1.AgentMode_AGENT_MODE_PLAN, "generalPurpose", "medium_explore")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(nested)
	if err != nil {
		t.Fatal(err)
	}
	assertTaskAccessModeSchema(t, encoded, []string{"inspect"})
}

func assertTaskAccessModeSchema(t *testing.T, raw []byte, wantModes []string) {
	t.Helper()
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		function, _ := tool["function"].(map[string]any)
		if function["name"] != "Task" {
			continue
		}
		description, _ := function["description"].(string)
		if strings.Contains(description, "browser-use") || strings.Contains(description, "- shell:") {
			t.Fatalf("Task description advertises unsupported type: %s", description)
		}
		parameters, _ := function["parameters"].(map[string]any)
		properties, _ := parameters["properties"].(map[string]any)
		if _, exists := properties["readonly"]; exists {
			t.Fatal("Task schema still exposes readonly")
		}
		accessMode, _ := properties["access_mode"].(map[string]any)
		modes, _ := accessMode["enum"].([]any)
		if len(modes) != len(wantModes) {
			t.Fatalf("access_mode enum = %v, want %v", modes, wantModes)
		}
		for index, mode := range wantModes {
			if modes[index] != mode {
				t.Fatalf("access_mode enum = %v, want %v", modes, wantModes)
			}
		}
		subagentType, _ := properties["subagent_type"].(map[string]any)
		for _, value := range subagentType["enum"].([]any) {
			if value != "generalPurpose" && value != "explore" && value != runtimecore.SubagentTypeLongContextRead {
				t.Fatalf("Task schema advertises unsupported subagent type %q", value)
			}
		}
		required, _ := parameters["required"].([]any)
		for _, field := range required {
			if field == "access_mode" {
				return
			}
		}
		t.Fatal("Task schema does not require access_mode")
	}
	t.Fatal("Task schema is missing")
}
