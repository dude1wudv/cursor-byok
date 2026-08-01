package execbridge

import (
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenSubagentAwaitAndStillRunningRemainNonTerminal(t *testing.T) {
	bridge := NewBridge()
	message, pending, err := bridge.OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{
		CallID:   "tool-await",
		ToolName: "SubagentAwait",
		ArgsJSON: []byte(`{"agent_id":"agent-1","timeout_ms":1200}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	args := message.GetExecServerMessage().GetSubagentAwaitArgs()
	if args == nil || args.GetAgentId() != "agent-1" || args.GetTimeoutMs() != 1200 {
		t.Fatalf("SubagentAwait args = %#v", args)
	}
	if pending.ExecKind != "subagent_await" || pending.ToolCallID != "tool-await" {
		t.Fatalf("pending = %#v", pending)
	}

	result, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id: pending.MessageID, ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentAwaitResult{SubagentAwaitResult: &agentv1.SubagentAwaitResult{
			Result: &agentv1.SubagentAwaitResult_StillRunning{StillRunning: &agentv1.SubagentAwaitStillRunning{AgentId: "agent-1"}},
		}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsTerminal || result.ToolCall != nil {
		t.Fatalf("still_running became terminal: %#v", result)
	}
}

func TestSubagentAwaitTerminalResultsConvertToTaskResults(t *testing.T) {
	complete := &agentv1.SubagentAwaitResult{Result: &agentv1.SubagentAwaitResult_Complete{
		Complete: &agentv1.SubagentAwaitComplete{AgentId: "agent-1", FinalMessage: stringPtr("done")},
	}}
	converted := ConvertSubagentAwaitResult(complete)
	if converted == nil || converted.GetSuccess().GetAgentId() != "agent-1" || converted.GetSuccess().GetFinalMessage() != "done" {
		t.Fatalf("complete conversion = %#v", converted)
	}

	notFound := &agentv1.SubagentAwaitResult{Result: &agentv1.SubagentAwaitResult_NotFound{
		NotFound: &agentv1.SubagentAwaitNotFound{AgentId: "agent-1"},
	}}
	converted = ConvertSubagentAwaitResult(notFound)
	if converted == nil || converted.GetError().GetError() != "subagent not found" || converted.GetError().GetAgentId() != "agent-1" {
		t.Fatalf("not_found conversion = %#v", converted)
	}
}
