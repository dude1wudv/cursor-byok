package forwarder

import (
	"encoding/json"
	"reflect"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestResolveTaskThinkingEffortPriority(t *testing.T) {
	tests := []struct{ name, requested, role, parent, want, source string }{
		{"explicit", "medium", "complex_debug", "max", "medium", "explicit"},
		{"role default", "", "simple_explore", "max", "low", "role_default"},
		{"parent inherited", "", "", "xhigh", "xhigh", "parent_inherited"},
		{"unset", "", "", "", "", "unset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source := resolveTaskThinkingEffort(tt.requested, tt.role, tt.parent)
			if got != tt.want || source != tt.source {
				t.Fatalf("got (%q,%q), want (%q,%q)", got, source, tt.want, tt.source)
			}
		})
	}
	for _, effort := range []string{"disabled", "low", "medium", "high", "xhigh", "max"} {
		got, source := resolveTaskThinkingEffort(effort, "simple_explore", "max")
		if got != effort || source != "explicit" {
			t.Fatalf("effort %q got (%q,%q)", effort, got, source)
		}
	}
}

func TestMatchedDispatchEffortOverridesCursorDefault(t *testing.T) {
	launch := pendingSubagentLaunch{ThinkingEffort: "medium"}
	if got := applyMatchedSubagentThinkingEffort("max", launch, true); got != "medium" {
		t.Fatalf("got %q, want medium", got)
	}
	if got := applyMatchedSubagentThinkingEffort("high", launch, false); got != "high" {
		t.Fatalf("unmatched got %q", got)
	}
}

func TestTaskBarrierWaitsAcrossProviderPasses(t *testing.T) {
	stream := &ActiveStream{TaskBatches: map[int]*TaskBatch{
		1: {Generation: 1, ProviderPass: 1, Members: map[string]*TaskBatchMember{"a": {ToolCallID: "a", Terminal: true}}},
		2: {Generation: 2, ProviderPass: 2, Members: map[string]*TaskBatchMember{"b": {ToolCallID: "b"}}},
	}}
	if generation, ready := taskBatchAllowsParentNotification(stream); ready || generation != 2 {
		t.Fatalf("ready=%t generation=%d", ready, generation)
	}
	markTaskBatchTerminalLocked(stream, runtimecore.PendingExec{ExecKind: "subagent", ToolCallID: "b", ProviderPass: 2})
	if generation, ready := taskBatchAllowsParentNotification(stream); !ready || generation != 2 {
		t.Fatalf("ready=%t generation=%d", ready, generation)
	}
	if !stream.TaskBatches[1].ParentNotified || !stream.TaskBatches[2].ParentNotified {
		t.Fatalf("batches not atomically notified: %#v", stream.TaskBatches)
	}
}

type stableToolCatalogStub struct{}

func (stableToolCatalogStub) Load(mode agentv1.AgentMode, _ string) ([]json.RawMessage, []string, error) {
	read := json.RawMessage(`{"function":{"name":"Read"}}`)
	if normalizeMode(mode) == agentv1.AgentMode_AGENT_MODE_PLAN {
		return []json.RawMessage{read, json.RawMessage(`{"function":{"name":"CreatePlan"}}`)}, []string{"Read", "CreatePlan"}, nil
	}
	return []json.RawMessage{json.RawMessage(`{"function":{"name":"Write"}}`), read}, []string{"Write", "Read"}, nil
}

func TestStableProviderToolCatalogKeepsPlanAgentSuperset(t *testing.T) {
	planTools, planNames, err := loadStableProviderToolCatalogForConversation(stableToolCatalogStub{}, agentv1.AgentMode_AGENT_MODE_PLAN, &ConversationFile{})
	if err != nil {
		t.Fatal(err)
	}
	agentTools, agentNames, err := loadStableProviderToolCatalogForConversation(stableToolCatalogStub{}, agentv1.AgentMode_AGENT_MODE_AGENT, &ConversationFile{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planNames, agentNames) || len(planTools) != len(agentTools) {
		t.Fatalf("plan=%v agent=%v", planNames, agentNames)
	}
	if err := validateSubagentToolInvocationForConversation(agentv1.AgentMode_AGENT_MODE_PLAN, "", "", "Write", nil); err == nil {
		t.Fatal("Plan mode must still reject Write at dispatch")
	}
}
