package forwarder

import (
	"encoding/json"
	"testing"

	"cursor/gen/agentv1"
)

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

func TestWritableSubagentToolsMatchPreDispatchPolicy(t *testing.T) {
	_, names, err := NewToolCatalog().Load(agentv1.AgentMode_AGENT_MODE_AGENT, "generalPurpose")
	if err != nil {
		t.Fatal(err)
	}
	loaded := make(map[string]bool, len(names))
	for _, name := range names {
		loaded[name] = true
	}
	for _, name := range []string{"Write", "PatchEdit", "Delete", "Shell", "CallMcpTool"} {
		if !loaded[name] || !isToolAllowedInMode(agentv1.AgentMode_AGENT_MODE_AGENT, "generalPurpose", name) {
			t.Fatalf("writable child cannot use %q", name)
		}
	}
	if err := validateTaskSubagentCapability([]byte(`{"subagent_type":"explore","readonly":false}`)); err != nil {
		t.Fatalf("explore readonly normalization failed: %v", err)
	}
	if err := validateTaskSubagentCapability([]byte(`{"subagent_type":"unknown","readonly":true}`)); err == nil {
		t.Fatal("unknown subagent type was accepted")
	}
}
