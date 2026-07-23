package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestShouldOpenShellCircuit(t *testing.T) {
	tests := []struct {
		name            string
		state           shellCircuitState
		rejectionClass  string
		wantOpenCircuit bool
	}{
		{name: "capability rejection", rejectionClass: "capability", wantOpenCircuit: true},
		{name: "permission rejection", rejectionClass: "permission", wantOpenCircuit: true},
		{name: "first parser rejection", rejectionClass: "command_parse"},
		{name: "second parser rejection remains metadata", state: shellCircuitState{ParseRejections: 1}, rejectionClass: "command_parse"},
		{name: "already open", state: shellCircuitState{Open: true}, rejectionClass: "capability"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldOpenShellCircuit(tt.state, tt.rejectionClass); got != tt.wantOpenCircuit {
				t.Fatalf("shouldOpenShellCircuit() = %t, want %t", got, tt.wantOpenCircuit)
			}
		})
	}
}

func TestShellTerminalRejectionClassifiesSkippedStream(t *testing.T) {
	message := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ShellStream{
			ShellStream: &agentv1.ShellStream{
				Event: &agentv1.ShellStream_Rejected{
					Rejected: &agentv1.ShellRejected{Reason: "Skipped by Cursor"},
				},
			},
		},
	}
	reason, class := shellTerminalRejection(message)
	if reason != "Skipped by Cursor" || class != "capability" {
		t.Fatalf("shellTerminalRejection() = (%q, %q), want skipped capability rejection", reason, class)
	}
}

func TestCurrentTurnShellCircuitIgnoresTransportStall(t *testing.T) {
	const turnSeq = int64(7)
	stream := &ActiveStream{
		TurnSeq: turnSeq,
		CheckpointConversation: &ConversationFile{Entries: []HistoryEntry{
			newMetadataEntry(turnSeq, "request", "shell_stream_stalled", map[string]any{
				"reason": "transport_closed",
			}),
		}},
	}
	if circuit := currentTurnShellCircuit(stream); circuit.Open {
		t.Fatal("transport-only shell stall opened the rejection circuit")
	}
}

func TestForegroundShellDispatchFIFO(t *testing.T) {
	first := runtimecore.PendingExec{ExecID: "exec-1", ExecKind: "shell", ProviderPass: 3}
	second := runtimecore.PendingExec{ExecID: "exec-2", ExecKind: "shell", ProviderPass: 3}
	stream := &ActiveStream{}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, first) {
		t.Fatal("first shell was unexpectedly queued")
	}
	if reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, second) {
		t.Fatal("second shell bypassed active foreground shell")
	}
	if next, ok := releaseForegroundShellDispatch(stream, first); !ok || next.Pending.ExecID != second.ExecID {
		t.Fatalf("released next = %#v, ok=%t", next, ok)
	}
	if _, ok := releaseForegroundShellDispatch(stream, first); ok {
		t.Fatal("duplicate terminal advanced queue twice")
	}
	if _, ok := releaseForegroundShellDispatch(stream, second); ok {
		t.Fatal("empty queue produced another dispatch")
	}
}

func TestShellSkippedAndStallDoNotReleaseQueueButLateExitDoes(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	first := runtimecore.PendingExec{
		MessageID:      21,
		ExecID:         "exec-1",
		ConversationID: "conversation",
		ToolCallID:     "tool-1",
		ExecKind:       "shell",
		ProviderPass:   1,
		ModelCallID:    "model-call",
		ArgsJSON:       []byte(`{"command":"git status"}`),
	}
	second := runtimecore.PendingExec{
		MessageID:      22,
		ExecID:         "exec-2",
		ConversationID: "conversation",
		ToolCallID:     "tool-2",
		ExecKind:       "shell",
		ProviderPass:   1,
		ModelCallID:    "model-call",
		ArgsJSON:       []byte(`{"command":"git diff --check"}`),
	}
	stream.PendingExecs[first.ExecID] = first
	stream.PendingExecs[second.ExecID] = second
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, first) {
		t.Fatal("first shell was not active")
	}
	if reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, second) {
		t.Fatal("second shell was not queued")
	}
	service := &Service{
		broker:     broker,
		projector:  NewHistoryProjector(),
		execBridge: execbridge.NewBridge(),
		debug:      newDebugRecorder("", broker, nil),
	}
	skipped := &agentv1.ExecClientMessage{
		Id: first.MessageID, ExecId: first.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Rejected{
			Rejected: &agentv1.ShellRejected{Reason: "Skipped by Cursor"},
		}}},
	}
	if err := service.handleExecResult(InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: skipped}); err != nil {
		t.Fatal(err)
	}
	if stream.ActiveForegroundShellExecID != first.ExecID || len(stream.QueuedForegroundShells) != 1 {
		t.Fatalf("skipped advanced queue: active=%q queued=%d", stream.ActiveForegroundShellExecID, len(stream.QueuedForegroundShells))
	}
	if err := service.recoverShellWithoutTerminal(stream, first, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatal(err)
	}
	if stream.ActiveForegroundShellExecID != first.ExecID || len(stream.QueuedForegroundShells) != 1 {
		t.Fatalf("stall advanced queue: active=%q queued=%d", stream.ActiveForegroundShellExecID, len(stream.QueuedForegroundShells))
	}
	lateExit := &agentv1.ExecClientMessage{
		Id: first.MessageID, ExecId: first.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Exit{
			Exit: &agentv1.ShellStreamExit{Code: 0},
		}}},
	}
	if err := service.handleExecResult(InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: lateExit}); err != nil {
		t.Fatal(err)
	}
	if stream.ActiveForegroundShellExecID != second.ExecID || len(stream.QueuedForegroundShells) != 0 {
		t.Fatalf("late exit did not release next shell: active=%q queued=%d", stream.ActiveForegroundShellExecID, len(stream.QueuedForegroundShells))
	}
	if _, found := stream.PendingExecs[first.ExecID]; found {
		t.Fatal("late terminal result did not remove completed shell")
	}
	if _, found := stream.PendingExecs[second.ExecID]; !found {
		t.Fatal("next queued shell was not retained as pending")
	}
	toolResults := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == first.ToolCallID {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("late exit tool results=%d, want 1", toolResults)
	}
}

func TestSelectPendingExecStrictRejectsMixedIDs(t *testing.T) {
	first := runtimecore.PendingExec{MessageID: 31, ExecID: "exec-31", ToolCallID: "tool-31", ExecKind: "shell"}
	second := runtimecore.PendingExec{MessageID: 32, ExecID: "exec-32", ToolCallID: "tool-32", ExecKind: "shell"}
	stream := &ActiveStream{PendingExecs: map[string]runtimecore.PendingExec{first.ExecID: first, second.ExecID: second}}
	if _, found, mismatch := selectPendingExecStrict(first.ExecID, second.MessageID, stream); found || !mismatch {
		t.Fatalf("mixed IDs found=%t mismatch=%t", found, mismatch)
	}
	if pending, found, mismatch := selectPendingExecStrict(first.ExecID, first.MessageID, stream); !found || mismatch || pending.ToolCallID != first.ToolCallID {
		t.Fatalf("matching IDs pending=%#v found=%t mismatch=%t", pending, found, mismatch)
	}
}

func TestShellStallRecoveryDoesNotCompletePending(t *testing.T) {
	pending := runtimecore.PendingExec{MessageID: 9, ExecID: "exec-stalled", ToolCallID: "tool-stalled", ExecKind: "shell"}
	stream := &ActiveStream{
		RequestID:              "request",
		ConversationID:         "conversation",
		TurnSeq:                1,
		PendingExecs:           map[string]runtimecore.PendingExec{pending.ExecID: pending},
		CheckpointConversation: &ConversationFile{ConversationID: "conversation"},
	}
	if err := (&Service{}).recoverShellWithoutTerminal(stream, pending, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; !ok {
		t.Fatal("stall recovery removed pending shell")
	}
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" {
			t.Fatal("stall recovery synthesized terminal tool result")
		}
	}
}
