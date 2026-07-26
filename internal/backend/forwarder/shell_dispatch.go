package forwarder

import (
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	legacyShellMaxConcurrentPerRun  = 1
	defaultShellMaxConcurrentPerRun = 8
)

func shellExecutionBegan(message *agentv1.ExecClientMessage) bool {
	if message == nil || message.GetShellStream() == nil {
		return false
	}
	switch message.GetShellStream().GetEvent().(type) {
	case *agentv1.ShellStream_Start, *agentv1.ShellStream_Stdout, *agentv1.ShellStream_Stderr:
		return true
	default:
		return false
	}
}

func shellDispatchSnapshot(stream *ActiveStream, execID string) (active int, queued int, limit int, awaitingStart bool, started bool) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	active = len(stream.ActiveForegroundShells)
	queued = len(stream.QueuedForegroundShells)
	limit = shellDispatchLimitLocked(stream)
	awaitingStart = stream.ShellAwaitingStartExecID == strings.TrimSpace(execID)
	if pending, ok := stream.PendingExecs[strings.TrimSpace(execID)]; ok {
		switch strings.TrimSpace(pending.StreamState) {
		case "started", "streaming":
			started = true
		}
	}
	return
}

func shellDispatchLimitLocked(stream *ActiveStream) int {
	if stream.ShellMaxConcurrent < 1 {
		return legacyShellMaxConcurrentPerRun
	}
	return stream.ShellMaxConcurrent
}

func syncLegacyForegroundShellLocked(stream *ActiveStream) {
	if stream == nil {
		return
	}
	if _, ok := stream.ActiveForegroundShells[stream.ActiveForegroundShellExecID]; ok {
		return
	}
	stream.ActiveForegroundShellExecID = ""
	for execID := range stream.ActiveForegroundShells {
		stream.ActiveForegroundShellExecID = execID
		return
	}
}

func activateForegroundShellLocked(stream *ActiveStream, pending runtimecore.PendingExec) runtimecore.PendingExec {
	now := time.Now().UTC()
	pending.OpenedAt = now
	pending.LastShellActivityAt = now
	pending.ShellForegroundDeadline = now.Add(shellForegroundTimeoutDuration(pending.ArgsJSON) + shellTerminalRecoveryGrace)
	if stream.PendingExecs == nil {
		stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	}
	if stream.ActiveForegroundShells == nil {
		stream.ActiveForegroundShells = make(map[string]runtimecore.PendingExec)
	}
	stream.PendingExecs[pending.ExecID] = pending
	stream.ActiveForegroundShells[pending.ExecID] = pending
	stream.ShellAwaitingStartExecID = pending.ExecID
	syncLegacyForegroundShellLocked(stream)
	stream.UpdatedAt = now
	return pending
}

func reserveForegroundShellDispatch(stream *ActiveStream, message *agentv1.AgentServerMessage, pending runtimecore.PendingExec, startedToolCall ...*agentv1.ToolCall) bool {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return true
	}
	var queuedStartedToolCall *agentv1.ToolCall
	if len(startedToolCall) > 0 {
		queuedStartedToolCall = startedToolCall[0]
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ShellAwaitingStartExecID == "" && len(stream.ActiveForegroundShells) < shellDispatchLimitLocked(stream) {
		activateForegroundShellLocked(stream, pending)
		return true
	}
	pending.OpenedAt = time.Time{}
	pending.LastShellActivityAt = time.Time{}
	pending.ShellForegroundDeadline = time.Time{}
	stream.PendingExecs[pending.ExecID] = pending
	stream.QueuedForegroundShells = append(stream.QueuedForegroundShells, queuedShellDispatch{
		Message:         message,
		StartedToolCall: queuedStartedToolCall,
		Pending:         pending,
	})
	stream.UpdatedAt = time.Now().UTC()
	return false
}

func discardForegroundShellDispatch(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if active, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && active.MessageID == pending.MessageID && active.ProviderPass == pending.ProviderPass {
		delete(stream.ActiveForegroundShells, pending.ExecID)
		if stream.ShellAwaitingStartExecID == pending.ExecID {
			stream.ShellAwaitingStartExecID = ""
		}
		syncLegacyForegroundShellLocked(stream)
	}
	for index := range stream.QueuedForegroundShells {
		queued := stream.QueuedForegroundShells[index].Pending
		if queued.ExecID == pending.ExecID && queued.MessageID == pending.MessageID && queued.ProviderPass == pending.ProviderPass {
			stream.QueuedForegroundShells = append(stream.QueuedForegroundShells[:index], stream.QueuedForegroundShells[index+1:]...)
			break
		}
	}
	stream.UpdatedAt = time.Now().UTC()
}

func takeNextForegroundShellDispatchLocked(stream *ActiveStream) (queuedShellDispatch, bool) {
	if stream.ShellAwaitingStartExecID != "" || len(stream.ActiveForegroundShells) >= shellDispatchLimitLocked(stream) || len(stream.QueuedForegroundShells) == 0 {
		return queuedShellDispatch{}, false
	}
	ready := stream.QueuedForegroundShells[0]
	stream.QueuedForegroundShells = append([]queuedShellDispatch(nil), stream.QueuedForegroundShells[1:]...)
	ready.Pending = activateForegroundShellLocked(stream, ready.Pending)
	return ready, true
}

func releaseForegroundShellDispatches(stream *ActiveStream, completed runtimecore.PendingExec) []queuedShellDispatch {
	if stream == nil || strings.TrimSpace(completed.ExecKind) != "shell" {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	active, ok := stream.ActiveForegroundShells[completed.ExecID]
	if !ok || active.MessageID != completed.MessageID || active.ProviderPass != completed.ProviderPass {
		return nil
	}
	delete(stream.ActiveForegroundShells, completed.ExecID)
	if stream.ShellAwaitingStartExecID == completed.ExecID {
		stream.ShellAwaitingStartExecID = ""
	}
	syncLegacyForegroundShellLocked(stream)
	stream.UpdatedAt = time.Now().UTC()
	if ready, ok := takeNextForegroundShellDispatchLocked(stream); ok {
		return []queuedShellDispatch{ready}
	}
	return nil
}

func releaseForegroundShellDispatch(stream *ActiveStream, completed runtimecore.PendingExec) (queuedShellDispatch, bool) {
	ready := releaseForegroundShellDispatches(stream, completed)
	if len(ready) == 0 {
		return queuedShellDispatch{}, false
	}
	return ready[0], true
}

func (service *Service) publishForegroundShellDispatch(stream *ActiveStream, item queuedShellDispatch) error {
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: item.Message}); err != nil {
		return err
	}
	if item.StartedToolCall != nil {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(item.Pending.ToolCallID, item.Pending.ModelCallID, item.StartedToolCall),
		}); err != nil {
			return err
		}
	}
	service.scheduleShellForegroundRecovery(stream.RequestID, item.Pending)
	service.recordExecDispatchMetadata(stream, item.Pending, false, true, "exec_then_started_then_checkpoint")
	return service.publishCheckpoint(stream.RequestID, stream.ConversationID)
}

func releaseShellStartAllocation(stream *ActiveStream, pending runtimecore.PendingExec) (queuedShellDispatch, bool) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return queuedShellDispatch{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ShellAwaitingStartExecID != pending.ExecID {
		return queuedShellDispatch{}, false
	}
	stream.ShellAwaitingStartExecID = ""
	stream.UpdatedAt = time.Now().UTC()
	return takeNextForegroundShellDispatchLocked(stream)
}

func (service *Service) advanceForegroundShellStartQueue(stream *ActiveStream, pending runtimecore.PendingExec) error {
	ready, ok := releaseShellStartAllocation(stream, pending)
	if !ok {
		return nil
	}
	return service.publishForegroundShellDispatch(stream, ready)
}

func (service *Service) dispatchOrQueueForegroundShell(stream *ActiveStream, message *agentv1.AgentServerMessage, startedToolCall *agentv1.ToolCall, pending runtimecore.PendingExec) (bool, error) {
	if reserveForegroundShellDispatch(stream, message, pending, startedToolCall) {
		stream.mu.Lock()
		pending = stream.ActiveForegroundShells[pending.ExecID]
		stream.mu.Unlock()
		return true, service.publishForegroundShellDispatch(stream, queuedShellDispatch{
			Message:         message,
			StartedToolCall: startedToolCall,
			Pending:         pending,
		})
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_dispatch_queued", map[string]any{
			"tool_call_id":  pending.ToolCallID,
			"exec_id":       pending.ExecID,
			"message_id":    pending.MessageID,
			"provider_pass": pending.ProviderPass,
			"active":        len(stream.ActiveForegroundShells),
			"limit":         stream.ShellMaxConcurrent,
		}),
	})
	return false, err
}

func (service *Service) advanceForegroundShellQueue(stream *ActiveStream, completed runtimecore.PendingExec) error {
	for _, item := range releaseForegroundShellDispatches(stream, completed) {
		if err := service.publishForegroundShellDispatch(stream, item); err != nil {
			return err
		}
	}
	return nil
}
