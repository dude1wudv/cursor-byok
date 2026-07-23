package forwarder

import (
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func reserveForegroundShellDispatch(stream *ActiveStream, message *agentv1.AgentServerMessage, pending runtimecore.PendingExec) bool {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ActiveForegroundShellExecID == "" {
		stream.ActiveForegroundShellExecID = pending.ExecID
		return true
	}
	stream.QueuedForegroundShells = append(stream.QueuedForegroundShells, queuedShellDispatch{Message: message, Pending: pending})
	stream.UpdatedAt = time.Now().UTC()
	return false
}

func releaseForegroundShellDispatch(stream *ActiveStream, completed runtimecore.PendingExec) (queuedShellDispatch, bool) {
	if stream == nil || strings.TrimSpace(completed.ExecKind) != "shell" {
		return queuedShellDispatch{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ActiveForegroundShellExecID != completed.ExecID {
		return queuedShellDispatch{}, false
	}
	stream.ActiveForegroundShellExecID = ""
	if len(stream.QueuedForegroundShells) == 0 {
		return queuedShellDispatch{}, false
	}
	next := stream.QueuedForegroundShells[0]
	stream.QueuedForegroundShells = append([]queuedShellDispatch(nil), stream.QueuedForegroundShells[1:]...)
	now := time.Now().UTC()
	next.Pending.OpenedAt = now
	next.Pending.LastShellActivityAt = now
	next.Pending.ShellForegroundDeadline = now.Add(shellForegroundTimeoutDuration(next.Pending.ArgsJSON) + shellTerminalRecoveryGrace)
	next.Pending.ShellRecoveryScheduled = false
	if stream.PendingExecs == nil {
		stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	}
	stream.PendingExecs[next.Pending.ExecID] = next.Pending
	stream.ActiveForegroundShellExecID = next.Pending.ExecID
	stream.UpdatedAt = now
	return next, true
}

func drainForegroundShellQueue(stream *ActiveStream, completed runtimecore.PendingExec) []queuedShellDispatch {
	if stream == nil || strings.TrimSpace(completed.ExecKind) != "shell" {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ActiveForegroundShellExecID != completed.ExecID {
		return nil
	}
	stream.ActiveForegroundShellExecID = ""
	queued := append([]queuedShellDispatch(nil), stream.QueuedForegroundShells...)
	stream.QueuedForegroundShells = nil
	stream.UpdatedAt = time.Now().UTC()
	return queued
}

func (service *Service) dispatchOrQueueForegroundShell(stream *ActiveStream, message *agentv1.AgentServerMessage, pending runtimecore.PendingExec) (bool, error) {
	if reserveForegroundShellDispatch(stream, message, pending) {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: message}); err != nil {
			drainForegroundShellQueue(stream, pending)
			return false, err
		}
		service.scheduleShellForegroundRecovery(stream.RequestID, pending)
		return true, nil
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_dispatch_queued", map[string]any{
			"tool_call_id":  pending.ToolCallID,
			"exec_id":       pending.ExecID,
			"message_id":    pending.MessageID,
			"provider_pass": pending.ProviderPass,
		}),
	})
	return false, err
}

func (service *Service) advanceForegroundShellQueue(stream *ActiveStream, completed runtimecore.PendingExec) error {
	if strings.TrimSpace(completed.ExecKind) != "shell" {
		return nil
	}
	circuit := currentTurnShellCircuit(stream)
	if circuit.Open {
		queued := drainForegroundShellQueue(stream, completed)
		for _, item := range queued {
			pending := item.Pending
			markExecCompleted(stream, pending)
			result := fmt.Sprintf("Shell blocked locally after Cursor rejected the preceding command (%s). This command was not executed.", firstNonEmpty(circuit.RejectionClass, "policy"))
			if err := service.appendToolResult(stream, pending.ToolCallID, "Shell", pending.ArgsJSON, result, pending.ReasoningContent, nil); err != nil {
				return err
			}
			if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
				newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_queue_blocked", map[string]any{
					"tool_call_id":    pending.ToolCallID,
					"exec_id":         pending.ExecID,
					"message_id":      pending.MessageID,
					"provider_pass":   pending.ProviderPass,
					"rejection_class": circuit.RejectionClass,
				}),
			}); err != nil {
				return err
			}
			if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
				return err
			}
		}
		return nil
	}
	next, ok := releaseForegroundShellDispatch(stream, completed)
	if !ok {
		return nil
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: next.Message}); err != nil {
		return err
	}
	service.scheduleShellForegroundRecovery(stream.RequestID, next.Pending)
	return nil
}
