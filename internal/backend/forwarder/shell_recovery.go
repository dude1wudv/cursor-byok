package forwarder

import (
	"context"
	"fmt"
	"strings"
	"time"

	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const shellTerminalRecoveryGrace = 1500 * time.Millisecond

const (
	shellRecoveryReasonForegroundDeadline = "foreground_deadline"
	shellRecoveryReasonTransportClosed    = "transport_closed"
	shellRecoveryReasonSkipped            = "skipped"
)

func initializePendingExecForTracking(pending runtimecore.PendingExec) runtimecore.PendingExec {
	if strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	now := time.Now().UTC()
	if pending.OpenedAt.IsZero() {
		pending.OpenedAt = now
	}
	if pending.LastShellActivityAt.IsZero() {
		pending.LastShellActivityAt = pending.OpenedAt
	}
	if pending.ShellForegroundDeadline.IsZero() {
		pending.ShellForegroundDeadline = pending.OpenedAt.Add(shellForegroundTimeoutDuration(pending.ArgsJSON) + shellTerminalRecoveryGrace)
	}
	return pending
}

func shellForegroundTimeoutDuration(argsJSON []byte) time.Duration {
	timeoutMS := int64(30000)
	args, err := runtimecore.DecodeArgsMap(argsJSON)
	if err == nil {
		if blockUntilMS, found, err := runtimecore.ReadFloat64Arg(args, "block_until_ms", "blockUntilMS"); err == nil && found {
			if blockUntilMS <= 0 {
				return 0
			}
			timeoutMS = int64(blockUntilMS)
		}
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func shellForegroundTimeoutMS(argsJSON []byte) int64 {
	return shellForegroundTimeoutDuration(argsJSON).Milliseconds()
}

func (service *Service) scheduleShellForegroundRecovery(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "shell" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	deadline := pending.ShellForegroundDeadline
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(shellForegroundTimeoutDuration(pending.ArgsJSON) + shellTerminalRecoveryGrace)
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellForeground, pending.ExecID),
		time.Until(deadline),
		streamTimerShellForeground,
		pending.ExecID,
		pending.MessageID,
		shellRecoveryReasonForegroundDeadline,
	)
}

func (service *Service) scheduleShellRecoveryCandidate(requestID string, pending runtimecore.PendingExec, reason string) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecKind) != "shell" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	if !found || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass {
		stream.mu.Unlock()
		return
	}
	if tombstone, completed := stream.ShellExecTombstones[pending.ExecID]; completed && tombstone.MessageID == pending.MessageID && tombstone.Generation == pending.ProviderPass {
		stream.mu.Unlock()
		return
	}
	if stream.ShellRecoveryCandidates == nil {
		stream.ShellRecoveryCandidates = make(map[string]shellRecoveryCandidate)
	}
	candidate := shellRecoveryCandidate{
		ExecID:     pending.ExecID,
		MessageID:  pending.MessageID,
		Generation: pending.ProviderPass,
		Reason:     reason,
		ObservedAt: time.Now().UTC(),
	}
	stream.ShellRecoveryCandidates[pending.ExecID] = candidate
	current.ShellRecoveryScheduled = true
	stream.PendingExecs[pending.ExecID] = current
	stream.UpdatedAt = candidate.ObservedAt
	stream.mu.Unlock()
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellTransportClose, pending.ExecID),
		shellTerminalRecoveryGrace,
		streamTimerShellTransportClose,
		pending.ExecID,
		pending.MessageID,
		reason,
	)
}

func (service *Service) scheduleShellTransportCloseRecovery(requestID string, pending runtimecore.PendingExec) {
	service.scheduleShellRecoveryCandidate(requestID, pending, shellRecoveryReasonTransportClosed)
}

func snapshotPendingExecWithStatus(stream *ActiveStream, execID string) (runtimecore.PendingExec, StreamStatus, bool) {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return runtimecore.PendingExec{}, "", false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingExecs[strings.TrimSpace(execID)]
	if !ok {
		return runtimecore.PendingExec{}, stream.Status, false
	}
	return item, stream.Status, true
}

func clearShellRecoveryCandidate(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	stream.mu.Lock()
	if candidate, ok := stream.ShellRecoveryCandidates[pending.ExecID]; ok && candidate.MessageID == pending.MessageID && candidate.Generation == pending.ProviderPass {
		delete(stream.ShellRecoveryCandidates, pending.ExecID)
	}
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerShellForeground, pending.ExecID))
	clearStreamTimer(stream, providerTimerKey(streamTimerShellTransportClose, pending.ExecID))
}

func (service *Service) recoverShellWithoutTerminalIfNeeded(stream *ActiveStream, execID string, messageID uint32, reason string) error {
	if stream == nil || strings.TrimSpace(execID) == "" {
		return nil
	}
	current, status, found := snapshotPendingExecWithStatus(stream, execID)
	if !found || current.MessageID != messageID || strings.TrimSpace(current.ExecKind) != "shell" || isTerminalStreamStatus(status) {
		return nil
	}
	switch strings.TrimSpace(current.StreamState) {
	case "exited", "backgrounded", "permission_denied":
		return nil
	}
	if reason == shellRecoveryReasonForegroundDeadline {
		if !current.ShellForegroundDeadline.IsZero() && time.Now().UTC().Before(current.ShellForegroundDeadline) {
			return nil
		}
		stream.mu.Lock()
		if stream.ShellRecoveryCandidates == nil {
			stream.ShellRecoveryCandidates = make(map[string]shellRecoveryCandidate)
		}
		stream.ShellRecoveryCandidates[current.ExecID] = shellRecoveryCandidate{
			ExecID: current.ExecID, MessageID: current.MessageID, Generation: current.ProviderPass,
			Reason: reason, ObservedAt: time.Now().UTC(),
		}
		stream.mu.Unlock()
	}
	return service.recoverShellWithoutTerminal(stream, current, reason)
}

func (service *Service) recoverShellWithoutTerminal(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if stream == nil {
		return nil
	}
	if strings.TrimSpace(reason) == shellRecoveryReasonSkipped {
		retried, err := service.retrySkippedShell(stream, pending)
		if retried || err != nil {
			return err
		}
	}
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	candidate, candidateFound := stream.ShellRecoveryCandidates[pending.ExecID]
	if !found || !candidateFound || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || candidate.MessageID != pending.MessageID || candidate.Generation != pending.ProviderPass || candidate.Reason != strings.TrimSpace(reason) {
		stream.mu.Unlock()
		return nil
	}
	if tombstone, completed := stream.ShellExecTombstones[pending.ExecID]; completed && tombstone.MessageID == pending.MessageID && tombstone.Generation == pending.ProviderPass {
		stream.mu.Unlock()
		return nil
	}
	if stream.ShellExecTombstones == nil {
		stream.ShellExecTombstones = make(map[string]shellExecTombstone)
	}
	if stream.RecentCompletedExecs == nil {
		stream.RecentCompletedExecs = make(map[uint32]time.Time)
	}
	stream.ShellExecTombstones[pending.ExecID] = shellExecTombstone{MessageID: pending.MessageID, Generation: pending.ProviderPass, CompletedAt: time.Now().UTC()}
	delete(stream.ShellRecoveryCandidates, pending.ExecID)
	stream.mu.Unlock()

	pending = current
	markExecCompleted(stream, pending)
	result := fmt.Sprintf("Shell did not provide a terminal result (%s). The execution was closed locally after a per-command grace period.", candidate.Reason)
	if candidate.Reason == shellRecoveryReasonSkipped {
		result = "shell skipped: Cursor rejected the execution before it started"
	}
	if err := service.appendToolResult(stream, pending.ToolCallID, "Shell", pending.ArgsJSON, result, pending.ReasoningContent, nil); err != nil {
		return err
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_recovered", map[string]any{
			"tool_call_id":        pending.ToolCallID,
			"message_id":          pending.MessageID,
			"exec_id":             pending.ExecID,
			"generation":          pending.ProviderPass,
			"reason":              candidate.Reason,
			"chunk_count":         pending.ChunkCount,
			"stdout_buffer_bytes": len(pending.StdoutBuffer),
			"stderr_buffer_bytes": len(pending.StderrBuffer),
			"terminal":            true,
		}),
	}); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, pending.ToolCallID, pending.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.advanceForegroundShellQueue(stream, pending); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func shellSkippedRetryEligibleLocked(stream *ActiveStream, pending runtimecore.PendingExec) bool {
	if stream == nil || !pending.FirstChunkAt.IsZero() || stream.ShellRetryCountByToolCall[strings.TrimSpace(pending.ToolCallID)] > 0 {
		return false
	}
	if _, started := stream.ShellStartedExecs[pending.ExecID]; started {
		return false
	}
	for execID := range stream.ActiveForegroundShells {
		if execID != pending.ExecID {
			return true
		}
	}
	return false
}

func (service *Service) retrySkippedShell(stream *ActiveStream, pending runtimecore.PendingExec) (bool, error) {
	if service == nil || service.execBridge == nil || stream == nil {
		return false, nil
	}
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	candidate, candidateFound := stream.ShellRecoveryCandidates[pending.ExecID]
	eligible := found && candidateFound && candidate.Reason == shellRecoveryReasonSkipped &&
		current.MessageID == pending.MessageID && current.ProviderPass == pending.ProviderPass && shellSkippedRetryEligibleLocked(stream, current)
	stream.mu.Unlock()
	if !eligible {
		return false, nil
	}

	message, replacement, err := service.execBridge.OpenExec(execbridge.OpenExecContext{
		ConversationID: stream.ConversationID,
		ModelID:        stream.ModelID,
	}, runtimecore.ToolInvocation{
		CallID:   current.ToolCallID,
		ToolName: "Shell",
		ArgsJSON: append([]byte(nil), current.ArgsJSON...),
	})
	if err != nil {
		return false, err
	}
	replacement.ModelCallID = current.ModelCallID
	replacement.ProviderPass = current.ProviderPass
	replacement.ReasoningContent = current.ReasoningContent
	replacement.ReasoningSignature = current.ReasoningSignature
	replacement.ReasoningSignatureSource = current.ReasoningSignatureSource
	replacement.OpenedAt = time.Time{}
	replacement.LastShellActivityAt = time.Time{}
	replacement.ShellForegroundDeadline = time.Time{}
	replacement.ShellRecoveryScheduled = false

	stream.mu.Lock()
	current, found = stream.PendingExecs[pending.ExecID]
	candidate, candidateFound = stream.ShellRecoveryCandidates[pending.ExecID]
	if !found || !candidateFound || candidate.Reason != shellRecoveryReasonSkipped || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || !shellSkippedRetryEligibleLocked(stream, current) {
		stream.mu.Unlock()
		return false, nil
	}
	now := time.Now().UTC()
	if stream.ShellExecTombstones == nil {
		stream.ShellExecTombstones = make(map[string]shellExecTombstone)
	}
	if stream.RecentCompletedExecs == nil {
		stream.RecentCompletedExecs = make(map[uint32]time.Time)
	}
	stream.ShellExecTombstones[current.ExecID] = shellExecTombstone{MessageID: current.MessageID, Generation: current.ProviderPass, CompletedAt: now}
	stream.RecentCompletedExecs[current.MessageID] = now
	delete(stream.PendingExecs, current.ExecID)
	delete(stream.ActiveForegroundShells, current.ExecID)
	delete(stream.ShellStartedExecs, current.ExecID)
	delete(stream.ShellRecoveryCandidates, current.ExecID)
	if stream.ShellAwaitingStartExecID == current.ExecID {
		stream.ShellAwaitingStartExecID = ""
	}
	if stream.ShellRetryCountByToolCall == nil {
		stream.ShellRetryCountByToolCall = make(map[string]int)
	}
	stream.ShellRetryCountByToolCall[current.ToolCallID]++
	stream.PendingExecs[replacement.ExecID] = replacement
	stream.QueuedForegroundShells = append([]queuedShellDispatch{{Message: message, Pending: replacement}}, stream.QueuedForegroundShells...)
	syncLegacyForegroundShellLocked(stream)
	ready, dispatch := takeNextForegroundShellDispatchLocked(stream)
	active := len(stream.ActiveForegroundShells)
	queued := len(stream.QueuedForegroundShells)
	limit := shellDispatchLimitLocked(stream)
	stream.UpdatedAt = now
	stream.mu.Unlock()

	clearStreamTimer(stream, providerTimerKey(streamTimerShellForeground, current.ExecID))
	clearStreamTimer(stream, providerTimerKey(streamTimerShellTransportClose, current.ExecID))
	commandHash, argsHash, cwdHash := shellInvocationHashes(current.ArgsJSON)
	values := map[string]any{
		"tool_call_id": current.ToolCallID, "old_exec_id": current.ExecID, "old_message_id": current.MessageID,
		"exec_id": replacement.ExecID, "message_id": replacement.MessageID, "retry_count": 1,
		"active": active, "queued": queued, "limit": limit, "recovery": "retried",
		"command_hash": commandHash, "args_hash": argsHash, "cwd_hash": cwdHash,
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_retry", values),
	}); err != nil {
		return false, err
	}
	if service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "shell_retry", values)
	}
	if dispatch {
		if err := service.publishForegroundShellDispatch(stream, ready); err != nil {
			return false, err
		}
	}
	return true, nil
}
