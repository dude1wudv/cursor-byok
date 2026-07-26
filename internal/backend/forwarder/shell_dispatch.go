package forwarder

import (
	"context"
	"log"
	"strings"
	"time"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	legacyShellMaxConcurrentPerRun  = 1
	defaultShellMaxConcurrentPerRun = 32
	shellMaxTransportAttempts       = 5
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

// nextShellDispatchSequenceLocked 返回本 stream 内下一个单调 queue/dispatch 事件序号。
func nextShellDispatchSequenceLocked(stream *ActiveStream) int64 {
	stream.ShellDispatchSequence++
	return stream.ShellDispatchSequence
}

// shellDispatchObservationLocked 汇总当前 FIFO 状态供事件记录，不改变调度。
func shellDispatchObservationLocked(stream *ActiveStream, queuePosition int) shellDispatchObservation {
	return shellDispatchObservation{
		Sequence:      nextShellDispatchSequenceLocked(stream),
		QueuePosition: queuePosition,
		QueueDepth:    len(stream.QueuedForegroundShells),
		Active:        len(stream.ActiveForegroundShells),
		Limit:         shellDispatchLimitLocked(stream),
	}
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
	if len(stream.ActiveForegroundShells) < shellDispatchLimitLocked(stream) {
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
	if len(stream.ActiveForegroundShells) >= shellDispatchLimitLocked(stream) || len(stream.QueuedForegroundShells) == 0 {
		return queuedShellDispatch{}, false
	}
	now := time.Now().UTC()
	readyIndex := -1
	for index := range stream.QueuedForegroundShells {
		readyAt := stream.QueuedForegroundShells[index].ReadyAt
		if readyAt.IsZero() || !readyAt.After(now) {
			readyIndex = index
			break
		}
	}
	if readyIndex < 0 {
		return queuedShellDispatch{}, false
	}
	ready := stream.QueuedForegroundShells[readyIndex]
	stream.QueuedForegroundShells = append(stream.QueuedForegroundShells[:readyIndex], stream.QueuedForegroundShells[readyIndex+1:]...)
	ready.Pending = activateForegroundShellLocked(stream, ready.Pending)
	ready.Observation = shellDispatchObservationLocked(stream, 0)
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
	ready := make([]queuedShellDispatch, 0, shellDispatchLimitLocked(stream)-len(stream.ActiveForegroundShells))
	for {
		next, found := takeNextForegroundShellDispatchLocked(stream)
		if !found {
			break
		}
		ready = append(ready, next)
	}
	return ready
}

func releaseForegroundShellDispatch(stream *ActiveStream, completed runtimecore.PendingExec) (queuedShellDispatch, bool) {
	ready := releaseForegroundShellDispatches(stream, completed)
	if len(ready) == 0 {
		return queuedShellDispatch{}, false
	}
	return ready[0], true
}

func markShellStartedPublished(stream *ActiveStream, pending runtimecore.PendingExec) runtimecore.PendingExec {
	if stream == nil {
		return pending
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	pending.ShellStartedPublished = true
	if current, ok := stream.PendingExecs[pending.ExecID]; ok && current.MessageID == pending.MessageID {
		current.ShellStartedPublished = true
		stream.PendingExecs[pending.ExecID] = current
		pending = current
	}
	if current, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && current.MessageID == pending.MessageID {
		current.ShellStartedPublished = true
		stream.ActiveForegroundShells[pending.ExecID] = current
	}
	for index := range stream.QueuedForegroundShells {
		if stream.QueuedForegroundShells[index].Pending.ExecID == pending.ExecID && stream.QueuedForegroundShells[index].Pending.MessageID == pending.MessageID {
			stream.QueuedForegroundShells[index].Pending.ShellStartedPublished = true
		}
	}
	return pending
}

func (service *Service) publishForegroundShellDispatch(stream *ActiveStream, item queuedShellDispatch) error {
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: item.Message}); err != nil {
		return err
	}
	startedEmitted := false
	if item.StartedToolCall != nil && !item.Pending.ShellStartedPublished {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(item.Pending.ToolCallID, item.Pending.ModelCallID, item.StartedToolCall),
		}); err != nil {
			return err
		}
		item.Pending = markShellStartedPublished(stream, item.Pending)
		startedEmitted = true
	}
	service.scheduleShellForegroundRecovery(stream.RequestID, item.Pending)
	service.recordShellDispatchTransition(stream, item.Pending, "shell_dispatch_activated", item.Observation)
	service.recordExecDispatchMetadata(stream, item.Pending, false, startedEmitted, "started_checkpoint_then_exec")
	checkpointErr := service.publishCheckpoint(stream.RequestID, stream.ConversationID)
	if checkpointErr != nil {
		log.Printf("forwarder shell post-dispatch checkpoint failed request_id=%s exec_id=%s err=%v", stream.RequestID, item.Pending.ExecID, checkpointErr)
	}
	return nil
}

// recordShellDispatchTransition 落一条 FIFO 状态迁移证据：单调序号、队列深度与位置、
// exec ID 与本次迁移时的 command/cwd hash，用于还原入队→出队→开始→完成顺序，
// 并定位 cwd 漂移发生在服务端还是客户端。
func (service *Service) recordShellDispatchTransition(stream *ActiveStream, pending runtimecore.PendingExec, event string, observation shellDispatchObservation) {
	if service == nil || stream == nil {
		return
	}
	commandHash, _, cwdHash := shellInvocationHashes(pending.ArgsJSON)
	values := map[string]any{
		"tool_call_id":      pending.ToolCallID,
		"logical_shell_id":  pending.LogicalShellID,
		"transport_attempt": pending.ShellAttempt,
		"retry_not_before":  pending.ShellRetryNotBefore,
		"exec_id":           pending.ExecID,
		"message_id":        pending.MessageID,
		"provider_pass":     pending.ProviderPass,
		"dispatch_sequence": observation.Sequence,
		"queue_position":    observation.QueuePosition,
		"queue_depth":       observation.QueueDepth,
		"active":            observation.Active,
		"limit":             observation.Limit,
		"command_hash":      commandHash,
		"cwd_hash":          cwdHash,
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, event, values),
	}); err != nil {
		log.Printf("forwarder shell dispatch transition metadata failed request_id=%s exec_id=%s event=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ExecID), event, err)
	}
	if service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, event, values)
	}
}

func (service *Service) dispatchOrQueueForegroundShell(stream *ActiveStream, message *agentv1.AgentServerMessage, startedToolCall *agentv1.ToolCall, pending runtimecore.PendingExec) (bool, error) {
	if startedToolCall != nil && !pending.ShellStartedPublished {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildToolCallStartedMessage(pending.ToolCallID, pending.ModelCallID, startedToolCall),
		}); err != nil {
			return false, err
		}
		pending = markShellStartedPublished(stream, pending)
		if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
			return false, err
		}
	}
	if reserveForegroundShellDispatch(stream, message, pending, startedToolCall) {
		stream.mu.Lock()
		pending = stream.ActiveForegroundShells[pending.ExecID]
		observation := shellDispatchObservationLocked(stream, 0)
		stream.mu.Unlock()
		return true, service.publishForegroundShellDispatch(stream, queuedShellDispatch{
			Message:         message,
			StartedToolCall: startedToolCall,
			Pending:         pending,
			Observation:     observation,
		})
	}
	stream.mu.Lock()
	queuePosition := 0
	for index := range stream.QueuedForegroundShells {
		queued := stream.QueuedForegroundShells[index].Pending
		if queued.ExecID == pending.ExecID && queued.MessageID == pending.MessageID && queued.ProviderPass == pending.ProviderPass {
			queuePosition = index + 1
			break
		}
	}
	observation := shellDispatchObservationLocked(stream, queuePosition)
	stream.mu.Unlock()
	service.recordShellDispatchTransition(stream, pending, "shell_dispatch_queued", observation)
	return false, nil
}

func (service *Service) advanceForegroundShellQueue(stream *ActiveStream, completed runtimecore.PendingExec) error {
	for _, item := range releaseForegroundShellDispatches(stream, completed) {
		if err := service.publishForegroundShellDispatch(stream, item); err != nil {
			return err
		}
	}
	return service.scheduleNextShellRetry(stream)
}

func shellRetryDelay(attempt int, messageID uint32) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 250 * time.Millisecond * time.Duration(1<<(attempt-1))
	if delay > 4*time.Second {
		delay = 4 * time.Second
	}
	return delay + time.Duration(messageID%125)*time.Millisecond
}

func (service *Service) scheduleNextShellRetry(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	var earliest time.Time
	for _, item := range stream.QueuedForegroundShells {
		if item.ReadyAt.IsZero() {
			continue
		}
		if earliest.IsZero() || item.ReadyAt.Before(earliest) {
			earliest = item.ReadyAt
		}
	}
	stream.mu.Unlock()
	key := providerTimerKey(streamTimerShellRetryReady, "")
	if earliest.IsZero() {
		clearStreamTimer(stream, key)
		return nil
	}
	service.scheduleStreamTimer(stream, key, time.Until(earliest), streamTimerShellRetryReady, "", 0, "retry_ready")
	return nil
}

func (service *Service) dispatchReadyForegroundShells(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	for {
		stream.mu.Lock()
		item, ok := takeNextForegroundShellDispatchLocked(stream)
		stream.mu.Unlock()
		if !ok {
			break
		}
		if err := service.publishForegroundShellDispatch(stream, item); err != nil {
			return err
		}
	}
	return service.scheduleNextShellRetry(stream)
}

func (service *Service) requeueSkippedForegroundShell(stream *ActiveStream, skipped runtimecore.PendingExec) error {
	if service == nil || stream == nil {
		return nil
	}
	message, retry, err := service.execBridge.ReopenShell(execbridge.OpenExecContext{
		ConversationID: stream.ConversationID,
		ModelID:        stream.ModelID,
	}, skipped)
	if err != nil {
		return err
	}
	retry.ShellRetryNotBefore = time.Now().UTC().Add(shellRetryDelay(skipped.ShellAttempt, skipped.MessageID))

	now := time.Now().UTC()
	stream.mu.Lock()
	current, found := stream.PendingExecs[skipped.ExecID]
	active, activeFound := stream.ActiveForegroundShells[skipped.ExecID]
	if !found || !activeFound || current.MessageID != skipped.MessageID || active.MessageID != skipped.MessageID {
		stream.mu.Unlock()
		return nil
	}
	if stream.ShellExecTombstones == nil {
		stream.ShellExecTombstones = make(map[string]shellExecTombstone)
	}
	stream.ShellExecTombstones[skipped.ExecID] = shellExecTombstone{
		MessageID:      skipped.MessageID,
		Generation:     skipped.ProviderPass,
		LogicalShellID: skipped.LogicalShellID,
		Attempt:        skipped.ShellAttempt,
		Reason:         shellRecoveryReasonSkipped,
		CompletedAt:    now,
	}
	if stream.RecentCompletedExecs == nil {
		stream.RecentCompletedExecs = make(map[uint32]time.Time)
	}
	stream.RecentCompletedExecs[skipped.MessageID] = now
	delete(stream.PendingExecs, skipped.ExecID)
	delete(stream.ActiveForegroundShells, skipped.ExecID)
	if stream.ShellAwaitingStartExecID == skipped.ExecID {
		stream.ShellAwaitingStartExecID = ""
	}
	syncLegacyForegroundShellLocked(stream)
	stream.PendingExecs[retry.ExecID] = retry
	stream.QueuedForegroundShells = append(stream.QueuedForegroundShells, queuedShellDispatch{
		Message: message,
		Pending: retry,
		ReadyAt: retry.ShellRetryNotBefore,
	})
	observation := shellDispatchObservationLocked(stream, len(stream.QueuedForegroundShells))
	stream.UpdatedAt = now
	stream.mu.Unlock()

	clearStreamTimer(stream, providerTimerKey(streamTimerShellSupervision, skipped.ExecID))
	clearStreamTimer(stream, providerTimerKey(streamTimerExecResult, skipped.ExecID))
	service.recordShellDispatchTransition(stream, retry, "shell_dispatch_retry_wait", observation)
	checkpointErr := service.publishCheckpoint(stream.RequestID, stream.ConversationID)
	dispatchErr := service.dispatchReadyForegroundShells(stream)
	if checkpointErr != nil {
		return checkpointErr
	}
	return dispatchErr
}
