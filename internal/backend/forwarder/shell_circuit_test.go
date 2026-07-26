package forwarder

import (
	"fmt"
	"strings"
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
