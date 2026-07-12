package forwarder

import (
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func observeBackgroundSubagentAction(stream *ActiveStream, message *agentv1.AgentClientMessage) (string, bool) {
	if stream == nil || message == nil || message.GetConversationAction() == nil {
		return "", false
	}
	item, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_BackgroundSubagentAction)
	if !ok || item.BackgroundSubagentAction == nil {
		return "", false
	}
	toolCallID := strings.TrimSpace(item.BackgroundSubagentAction.GetToolCallId())
	if toolCallID == "" {
		return "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pending, found := findSubagentPendingLocked(stream, toolCallID)
	if !found {
		return toolCallID, false
	}
	wasNew := pending.StreamState != "backgrounded"
	pending.StreamState = "backgrounded"
	stream.PendingExecs[pending.ExecID] = pending
	stream.UpdatedAt = time.Now().UTC()
	return toolCallID, wasNew
}

func observeBackgroundSubagentCompletions(stream *ActiveStream, message *agentv1.AgentClientMessage) []runtimecore.PendingExec {
	if stream == nil || message == nil || message.GetConversationAction() == nil {
		return nil
	}
	item, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pendingCompletions := make([]runtimecore.PendingExec, 0, len(item.BackgroundTaskCompletionAction.GetCompletions()))
	for _, completion := range item.BackgroundTaskCompletionAction.GetCompletions() {
		if completion == nil || completion.GetKind() != agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT {
			continue
		}
		pending, found := findSubagentPendingLocked(stream, completion.GetTaskId())
		if !found {
			continue
		}
		if completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS {
			pending.StreamState = "backgrounded"
			stream.PendingExecs[pending.ExecID] = pending
			stream.UpdatedAt = time.Now().UTC()
			continue
		}
		switch completion.GetStatus() {
		case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS:
			pending.StreamState = "completed_without_result"
		case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR:
			pending.StreamState = "failed_without_result"
		case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED:
			pending.StreamState = "aborted_without_result"
		default:
			continue
		}
		stream.PendingExecs[pending.ExecID] = pending
		pendingCompletions = append(pendingCompletions, pending)
	}
	if len(pendingCompletions) > 0 {
		stream.UpdatedAt = time.Now().UTC()
	}
	return pendingCompletions
}

func findSubagentPendingLocked(stream *ActiveStream, identifier string) (runtimecore.PendingExec, bool) {
	identifier = strings.TrimSpace(identifier)
	if stream == nil || identifier == "" {
		return runtimecore.PendingExec{}, false
	}
	for _, pending := range stream.PendingExecs {
		if strings.TrimSpace(pending.ExecKind) != "subagent" {
			continue
		}
		if strings.TrimSpace(pending.ToolCallID) == identifier || strings.TrimSpace(pending.ExecID) == identifier {
			return pending, true
		}
	}
	return runtimecore.PendingExec{}, false
}

func (service *Service) scheduleSubagentResultTimeout(requestID string, pending runtimecore.PendingExec, delay time.Duration, reason string) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "subagent" || delay <= 0 {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerSubagentResult, pending.ExecID),
		delay,
		streamTimerSubagentResult,
		pending.ExecID,
		pending.MessageID,
		strings.TrimSpace(reason),
	)
}

func (service *Service) recoverSubagentWithoutResult(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if stream == nil {
		return nil
	}
	markExecCompleted(stream, pending)
	reason = firstNonEmpty(strings.TrimSpace(reason), "subagent result was not returned")
	resultPayload := fmt.Sprintf("Task failed: %s", reason)
	if err := service.appendToolResult(stream, pending.ToolCallID, "Task", pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
		return err
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "subagent_result_missing", map[string]any{
			"tool_call_id": pending.ToolCallID,
			"message_id":   pending.MessageID,
			"exec_id":      pending.ExecID,
			"stream_state": pending.StreamState,
			"reason":       reason,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}
