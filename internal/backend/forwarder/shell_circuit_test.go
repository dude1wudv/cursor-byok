package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

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

func TestForegroundShellDispatchWaitsForOpeningHandshake(t *testing.T) {
	first := runtimecore.PendingExec{MessageID: 11, ExecID: "exec-1", ToolCallID: "tool-1", ExecKind: "shell", ProviderPass: 1}
	second := runtimecore.PendingExec{MessageID: 12, ExecID: "exec-2", ToolCallID: "tool-2", ExecKind: "shell", ProviderPass: 1}
	stream := &ActiveStream{ShellMaxConcurrent: 3}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, first) {
		t.Fatal("first shell was unexpectedly queued")
	}
	if reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, second) {
		t.Fatal("second shell bypassed the opening handshake gate")
	}
	if stream.ShellAwaitingStartExecID != first.ExecID || len(stream.QueuedForegroundShells) != 1 {
		t.Fatalf("opening gate=%q queued=%d", stream.ShellAwaitingStartExecID, len(stream.QueuedForegroundShells))
	}
	first = stream.PendingExecs[first.ExecID]
	first.StreamState = shellLifecycleStarted
	if !acknowledgeForegroundShellStart(stream, first) {
		t.Fatal("first shell handshake did not release the opening gate")
	}
	stream.mu.Lock()
	next, ok := takeNextForegroundShellDispatchLocked(stream)
	stream.mu.Unlock()
	if !ok || next.Pending.ExecID != second.ExecID {
		t.Fatalf("next dispatch=%#v ok=%t", next.Pending, ok)
	}
	if stream.ShellAwaitingStartExecID != second.ExecID || len(stream.ActiveForegroundShells) != 2 {
		t.Fatalf("next opening gate=%q active=%d", stream.ShellAwaitingStartExecID, len(stream.ActiveForegroundShells))
	}
}

func TestSkippedLateActivityCancelsRetry(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	pending := runtimecore.PendingExec{
		MessageID: 15, ExecID: "exec-1", ConversationID: "conversation", ToolCallID: "tool-1", LogicalShellID: "tool-1",
		ExecKind: "shell", ProviderPass: 1, ShellAttempt: 1, ArgsJSON: []byte(`{"command":"git status"}`),
	}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
		t.Fatal("shell was unexpectedly queued")
	}
	service := &Service{broker: broker, debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	pending = stream.PendingExecs[pending.ExecID]
	pending.StreamState = shellLifecycleStarted
	stream.PendingExecs[pending.ExecID] = pending
	pending = service.refreshShellForegroundActivity(stream, pending)
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; !ok {
		t.Fatal("late shell activity did not cancel skipped recovery")
	}
	if len(stream.QueuedForegroundShells) != 0 {
		t.Fatal("late shell activity still produced a retry")
	}
}

func TestSkippedReadonlyShellRetriesOnlyAfterAbortGrace(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{
		MessageID: 21, ExecID: "exec-1", ConversationID: "conversation", ToolCallID: "tool-1", LogicalShellID: "tool-1",
		ExecKind: "shell", ProviderPass: 1, ModelCallID: "model-call", ShellAttempt: 1, ArgsJSON: []byte(`{"command":"git status"}`),
	}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
		t.Fatal("readonly shell was unexpectedly queued")
	}
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	current := stream.PendingExecs[pending.ExecID]
	if current.ShellRecoveryState != shellRecoveryStateAbortRequested {
		t.Fatalf("recovery state=%d, want abort requested", current.ShellRecoveryState)
	}
	if len(stream.QueuedForegroundShells) != 0 {
		t.Fatal("readonly shell retried before abort grace")
	}
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; ok {
		t.Fatal("old transport attempt remained pending after retry handoff")
	}
	if len(stream.QueuedForegroundShells) != 1 {
		t.Fatalf("queued retries=%d, want 1", len(stream.QueuedForegroundShells))
	}
	retry := stream.QueuedForegroundShells[0].Pending
	if retry.ShellAttempt != 2 || retry.LogicalShellID != pending.LogicalShellID || retry.ExecID == pending.ExecID {
		t.Fatalf("retry identity=%#v", retry)
	}
	lateExit := &agentv1.ExecClientMessage{
		Id: pending.MessageID, ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Exit{
			Exit: &agentv1.ShellStreamExit{Code: 0},
		}}},
	}
	if err := service.handleExecResult(InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: lateExit}); err != nil {
		t.Fatal(err)
	}
	if len(stream.QueuedForegroundShells) != 1 || stream.QueuedForegroundShells[0].Pending.ExecID != retry.ExecID {
		t.Fatal("late terminal event from old attempt disturbed the retry")
	}
}

func TestSkippedReadonlyShellAtAttemptLimitReturnsUnknown(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{
		MessageID: 25, ExecID: "exec-5", ConversationID: "conversation", ToolCallID: "tool-5", LogicalShellID: "tool-5",
		ExecKind: "shell", ProviderPass: 1, ModelCallID: "model-call", ShellAttempt: shellMaxTransportAttempts, ArgsJSON: []byte(`{"command":"git status"}`),
	}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
		t.Fatal("readonly shell was unexpectedly queued")
	}
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	if len(stream.QueuedForegroundShells) != 0 {
		t.Fatal("readonly shell retried beyond the transport attempt limit")
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; ok {
		t.Fatal("attempt-limited shell remained pending without a terminal result")
	}
	results := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == pending.ToolCallID {
			results++
			if !strings.Contains(string(entry.Payload), "status is unknown") {
				t.Fatalf("unexpected result payload: %s", entry.Payload)
			}
			// 只读 inspect 的最终 Skipped 应投影为 silent backgrounded，而不是 Rejected。
			// result_text 可保留 "Skipped" 供模型消费；UI 形态看 tool_call 是否 backgrounded success。
			if strings.Contains(string(entry.Payload), `"rejected"`) {
				t.Fatalf("safe inspect skip still projected as rejected UI payload: %s", entry.Payload)
			}
			if !strings.Contains(string(entry.Payload), `"isBackground"`) && !strings.Contains(string(entry.Payload), `"is_background"`) {
				t.Fatalf("safe inspect skip missing backgrounded projection: %s", entry.Payload)
			}
			if !strings.Contains(string(entry.Payload), `"success"`) {
				t.Fatalf("safe inspect skip missing success projection: %s", entry.Payload)
			}
		}
		if entry.Kind == "metadata" && strings.Contains(string(entry.Payload), "shell_stream_recovered") {
			if !strings.Contains(string(entry.Payload), `"silent_inspect_skip":true`) && !strings.Contains(string(entry.Payload), `"silent_inspect_skip": true`) {
				t.Fatalf("recovery metadata missing silent_inspect_skip: %s", entry.Payload)
			}
		}
	}
	if results != 1 {
		t.Fatalf("tool results=%d, want 1", results)
	}
}

func TestSkippedMutatingShellReturnsUnknownWithoutRetry(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{
		MessageID: 31, ExecID: "exec-1", ConversationID: "conversation", ToolCallID: "tool-1", LogicalShellID: "tool-1",
		ExecKind: "shell", ProviderPass: 1, ModelCallID: "model-call", ShellAttempt: 1, ArgsJSON: []byte(`{"command":"git commit -m test"}`),
	}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
		t.Fatal("mutating shell was unexpectedly queued")
	}
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	if len(stream.QueuedForegroundShells) != 0 {
		t.Fatal("mutating shell was automatically retried")
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; ok {
		t.Fatal("mutating shell remained pending without a terminal result")
	}
	results := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == pending.ToolCallID {
			results++
			if !strings.Contains(string(entry.Payload), "status is unknown") {
				t.Fatalf("unexpected result payload: %s", entry.Payload)
			}
		}
	}
	if results != 1 {
		t.Fatalf("tool results=%d, want 1", results)
	}
}

func TestBackgroundShellLeaseRenewalAndCompletedPruning(t *testing.T) {
	now := time.Now().UTC()
	stream := &ActiveStream{
		BackgroundShells: map[string]*BackgroundShellState{
			"7": {ShellID: "7", Status: backgroundShellStatusRunning, LastActivityAt: now.Add(-time.Minute)},
			"8": {ShellID: "8", Status: backgroundShellStatusCompleted, OriginalMessageID: 8, OriginalExecID: "exec-8", CompletedAt: now.Add(-backgroundShellCompletedRetention - time.Second)},
			"9": {ShellID: "9", Status: backgroundShellStatusCompleted, CompletedAt: now.Add(-backgroundShellCompletedRetention - time.Second), LastActivityAt: now.Add(-time.Minute)},
		},
		BackgroundShellsByMessageID: map[uint32]string{8: "8"},
		BackgroundShellsByExecID:    map[string]string{"exec-8": "8"},
	}
	pruneBackgroundShellsLocked(stream, now)
	if _, ok := stream.BackgroundShells["8"]; ok {
		t.Fatal("expired completed background shell was not pruned")
	}
	if _, ok := stream.BackgroundShellsByMessageID[8]; ok {
		t.Fatal("expired background shell message index was not pruned")
	}
	if _, ok := stream.BackgroundShellsByExecID["exec-8"]; ok {
		t.Fatal("expired background shell exec index was not pruned")
	}
	if _, ok := stream.BackgroundShells["9"]; !ok {
		t.Fatal("recently renewed completed background shell was pruned")
	}
	before := stream.BackgroundShells["7"].LastActivityAt
	blockUntilMS := int64(0)
	result := (&Service{}).awaitShellSnapshot(stream, awaitShellArgs{ShellID: "7", BlockUntilMS: &blockUntilMS})
	if result.Status != backgroundShellStatusRunning || !stream.BackgroundShells["7"].LastActivityAt.After(before) {
		t.Fatalf("background lease was not renewed: result=%#v state=%#v", result, stream.BackgroundShells["7"])
	}
}

func TestWriteShellStdinErrorExpiresBackgroundShell(t *testing.T) {
	now := time.Now().UTC()
	stream := &ActiveStream{BackgroundShells: map[string]*BackgroundShellState{
		"7": {ShellID: "7", Status: backgroundShellStatusRunning, CreatedAt: now.Add(-time.Minute)},
	}}
	pending := runtimecore.PendingExec{ExecKind: "write_shell_stdin", ArgsJSON: []byte(`{"shell_id":7,"chars":"x"}`)}
	message := &agentv1.ExecClientMessage{Message: &agentv1.ExecClientMessage_WriteShellStdinResult{
		WriteShellStdinResult: &agentv1.WriteShellStdinResult{Result: &agentv1.WriteShellStdinResult_Error{
			Error: &agentv1.WriteShellStdinError{Error: "shell no longer exists"},
		}},
	}}
	(&Service{}).observeWriteShellStdinResult(stream, pending, message)
	state := stream.BackgroundShells["7"]
	if state.Status != backgroundShellStatusTransportClosed || state.CompletedAt.IsZero() {
		t.Fatalf("write error did not expire shell: %#v", state)
	}
	if !strings.Contains(state.StderrBuffer, "no longer exists") {
		t.Fatalf("write error was not retained: %q", state.StderrBuffer)
	}
}

func TestShellFinalizationKeepsPendingUntilResultPersisted(t *testing.T) {
	pending := runtimecore.PendingExec{MessageID: 41, ExecID: "exec-1", ToolCallID: "tool-1", ExecKind: "shell", ProviderPass: 1, StreamState: "exited"}
	stream := &ActiveStream{
		PendingExecs:           map[string]runtimecore.PendingExec{pending.ExecID: pending},
		ActiveForegroundShells: map[string]runtimecore.PendingExec{pending.ExecID: pending},
		ShellExecTombstones:    map[string]shellExecTombstone{},
		RecentCompletedExecs:   map[uint32]time.Time{},
	}
	claimed, ok := beginShellFinalization(stream, pending)
	if !ok || claimed.StreamState != shellLifecycleFinalizing {
		t.Fatalf("claimed=%#v ok=%t", claimed, ok)
	}
	if _, exists := stream.PendingExecs[pending.ExecID]; !exists {
		t.Fatal("finalizing shell was removed before persistence")
	}
	rollbackShellFinalization(stream, pending)
	if got := stream.PendingExecs[pending.ExecID].StreamState; got != pending.StreamState {
		t.Fatalf("rollback state=%q, want %q", got, pending.StreamState)
	}
	claimed, ok = beginShellFinalization(stream, pending)
	if !ok {
		t.Fatal("shell could not reclaim finalization after rollback")
	}
	claimed = markShellTerminalPersisted(stream, claimed)
	markExecCompleted(stream, claimed)
	if _, exists := stream.PendingExecs[pending.ExecID]; exists {
		t.Fatal("persisted shell remained pending after completion")
	}
	if _, exists := stream.ShellExecTombstones[pending.ExecID]; !exists {
		t.Fatal("persisted shell did not create a tombstone")
	}
}

func TestShellFinalizationFailureRearmsRecovery(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{MessageID: 51, ExecID: "exec-1", ToolCallID: "tool-1", ExecKind: "shell", ProviderPass: 1, StreamState: "exited"}
	stream.PendingExecs[pending.ExecID] = pending
	stream.ActiveForegroundShells[pending.ExecID] = pending
	claimed, ok := beginShellFinalization(stream, pending)
	if !ok {
		t.Fatal("shell did not enter finalizing")
	}
	originalToolCall := execbridge.BuildShellRejectedToolCall(pending.ToolCallID, pending.ArgsJSON, "original terminal result")
	claimed = snapshotShellTerminalResult(stream, claimed, pending.ToolCallID, "original exit payload", originalToolCall)
	rollbackShellFinalization(stream, pending)
	service := &Service{broker: broker, projector: NewHistoryProjector(), debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellFinalizationRetry(stream, claimed)
	current := stream.PendingExecs[pending.ExecID]
	if current.ShellRecoveryState != shellRecoveryStateCandidate || current.ShellRecoveryReason != shellRecoveryReasonPersistenceFailed {
		t.Fatalf("finalization retry state=%#v claimed=%#v", current, claimed)
	}
	if !current.ShellTerminalSnapshotReady || current.ShellTerminalResultPayload != "original exit payload" {
		t.Fatalf("terminal snapshot was not retained: %#v", current)
	}
	if _, ok := stream.TimerTokens[providerTimerKey(streamTimerShellSupervision, pending.ExecID)]; !ok {
		t.Fatal("finalization failure did not rearm shell supervision")
	}
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonPersistenceFailed); err != nil {
		t.Fatal(err)
	}
	if _, exists := stream.PendingExecs[pending.ExecID]; exists {
		t.Fatal("persisted terminal retry remained pending")
	}
	var persisted toolResultEntryPayload
	found := false
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind != "tool_result" || entry.ToolCallID != pending.ToolCallID {
			continue
		}
		if err := json.Unmarshal(entry.Payload, &persisted); err != nil {
			t.Fatal(err)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("persisted terminal retry did not write tool_result")
	}
	if persisted.ResultText != "original exit payload" || !strings.Contains(string(persisted.ToolCall), "original terminal result") {
		t.Fatalf("persisted terminal was rewritten: %#v", persisted)
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

func TestPreDispatchShellRejectionOpensCircuitOnRepeat(t *testing.T) {
	// 复现实际观测到的空转：inspect 子代理同一命令因确定性校验错误被反复拒绝。
	// 第 1 次仅记账，第 2 次同指纹开路，第 3 次起由 handleToolInvocation 的 circuit.Open 分支拦截。
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_PLAN, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "plan", NextTurnSeq: 2, NextEntrySeq: 1}
	service := &Service{
		broker:    broker,
		projector: NewHistoryProjector(),
		debug:     newDebugRecorder("", broker, nil),
	}
	invocation := runtimecore.ToolInvocation{
		CallID:   "tool-1",
		ToolName: "Shell",
		ArgsJSON: []byte(`{"command":"git push origin main","working_directory":"E:\\repo"}`),
	}
	cause := fmt.Errorf("inspect Shell git subcommand %q is not read-only", "push")

	opened, err := service.recordPreDispatchShellRejection(stream, invocation, cause)
	if err != nil {
		t.Fatal(err)
	}
	if opened {
		t.Fatal("first deterministic rejection must not open the circuit")
	}
	if circuit := currentTurnShellCircuit(stream); circuit.Open {
		t.Fatal("circuit open after a single rejection")
	}

	// 模型对同一命令仅微调无关参数重试：command/cwd 归一化后指纹一致。
	invocation.CallID = "tool-2"
	invocation.ArgsJSON = []byte(`{"command":"git  push origin main","working_directory":"e:/repo","description":"retry"}`)
	opened, err = service.recordPreDispatchShellRejection(stream, invocation, cause)
	if err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("second identical-fingerprint rejection must open the circuit")
	}
	circuit := currentTurnShellCircuit(stream)
	if !circuit.Open {
		t.Fatal("event-sourced circuit state did not reflect the open")
	}
	if circuit.RejectionClass != "policy" {
		t.Fatalf("rejection class = %q, want policy", circuit.RejectionClass)
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

func TestShellRecoveryStateTransitions(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{
		MessageID: 41, ExecID: "exec-41", ConversationID: "conversation", ToolCallID: "tool-41",
		ExecKind: "shell", ProviderPass: 1, ArgsJSON: []byte(`{"command":"git status"}`),
	}
	stream.PendingExecs[pending.ExecID] = pending
	service := &Service{broker: broker, projector: NewHistoryProjector(), debug: newDebugRecorder("", broker, nil)}

	// none → candidate（skipped 信号）
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	current := stream.PendingExecs[pending.ExecID]
	if current.ShellRecoveryState != shellRecoveryStateCandidate || current.ShellRecoveryReason != shellRecoveryReasonSkipped {
		t.Fatalf("candidate transition state=%d reason=%q", current.ShellRecoveryState, current.ShellRecoveryReason)
	}

	// candidate → none（真实活动复位并递增代次）
	refreshed := service.refreshShellForegroundActivity(stream, current)
	if refreshed.ShellRecoveryState != shellRecoveryStateNone || refreshed.ShellActivityGeneration != current.ShellActivityGeneration+1 {
		t.Fatalf("activity reset state=%d generation=%d", refreshed.ShellRecoveryState, refreshed.ShellActivityGeneration)
	}

	// 陈旧收口调用（活动之后）必须 no-op
	if err := service.recoverShellWithoutTerminal(stream, current, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; !ok {
		t.Fatal("stale recovery closed a refreshed shell")
	}

	// none → abort_requested → 本地超时收口
	current = stream.PendingExecs[pending.ExecID]
	if err := service.requestShellAbortBeforeRecovery(stream, current); err != nil {
		t.Fatal(err)
	}
	current = stream.PendingExecs[pending.ExecID]
	if current.ShellRecoveryState != shellRecoveryStateAbortRequested {
		t.Fatalf("abort transition state=%d", current.ShellRecoveryState)
	}
	if err := service.recoverShellWithoutTerminal(stream, current, shellRecoveryReasonForegroundDeadline); err != nil {
		t.Fatal(err)
	}
	if _, ok := stream.PendingExecs[pending.ExecID]; ok {
		t.Fatal("foreground recovery after abort did not close the exec")
	}
	timeoutResults := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == pending.ToolCallID {
			timeoutResults++
		}
	}
	if timeoutResults != 1 {
		t.Fatalf("timeout tool results = %d, want 1", timeoutResults)
	}
}

func TestShellSupervisionSingleTimerPerExec(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	pending := runtimecore.PendingExec{
		MessageID: 51, ExecID: "exec-51", ConversationID: "conversation", ToolCallID: "tool-51",
		ExecKind: "shell", ProviderPass: 1, ArgsJSON: []byte(`{"command":"git status"}`),
	}
	stream.PendingExecs[pending.ExecID] = pending
	service := &Service{broker: broker, projector: NewHistoryProjector(), debug: newDebugRecorder("", broker, nil)}

	service.scheduleShellForegroundRecovery(stream.RequestID, pending)
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonTransportClosed)

	stream.mu.Lock()
	shellTimers := 0
	for key := range stream.TimerTokens {
		if strings.Contains(key, pending.ExecID) {
			shellTimers++
		}
	}
	stream.mu.Unlock()
	if shellTimers != 1 {
		t.Fatalf("shell supervision timers for one exec = %d, want exactly 1", shellTimers)
	}
}

// TestPendingBridgeCountGatesParallelToolResume 验证并行工具调用的恢复门：
// 同一 pass 的多个 PendingExecs 全部终态前 pendingBridgeCount 保持非零
// （actor 的 providerActionResume 分支据此推迟且只恢复一次 provider）。
func TestPendingBridgeCountGatesParallelToolResume(t *testing.T) {
	stream := &ActiveStream{PendingExecs: map[string]runtimecore.PendingExec{}}
	first := runtimecore.PendingExec{MessageID: 61, ExecID: "exec-61", ToolCallID: "tool-61", ExecKind: "read", ProviderPass: 1}
	second := runtimecore.PendingExec{MessageID: 62, ExecID: "exec-62", ToolCallID: "tool-62", ExecKind: "grep", ProviderPass: 1}
	third := runtimecore.PendingExec{MessageID: 63, ExecID: "exec-63", ToolCallID: "tool-63", ExecKind: "shell", ProviderPass: 1}
	for _, pending := range []runtimecore.PendingExec{first, second, third} {
		stream.PendingExecs[pending.ExecID] = pending
	}
	if count := pendingBridgeCount(stream); count != 3 {
		t.Fatalf("pendingBridgeCount = %d, want 3", count)
	}
	// 乱序完成：后派发的先终态。
	markExecCompleted(stream, third)
	markExecCompleted(stream, first)
	if count := pendingBridgeCount(stream); count != 1 {
		t.Fatalf("pendingBridgeCount after 2 completions = %d, want 1", count)
	}
	// 重复完成同一 exec 不得影响剩余计数。
	markExecCompleted(stream, first)
	if count := pendingBridgeCount(stream); count != 1 {
		t.Fatalf("pendingBridgeCount after duplicate completion = %d, want 1", count)
	}
	markExecCompleted(stream, second)
	if count := pendingBridgeCount(stream); count != 0 {
		t.Fatalf("pendingBridgeCount after all completions = %d, want 0 (provider may resume)", count)
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

func TestSkippedGitAndTasklistSuppressedInUI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
	}{
		{name: "git status", command: "git status --short"},
		{name: "tasklist", command: "tasklist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := NewStreamBroker()
			stream, err := broker.OpenStream("request-"+tc.name, "conversation-"+tc.name, 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
			if err != nil {
				t.Fatal(err)
			}
			toolCallID := "tool-" + strings.ReplaceAll(tc.name, " ", "-")
			stream.CheckpointConversation = &ConversationFile{ConversationID: stream.ConversationID, Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
			pending := runtimecore.PendingExec{
				MessageID: 41, ExecID: "exec-" + toolCallID, ConversationID: stream.ConversationID, ToolCallID: toolCallID, LogicalShellID: toolCallID,
				ExecKind: "shell", ProviderPass: 1, ModelCallID: "model-call", ShellAttempt: shellMaxTransportAttempts,
				ArgsJSON: []byte(fmt.Sprintf(`{"command":%q}`, tc.command)),
			}
			if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
				t.Fatal("shell was unexpectedly queued")
			}
			service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
			service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
			if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
				t.Fatal(err)
			}

			var resultPayload []byte
			for _, entry := range stream.CheckpointConversation.Entries {
				if entry.Kind == "tool_result" && entry.ToolCallID == toolCallID {
					resultPayload = entry.Payload
				}
			}
			if len(resultPayload) == 0 {
				t.Fatal("missing tool_result for silent inspect skip")
			}
			if strings.Contains(string(resultPayload), `"rejected"`) {
				t.Fatalf("inspect skip still uses rejected UI payload: %s", resultPayload)
			}

			// checkpoint 投影应剥离 silent inspect Shell，避免 reconnect 再刷 Skipped。
			state, err := service.projector.ProjectLegacyCheckpoint(stream.CheckpointConversation)
			if err != nil {
				t.Fatal(err)
			}
			for _, rawTurn := range state.GetTurns() {
				turn := &agentv1.ConversationTurnStructure{}
				if err := proto.Unmarshal(rawTurn, turn); err != nil {
					t.Fatal(err)
				}
				agentTurn := turn.GetAgentConversationTurn()
				if agentTurn == nil {
					continue
				}
				for _, rawStep := range agentTurn.GetSteps() {
					step := &agentv1.ConversationStep{}
					if err := proto.Unmarshal(rawStep, step); err != nil {
						t.Fatal(err)
					}
					if shell := step.GetToolCall().GetShellToolCall(); shell != nil {
						t.Fatalf("silent inspect shell leaked into checkpoint: %#v", shell)
					}
				}
			}
		})
	}
}

func TestCheckpointDoesNotMistakeBackgroundShellWithoutIDForSilentSkip(t *testing.T) {
	isBackground := true
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{Command: "git status --short"},
				Result: &agentv1.ShellResult{
					IsBackground: &isBackground,
					Result: &agentv1.ShellResult_Success{
						Success: &agentv1.ShellSuccess{Command: "git status --short"},
					},
				},
			},
		},
	}
	if isSilentInspectShellSkippedToolCall(toolCall) {
		t.Fatal("real background shell with nil shell_id was mistaken for synthetic silent skip")
	}
	shellID := uint32(0)
	toolCall.GetShellToolCall().GetResult().GetSuccess().ShellId = &shellID
	if !isSilentInspectShellSkippedToolCall(toolCall) {
		t.Fatal("explicit shell_id=0 sentinel was not recognized as synthetic silent skip")
	}
}

func TestSilentInspectCheckpointSanitizerUsesRecoveryMetadataScope(t *testing.T) {
	const toolCallID = "reused-tool-id"
	synthetic := execbridge.BuildShellSkippedBackgroundedToolCall(
		toolCallID,
		[]byte(`{"command":"git status --short"}`),
		"Skipped by Cursor",
	)
	toolCallJSON, err := protojson.Marshal(synthetic)
	if err != nil {
		t.Fatal(err)
	}
	resultPayload, err := json.Marshal(toolResultEntryPayload{
		ToolCallID: toolCallID,
		ToolName:   "Shell",
		ToolCall:   toolCallJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	toolCallPayload, err := json.Marshal(toolCallEntryPayload{
		ToolCallID: toolCallID,
		ToolName:   "Shell",
		ToolCall:   toolCallJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := []HistoryEntry{
		{TurnSeq: 1, RequestID: "request-1", Kind: "tool_call", ToolCallID: toolCallID, Payload: toolCallPayload},
		{TurnSeq: 1, RequestID: "request-1", Kind: "tool_result", ToolCallID: toolCallID, Payload: resultPayload},
		newMetadataEntry(1, "request-1", "shell_stream_recovered", map[string]any{
			"tool_call_id":        toolCallID,
			"terminal_owner":      "silent_inspect_skip",
			"silent_inspect_skip": true,
		}),
		// The same tool ID in another request/turn is a real record and must survive.
		{TurnSeq: 2, RequestID: "request-2", Kind: "tool_call", ToolCallID: toolCallID, Payload: toolCallPayload},
		{TurnSeq: 2, RequestID: "request-2", Kind: "tool_result", ToolCallID: toolCallID, Payload: resultPayload},
		// A rejected "skip" without the explicit recovery marker is historical data,
		// not evidence that the sanitizer may delete it.
		{TurnSeq: 3, RequestID: "request-3", Kind: "tool_result", ToolCallID: toolCallID, Payload: resultPayload},
	}

	filtered := sanitizeSkippedGitTasklistEntries(entries)
	for _, entry := range filtered {
		if entry.TurnSeq == 1 && entry.RequestID == "request-1" &&
			(entry.Kind == "tool_call" || entry.Kind == "tool_result") {
			t.Fatalf("metadata-authorized synthetic entry survived: %#v", entry)
		}
	}
	if got := len(filtered); got != 4 {
		t.Fatalf("filtered entry count=%d, want 4 (metadata plus unrelated records)", got)
	}
	for _, entry := range filtered {
		if entry.TurnSeq == 2 && entry.RequestID == "request-2" && entry.ToolCallID != toolCallID {
			t.Fatalf("unexpected unrelated entry: %#v", entry)
		}
	}
}

func TestNewCursorSkippedRecoveryKeepsMutatingRejected(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	pending := runtimecore.PendingExec{
		MessageID: 55, ExecID: "exec-mut", ConversationID: "conversation", ToolCallID: "tool-mut", LogicalShellID: "tool-mut",
		ExecKind: "shell", ProviderPass: 1, ModelCallID: "model-call", ShellAttempt: 1,
		ArgsJSON: []byte(`{"command":"git commit -m test"}`),
	}
	if !reserveForegroundShellDispatch(stream, &agentv1.AgentServerMessage{}, pending) {
		t.Fatal("mutating shell was unexpectedly queued")
	}
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonSkipped)
	if err := service.recoverShellWithoutTerminalIfNeeded(stream, pending.ExecID, pending.MessageID, shellRecoveryReasonSkipped); err != nil {
		t.Fatal(err)
	}
	foundRejected := false
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == pending.ToolCallID {
			if strings.Contains(string(entry.Payload), `"rejected"`) || strings.Contains(string(entry.Payload), "status is unknown") {
				foundRejected = true
			}
		}
	}
	if !foundRejected {
		t.Fatal("mutating skipped shell should remain an explicit rejected/unknown terminal result")
	}
}
