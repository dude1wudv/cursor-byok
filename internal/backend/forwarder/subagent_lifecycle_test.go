package forwarder

import (
	"encoding/json"
	"testing"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestInitializePendingSubagentLease(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	pending := initializePendingSubagentLease(runtimecore.PendingExec{ExecKind: "subagent"}, now)

	if !pending.OpenedAt.Equal(now) || !pending.LastSubagentProgressAt.Equal(now) {
		t.Fatalf("unexpected initial activity timestamps: opened=%s progress=%s", pending.OpenedAt, pending.LastSubagentProgressAt)
	}
	if !pending.SubagentLeaseDeadline.Equal(now.Add(subagentInactivityTimeout)) {
		t.Fatalf("unexpected lease deadline: %s", pending.SubagentLeaseDeadline)
	}
	if !pending.SubagentHardDeadline.Equal(now.Add(subagentMaximumRuntime)) {
		t.Fatalf("unexpected hard deadline: %s", pending.SubagentHardDeadline)
	}
}

func TestSubagentTimeoutDelayPrefersHardDeadline(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	pending := runtimecore.PendingExec{
		ExecKind:               "subagent",
		SubagentLeaseDeadline:  now.Add(8 * time.Minute),
		SubagentHardDeadline:   now.Add(3 * time.Minute),
		LastSubagentProgressAt: now,
	}

	delay, reason := subagentTimeoutDelayAndReason(pending, now)
	if delay != 3*time.Minute || reason != subagentTimeoutReasonMaximumRuntime {
		t.Fatalf("unexpected timeout decision: delay=%s reason=%q", delay, reason)
	}
}

func TestSubagentTimeoutAndMissingResultRemainNonTerminal(t *testing.T) {
	pending := runtimecore.PendingExec{MessageID: 41, ExecID: "exec-timeout", ToolCallID: "tool-timeout", ExecKind: "subagent", ProviderPass: 3}
	stream := &ActiveStream{
		RequestID:              "request",
		ConversationID:         "conversation",
		TurnSeq:                1,
		PendingExecs:           map[string]runtimecore.PendingExec{pending.ExecID: pending},
		CheckpointConversation: &ConversationFile{ConversationID: "conversation", Mode: "agent"},
	}
	service := &Service{}
	if err := service.recoverSubagentStillRunning(stream, pending, subagentTimeoutReasonInactivityLease); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSubagentWithoutResult(stream, pending, "transport closed"); err != nil {
		t.Fatal(err)
	}
	if _, found := stream.PendingExecs[pending.ExecID]; !found {
		t.Fatal("timeout or missing-result hint removed pending subagent")
	}
	unknown := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" {
			t.Fatal("timeout or missing-result hint wrote final tool result")
		}
		if entry.Kind != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) == nil && payload.Type == "subagent_result_unknown" {
			unknown++
			if terminal, _ := payload.Value["terminal"].(bool); terminal {
				t.Fatal("unknown-result metadata was terminal")
			}
		}
	}
	if unknown != 2 {
		t.Fatalf("unknown-result metadata count=%d, want 2", unknown)
	}
}

func TestSubagentTransportCloseAllowsLateSuccess(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-close", "conversation-close", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	pending := runtimecore.PendingExec{
		MessageID:    42,
		ExecID:       "exec-close",
		ToolCallID:   "tool-close",
		ExecKind:     "subagent",
		ProviderPass: 1,
		ModelCallID:  "model-call",
		ArgsJSON:     []byte(`{"description":"inspect","prompt":"check"}`),
	}
	stream.CheckpointConversation = &ConversationFile{ConversationID: stream.ConversationID, Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 1}
	stream.PendingExecs[pending.ExecID] = pending
	stream.SubagentFinalizations = make(map[string]*SubagentFinalizationState)
	registerTaskBatchMember(stream, pending)
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	closeMessage := &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_StreamClose{
		StreamClose: &agentv1.ExecClientStreamClose{Id: pending.MessageID},
	}}
	if err := service.handleExecControl(InboundIntent{Kind: "exec_control", RequestID: stream.RequestID, ExecClientControlMessage: closeMessage}); err != nil {
		t.Fatal(err)
	}
	if _, found := stream.PendingExecs[pending.ExecID]; !found {
		t.Fatal("transport close removed pending subagent")
	}
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" {
			t.Fatal("transport close wrote final tool result")
		}
	}
	final := &agentv1.ExecClientMessage{
		Id: pending.MessageID, ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentResult{SubagentResult: &agentv1.SubagentResult{
			Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{AgentId: "agent-close", FinalMessage: stringPtr("done")}},
		}},
	}
	if err := service.handleExecResult(InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: final}); err != nil {
		t.Fatal(err)
	}
	if got := projectSubagentRunStates(stream.CheckpointConversation.Entries)[pending.ToolCallID]; got == nil || got.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS {
		t.Fatalf("late result after transport close = %#v", got)
	}
}

func TestBackgroundSubagentProgressRenewsLeaseWithoutExtendingHardDeadline(t *testing.T) {
	now := time.Now().UTC()
	pending := initializePendingSubagentLease(runtimecore.PendingExec{
		MessageID:  7,
		ExecID:     "exec-1",
		ToolCallID: "tool-1",
		ExecKind:   "subagent",
		OpenedAt:   now.Add(-5 * time.Minute),
	}, now.Add(-5*time.Minute))
	hardDeadline := pending.SubagentHardDeadline
	stream := &ActiveStream{PendingExecs: map[string]runtimecore.PendingExec{pending.ExecID: pending}}

	progresses, completions := observeBackgroundSubagentCompletions(stream, backgroundSubagentMessage(
		pending.ToolCallID,
		agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED,
		agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS,
	))
	if len(progresses) != 1 || len(completions) != 0 {
		t.Fatalf("unexpected observation counts: progresses=%d completions=%d", len(progresses), len(completions))
	}
	updated := stream.PendingExecs[pending.ExecID]
	if updated.StreamState != "backgrounded" {
		t.Fatalf("unexpected stream state: %q", updated.StreamState)
	}
	if !updated.SubagentHardDeadline.Equal(hardDeadline) {
		t.Fatalf("progress extended hard deadline: got=%s want=%s", updated.SubagentHardDeadline, hardDeadline)
	}
	remaining := time.Until(updated.SubagentLeaseDeadline)
	if remaining < subagentInactivityTimeout-time.Second || remaining > subagentInactivityTimeout+time.Second {
		t.Fatalf("progress did not renew inactivity lease: remaining=%s", remaining)
	}
}

func TestBackgroundSubagentCompletionHintAllowsLateProgress(t *testing.T) {
	now := time.Now().UTC()
	pending := initializePendingSubagentLease(runtimecore.PendingExec{
		ExecID:     "exec-2",
		ToolCallID: "tool-2",
		ExecKind:   "subagent",
	}, now)
	stream := &ActiveStream{PendingExecs: map[string]runtimecore.PendingExec{pending.ExecID: pending}}

	progresses, completions := observeBackgroundSubagentCompletions(stream, backgroundSubagentMessage(
		pending.ExecID,
		agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
		agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_FINISHED,
	))
	if len(progresses) != 0 || len(completions) != 1 {
		t.Fatalf("unexpected hint observation counts: progresses=%d completions=%d", len(progresses), len(completions))
	}
	if stream.PendingExecs[pending.ExecID].StreamState != pending.StreamState {
		t.Fatal("completion hint changed authoritative subagent state")
	}

	progresses, completions = observeBackgroundSubagentCompletions(stream, backgroundSubagentMessage(
		pending.ExecID,
		agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED,
		agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS,
	))
	if len(progresses) != 1 || len(completions) != 0 {
		t.Fatalf("completion hint blocked later progress: progresses=%d completions=%d", len(progresses), len(completions))
	}
	if stream.PendingExecs[pending.ExecID].StreamState != "backgrounded" {
		t.Fatal("late progress did not restore backgrounded state")
	}
}

func TestBackgroundTaskSignalsDoNotRenewUnmatchedSubagent(t *testing.T) {
	now := time.Now().UTC()
	pending := initializePendingSubagentLease(runtimecore.PendingExec{
		ExecID:     "exec-3",
		ToolCallID: "tool-3",
		ExecKind:   "subagent",
	}, now)
	stream := &ActiveStream{PendingExecs: map[string]runtimecore.PendingExec{pending.ExecID: pending}}

	for _, message := range []*agentv1.AgentClientMessage{
		backgroundSubagentMessage("other-task", agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED, agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS),
		backgroundSubagentMessage(pending.ExecID, agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED, agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS),
	} {
		progresses, completions := observeBackgroundSubagentCompletions(stream, message)
		if len(progresses) != 0 || len(completions) != 0 {
			t.Fatalf("untrusted signal changed subagent state: progresses=%d completions=%d", len(progresses), len(completions))
		}
	}
	if !stream.PendingExecs[pending.ExecID].SubagentLeaseDeadline.Equal(pending.SubagentLeaseDeadline) {
		t.Fatal("untrusted signal renewed subagent lease")
	}
}

func TestProjectSubagentRunStatesKeepsNewestGeneration(t *testing.T) {
	pendingOld := runtimecore.PendingExec{MessageID: 1, ExecID: "old", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 1}
	pendingNew := runtimecore.PendingExec{MessageID: 2, ExecID: "new", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 2}
	entries := []HistoryEntry{
		subagentRunStateMetadataEntry(1, "request", pendingOld, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ABORTED, "old-agent", ""),
		subagentRunStateMetadataEntry(1, "request", pendingNew, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_RUNNING, "new-agent", ""),
		subagentRunStateMetadataEntry(1, "request", pendingOld, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR, "old-agent", "late old result"),
		subagentRunStateMetadataEntry(1, "request", pendingNew, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED, "new-agent", ""),
	}
	state := projectSubagentRunStates(entries)["tool"]
	if state == nil || state.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED || state.GetSubagentId() != "new-agent" {
		t.Fatalf("projected state = %#v, want newest generation backgrounded", state)
	}
}

func TestProjectSubagentRunStatesDoesNotDowngradeTerminalSameGeneration(t *testing.T) {
	pending := runtimecore.PendingExec{MessageID: 3, ExecID: "exec", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 4}
	entries := []HistoryEntry{
		subagentRunStateMetadataEntry(1, "request", pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_RUNNING, "agent", ""),
		subagentRunStateMetadataEntry(1, "request", pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS, "agent", "done"),
		subagentRunStateMetadataEntry(1, "request", pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED, "agent", "late progress"),
		subagentRunStateMetadataEntry(1, "request", pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR, "agent", "duplicate terminal"),
	}
	state := projectSubagentRunStates(entries)[pending.ToolCallID]
	if state == nil || state.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS || state.GetDetail() != "done" {
		t.Fatalf("projected state = %#v, want first terminal success", state)
	}
}

func TestBackgroundAckDetachesWithoutTerminalAndLateResultFinalizesOnce(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	pending := runtimecore.PendingExec{
		MessageID:    17,
		ExecID:       "exec-subagent",
		ToolCallID:   "tool-subagent",
		ExecKind:     "subagent",
		ProviderPass: 1,
		ModelCallID:  "model-call",
		ArgsJSON:     []byte(`{"description":"inspect","prompt":"check"}`),
	}
	stream.CheckpointConversation = &ConversationFile{
		ConversationID: "conversation",
		Mode:           "agent",
		NextTurnSeq:    2,
		NextEntrySeq:   1,
	}
	stream.PendingExecs[pending.ExecID] = pending
	stream.SubagentFinalizations = make(map[string]*SubagentFinalizationState)
	registerTaskBatchMember(stream, pending)
	service := &Service{
		broker:     broker,
		projector:  NewHistoryProjector(),
		execBridge: execbridge.NewBridge(),
		debug:      newDebugRecorder("", broker, nil),
	}
	ack := &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentResult{SubagentResult: &agentv1.SubagentResult{
			Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{
				AgentId:          "agent-17",
				BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_USER_REQUEST,
			}},
		}},
	}
	if err := service.acceptBackgroundSubagentAck(stream, pending, ack); err != nil {
		t.Fatal(err)
	}
	if _, found := stream.PendingExecs[pending.ExecID]; found {
		t.Fatal("background ack remained in blocking pending execs")
	}
	state := subagentFinalizationSnapshot(stream, pending)
	if !state.BackgroundAcknowledged || state.ResultReceived || state.ToolResultPersisted || state.DispatchClosed || state.ToolCompletedPublished {
		t.Fatalf("background ack created terminal finalization state: %#v", state)
	}
	batch := stream.TaskBatches[pending.ProviderPass]
	member := batch.Members[pending.ToolCallID]
	if member == nil || !member.ParentReleased || member.Terminal {
		t.Fatalf("background ack batch state = %#v, want parent released but non-terminal", member)
	}
	if got := projectSubagentRunStates(stream.CheckpointConversation.Entries)[pending.ToolCallID]; got == nil || got.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED {
		t.Fatalf("background checkpoint state = %#v", got)
	}
	checkpoint, err := service.projector.ProjectLegacyCheckpoint(stream.CheckpointConversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.GetSubagentStateRefs()) != 0 {
		t.Fatalf("checkpoint fabricated subagent state refs: %#v", checkpoint.GetSubagentStateRefs())
	}
	assertNoSubagentTerminalArtifacts(t, stream)
	progresses, completions := observeBackgroundSubagentCompletions(stream, backgroundSubagentMessage(
		pending.ToolCallID,
		agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED,
		agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS,
	))
	if len(progresses) != 1 || len(completions) != 0 {
		t.Fatalf("detached progress counts progresses=%d completions=%d", len(progresses), len(completions))
	}
	if _, found := stream.PendingExecs[pending.ExecID]; found {
		t.Fatal("detached progress reattached blocking pending exec")
	}
	if subagentFinalizationSnapshot(stream, pending).Pending.StreamState != "backgrounded" {
		t.Fatal("detached progress did not update retained correlation")
	}
	if err := service.handleExecControl(InboundIntent{
		Kind:      "exec_control",
		RequestID: stream.RequestID,
		ExecClientControlMessage: &agentv1.ExecClientControlMessage{Message: &agentv1.ExecClientControlMessage_StreamClose{
			StreamClose: &agentv1.ExecClientStreamClose{Id: pending.MessageID},
		}},
	}); err != nil {
		t.Fatalf("detached background stream close was not ignored: %v", err)
	}

	final := &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentResult{SubagentResult: &agentv1.SubagentResult{
			Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{
				AgentId:      "agent-17",
				FinalMessage: stringPtr("done"),
			}},
		}},
	}
	intent := InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: final}
	if err := service.handleExecResult(intent); err != nil {
		t.Fatal(err)
	}
	if err := service.handleExecResult(intent); err != nil {
		t.Fatal(err)
	}
	state = subagentFinalizationSnapshot(stream, pending)
	if !state.ResultReceived || !state.ToolResultPersisted || !state.DispatchClosed || !state.ToolCompletedPublished || !state.TaskBatchTerminal {
		t.Fatalf("late result did not finalize: %#v", state)
	}
	if got := projectSubagentRunStates(stream.CheckpointConversation.Entries)[pending.ToolCallID]; got == nil || got.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS {
		t.Fatalf("final checkpoint state = %#v", got)
	}
	toolResults := 0
	closures := 0
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == pending.ToolCallID {
			toolResults++
		}
		if entry.Kind != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) == nil && payload.Type == subagentDispatchClosed {
			closures++
		}
	}
	if toolResults != 1 || closures != 1 {
		t.Fatalf("finalization artifacts tool_results=%d closures=%d, want exactly once", toolResults, closures)
	}
	completedUpdates := 0
	for _, event := range stream.Backlog {
		if update := event.Message.GetInteractionUpdate(); update != nil {
			if _, ok := update.GetMessage().(*agentv1.InteractionUpdate_ToolCallCompleted); ok {
				completedUpdates++
			}
		}
	}
	if completedUpdates != 1 {
		t.Fatalf("ToolCallCompleted count=%d, want 1", completedUpdates)
	}
	if batch := stream.TaskBatches[pending.ProviderPass]; batch == nil || !batch.ParentNotified {
		t.Fatalf("parent flow was not notified after final subagent result: %#v", batch)
	}
}

func TestExplicitCancelFinalizesDetachedSubagentAsAborted(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-cancel", "conversation-cancel", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	pending := runtimecore.PendingExec{
		MessageID:        43,
		ExecID:           "exec-cancel",
		ToolCallID:       "tool-cancel",
		ExecKind:         "subagent",
		ProviderPass:     1,
		ModelCallID:      "model-call",
		SubagentResumeID: "agent-cancel",
		ArgsJSON:         []byte(`{"description":"inspect","prompt":"check"}`),
	}
	stream.CheckpointConversation = &ConversationFile{
		ConversationID: stream.ConversationID,
		Mode:           "agent",
		NextTurnSeq:    2,
		NextEntrySeq:   1,
		Entries: []HistoryEntry{
			subagentRunStateMetadataEntry(1, stream.RequestID, pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED, pending.SubagentResumeID, ""),
		},
	}
	stream.SubagentFinalizations = map[string]*SubagentFinalizationState{
		subagentFinalizationKey(pending): {Pending: pending, BackgroundAcknowledged: true},
	}
	stream.TaskBatches = make(map[int]*TaskBatch)
	registerTaskBatchMember(stream, pending)
	releaseTaskBatchParentWait(stream, pending)
	service := &Service{broker: broker, projector: NewHistoryProjector(), execBridge: execbridge.NewBridge(), debug: newDebugRecorder("", broker, nil)}
	cancelMessage := &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_ConversationAction{
		ConversationAction: &agentv1.ConversationAction{Action: &agentv1.ConversationAction_CancelSubagentAction{
			CancelSubagentAction: &agentv1.CancelSubagentAction{SubagentId: pending.SubagentResumeID},
		}},
	}}
	if err := service.handleMetadataIntent(InboundIntent{Kind: "metadata", RequestID: stream.RequestID, ClientMessage: cancelMessage}); err != nil {
		t.Fatal(err)
	}
	state := subagentFinalizationSnapshot(stream, pending)
	if !state.ExplicitlyCanceled || !state.ReconcileRequested {
		t.Fatalf("explicit cancellation state = %#v", state)
	}
	if got := projectSubagentRunStates(stream.CheckpointConversation.Entries)[pending.ToolCallID]; got == nil || got.GetStatus() != agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ABORTED {
		t.Fatalf("explicit cancellation projection = %#v", got)
	}
	before := len(stream.CheckpointConversation.Entries)
	late := &agentv1.ExecClientMessage{
		Id: pending.MessageID, ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentResult{SubagentResult: &agentv1.SubagentResult{
			Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{AgentId: pending.SubagentResumeID, FinalMessage: stringPtr("late success")}},
		}},
	}
	if err := service.handleExecResult(InboundIntent{Kind: "exec_result", RequestID: stream.RequestID, ExecClientMessage: late}); err != nil {
		t.Fatal(err)
	}
	if len(stream.CheckpointConversation.Entries) != before {
		t.Fatal("late result mutated explicitly canceled subagent history")
	}
}

func TestExplicitCancelMatchesDetachedCurrentSubagentOnly(t *testing.T) {
	old := runtimecore.PendingExec{MessageID: 1, ExecID: "old", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 1, SubagentResumeID: "agent-old"}
	current := runtimecore.PendingExec{MessageID: 2, ExecID: "current", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 2, SubagentResumeID: "agent-current"}
	stream := &ActiveStream{
		PendingExecs: map[string]runtimecore.PendingExec{},
		SubagentFinalizations: map[string]*SubagentFinalizationState{
			subagentFinalizationKey(old):     {Pending: old, BackgroundAcknowledged: true},
			subagentFinalizationKey(current): {Pending: current, BackgroundAcknowledged: true},
		},
	}
	cancel := func(identifier string) *agentv1.AgentClientMessage {
		return &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{Action: &agentv1.ConversationAction_CancelSubagentAction{
				CancelSubagentAction: &agentv1.CancelSubagentAction{SubagentId: identifier},
			}},
		}}
	}
	if _, found := observeExplicitSubagentCancellation(stream, cancel(old.SubagentResumeID)); found {
		t.Fatal("explicit cancel matched stale subagent generation")
	}
	matched, found := observeExplicitSubagentCancellation(stream, cancel(current.SubagentResumeID))
	if !found || matched.ExecID != current.ExecID {
		t.Fatalf("explicit cancel matched %#v, found=%t", matched, found)
	}
}

func TestOldGenerationCannotMatchDetachedSubagentFinalization(t *testing.T) {
	old := runtimecore.PendingExec{MessageID: 1, ExecID: "old", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 1}
	current := runtimecore.PendingExec{MessageID: 2, ExecID: "current", ToolCallID: "tool", ExecKind: "subagent", ProviderPass: 2}
	stream := &ActiveStream{
		PendingExecs: map[string]runtimecore.PendingExec{current.ExecID: current},
		SubagentFinalizations: map[string]*SubagentFinalizationState{
			subagentFinalizationKey(old): {Pending: old, BackgroundAcknowledged: true},
		},
	}
	if _, found := selectPendingSubagentFinalization(old.ExecID, old.MessageID, stream); found {
		t.Fatal("old generation matched detached finalization while newer generation was active")
	}
	if subagentGenerationIsCurrent(stream, old) {
		t.Fatal("old generation was considered current")
	}
}

func assertNoSubagentTerminalArtifacts(t *testing.T, stream *ActiveStream) {
	t.Helper()
	for _, entry := range stream.CheckpointConversation.Entries {
		if entry.Kind == "tool_result" {
			t.Fatalf("background ack wrote final tool_result: %#v", entry)
		}
		if entry.Kind != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) == nil && payload.Type == subagentDispatchClosed {
			t.Fatalf("background ack closed dispatch: %#v", entry)
		}
	}
	for _, event := range stream.Backlog {
		if update := event.Message.GetInteractionUpdate(); update != nil {
			if _, ok := update.GetMessage().(*agentv1.InteractionUpdate_ToolCallCompleted); ok {
				t.Fatal("background ack published ToolCallCompleted")
			}
		}
	}
}

func TestDetachedSubagentFinalizationRequiresMatchingIDs(t *testing.T) {
	pending := runtimecore.PendingExec{MessageID: 51, ExecID: "exec-51", ToolCallID: "tool-51", ExecKind: "subagent", ProviderPass: 5}
	stream := &ActiveStream{
		PendingExecs: map[string]runtimecore.PendingExec{},
		SubagentFinalizations: map[string]*SubagentFinalizationState{
			subagentFinalizationKey(pending): {Pending: pending, BackgroundAcknowledged: true},
		},
	}
	if _, found := selectPendingSubagentFinalization(pending.ExecID, pending.MessageID+1, stream); found {
		t.Fatal("wrong message_id matched detached finalization")
	}
	if _, found := selectPendingSubagentFinalization("other-exec", pending.MessageID, stream); found {
		t.Fatal("wrong exec_id matched detached finalization")
	}
	if matched, found := selectPendingSubagentFinalization(pending.ExecID, pending.MessageID, stream); !found || matched.ToolCallID != pending.ToolCallID {
		t.Fatalf("matching IDs returned %#v, found=%t", matched, found)
	}
}

func TestBackgroundAckIsNotFinalResult(t *testing.T) {
	ack := &agentv1.SubagentResult{Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{
		AgentId:          "agent-1",
		BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_USER_REQUEST,
	}}}
	if !isBackgroundSubagentAck(ack) {
		t.Fatal("background ack was not recognized")
	}
	ack.GetSuccess().FinalMessage = stringPtr("done")
	if isBackgroundSubagentAck(ack) {
		t.Fatal("real final message was treated as background ack")
	}
}

func backgroundSubagentMessage(taskID string, kind agentv1.BackgroundTaskKind, status agentv1.BackgroundTaskStatus, reason agentv1.BackgroundTaskCompletionReason) *agentv1.AgentClientMessage {
	return &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_ConversationAction{
			ConversationAction: &agentv1.ConversationAction{
				Action: &agentv1.ConversationAction_BackgroundTaskCompletionAction{
					BackgroundTaskCompletionAction: &agentv1.BackgroundTaskCompletionAction{
						Completions: []*agentv1.BackgroundTaskCompletion{{
							TaskId: taskID,
							Kind:   kind,
							Status: status,
							Reason: reason,
						}},
					},
				},
			},
		},
	}
}
