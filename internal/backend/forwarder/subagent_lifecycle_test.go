package forwarder

import (
	"testing"
	"time"

	"cursor/gen/agentv1"
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

func TestBackgroundSubagentTerminalStateRejectsLaterProgress(t *testing.T) {
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
		t.Fatalf("unexpected terminal observation counts: progresses=%d completions=%d", len(progresses), len(completions))
	}
	terminal := stream.PendingExecs[pending.ExecID]
	leaseDeadline := terminal.SubagentLeaseDeadline

	progresses, completions = observeBackgroundSubagentCompletions(stream, backgroundSubagentMessage(
		pending.ExecID,
		agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT,
		agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED,
		agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS,
	))
	if len(progresses) != 0 || len(completions) != 0 {
		t.Fatalf("terminal task accepted later progress: progresses=%d completions=%d", len(progresses), len(completions))
	}
	if !stream.PendingExecs[pending.ExecID].SubagentLeaseDeadline.Equal(leaseDeadline) {
		t.Fatal("terminal task lease was renewed")
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
