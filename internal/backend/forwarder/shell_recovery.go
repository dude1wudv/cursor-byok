package forwarder

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

const shellTerminalRecoveryGrace = 1500 * time.Millisecond

const (
	shellRecoveryReasonForegroundDeadline = "foreground_deadline"
	shellRecoveryReasonTransportClosed    = "transport_closed"
	shellRecoveryReasonSkipped            = "skipped"
)

// Shell 异常收口状态机取值（PendingExec.ShellRecoveryState）。
// 状态直接挂在 PendingExec 上——不再维护平行的 ShellRecoveryCandidates 影子表，
// 消除两份账本之间的交叉一致性检查。
const (
	shellRecoveryStateNone           int8 = 0
	shellRecoveryStateCandidate      int8 = 1
	shellRecoveryStateAbortRequested int8 = 2
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

// scheduleShellForegroundRecovery 安排（或重置）该 exec 唯一的监督定时器到 foreground 截止时间。
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
		providerTimerKey(streamTimerShellSupervision, pending.ExecID),
		time.Until(deadline),
		streamTimerShellSupervision,
		pending.ExecID,
		pending.MessageID,
		shellRecoveryReasonForegroundDeadline,
	)
}

// scheduleShellRecoveryCandidate 把非终态异常信号（skipped/transport_closed/control_throw）
// 登记到 PendingExec 上，并把监督定时器改短到 grace 窗口。
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
	now := time.Now().UTC()
	current.ShellRecoveryState = shellRecoveryStateCandidate
	current.ShellRecoveryStateAt = now
	current.ShellRecoveryReason = reason
	current.ShellRecoveryGeneration = current.ShellActivityGeneration
	stream.PendingExecs[pending.ExecID] = current
	stream.UpdatedAt = now
	stream.mu.Unlock()
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellSupervision, pending.ExecID),
		shellTerminalRecoveryGrace,
		streamTimerShellSupervision,
		pending.ExecID,
		pending.MessageID,
		reason,
	)
}

func (service *Service) scheduleShellTransportCloseRecovery(requestID string, pending runtimecore.PendingExec) {
	service.scheduleShellRecoveryCandidate(requestID, pending, shellRecoveryReasonTransportClosed)
}

// refreshShellForegroundActivity 把 Start/stdout/stderr 归一为单一活动迁移：递增活动代次、
// 复位恢复状态、失效旧监督定时器，并按最新截止时间重新安排监督。
func (service *Service) refreshShellForegroundActivity(stream *ActiveStream, pending runtimecore.PendingExec) runtimecore.PendingExec {
	if service == nil || stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	if !found || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass {
		stream.mu.Unlock()
		return pending
	}
	now := time.Now().UTC()
	current.ShellActivityGeneration++
	current.ShellRecoveryState = shellRecoveryStateNone
	current.ShellRecoveryStateAt = now
	current.ShellRecoveryReason = ""
	current.ShellForegroundDeadline = now.Add(shellForegroundTimeoutDuration(current.ArgsJSON) + shellTerminalRecoveryGrace)
	stream.PendingExecs[current.ExecID] = current
	stream.UpdatedAt = now
	stream.mu.Unlock()
	service.scheduleShellForegroundRecovery(stream.RequestID, current)
	return current
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

// clearShellRecoveryCandidate 在收到真实终态时复位恢复状态并撤销监督定时器。
func clearShellRecoveryCandidate(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	stream.mu.Lock()
	if current, ok := stream.PendingExecs[pending.ExecID]; ok && current.MessageID == pending.MessageID && current.ProviderPass == pending.ProviderPass {
		current.ShellRecoveryState = shellRecoveryStateNone
		current.ShellRecoveryReason = ""
		stream.PendingExecs[pending.ExecID] = current
	}
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerShellSupervision, pending.ExecID))
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
	reason = strings.TrimSpace(reason)
	if reason == shellRecoveryReasonForegroundDeadline {
		if !current.ShellForegroundDeadline.IsZero() && time.Now().UTC().Before(current.ShellForegroundDeadline) {
			// 定时器早于最新截止时间触发（活动刷新过 deadline）：以当前截止时间重新武装唯一监督定时器。
			service.scheduleShellForegroundRecovery(stream.RequestID, current)
			return nil
		}
		// 两阶段收口：先请求客户端 abort，短 grace 后仍无真实终态才本地关闭。
		if current.ShellRecoveryState != shellRecoveryStateAbortRequested {
			return service.requestShellAbortBeforeRecovery(stream, current)
		}
		return service.recoverShellWithoutTerminal(stream, current, reason)
	}
	// 候选路径：仅当 PendingExec 上仍登记着同原因、同活动代次的候选时才收口。
	if current.ShellRecoveryState != shellRecoveryStateCandidate ||
		current.ShellRecoveryReason != reason ||
		current.ShellRecoveryGeneration != current.ShellActivityGeneration {
		return nil
	}
	return service.recoverShellWithoutTerminal(stream, current, reason)
}

// requestShellAbortBeforeRecovery 是 foreground 恢复第一阶段：登记状态并向客户端请求中止，不合成任何终态。
func (service *Service) requestShellAbortBeforeRecovery(stream *ActiveStream, pending runtimecore.PendingExec) error {
	if service == nil || stream == nil {
		return nil
	}
	now := time.Now().UTC()
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	if !found || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || current.ShellActivityGeneration != pending.ShellActivityGeneration {
		stream.mu.Unlock()
		return nil
	}
	current.ShellRecoveryState = shellRecoveryStateAbortRequested
	current.ShellRecoveryStateAt = now
	current.ShellRecoveryReason = shellRecoveryReasonForegroundDeadline
	current.ShellRecoveryGeneration = current.ShellActivityGeneration
	stream.PendingExecs[current.ExecID] = current
	stream.UpdatedAt = now
	stream.mu.Unlock()
	if service.broker != nil {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildExecAbortMessage(current)}); err != nil {
			return err
		}
	}
	if service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "shell_abort_requested", map[string]any{
			"tool_call_id":        current.ToolCallID,
			"exec_id":             current.ExecID,
			"message_id":          current.MessageID,
			"provider_pass":       current.ProviderPass,
			"activity_generation": current.ShellActivityGeneration,
			"reason":              shellRecoveryReasonForegroundDeadline,
		})
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellSupervision, current.ExecID),
		shellTerminalRecoveryGrace,
		streamTimerShellSupervision,
		current.ExecID,
		current.MessageID,
		shellRecoveryReasonForegroundDeadline,
	)
	return nil
}

func (service *Service) recoverShellWithoutTerminal(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if stream == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	if !found || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || current.ShellActivityGeneration != pending.ShellActivityGeneration {
		stream.mu.Unlock()
		return nil
	}
	// 只有 PendingExec 上仍登记着与本次收口一致的恢复状态时才允许本地合成终态；
	// 未登记（例如陈旧调用/已被活动复位）一律 no-op，避免误收口仍在运行的 shell。
	switch current.ShellRecoveryState {
	case shellRecoveryStateCandidate:
		if current.ShellRecoveryReason != reason || current.ShellRecoveryGeneration != current.ShellActivityGeneration {
			stream.mu.Unlock()
			return nil
		}
	case shellRecoveryStateAbortRequested:
		if reason != shellRecoveryReasonForegroundDeadline {
			stream.mu.Unlock()
			return nil
		}
	default:
		stream.mu.Unlock()
		return nil
	}
	if tombstone, completed := stream.ShellExecTombstones[pending.ExecID]; completed && tombstone.MessageID == pending.MessageID && tombstone.Generation == pending.ProviderPass {
		stream.mu.Unlock()
		return nil
	}
	// 终态所有权在同一临界区内一次性提交：tombstone、pending 删除和 batch terminal，
	// 消除本地恢复与迟到 Exit 的双收口窗口。
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)
	if stream.ShellExecTombstones == nil {
		stream.ShellExecTombstones = make(map[string]shellExecTombstone)
	}
	if stream.RecentCompletedExecs == nil {
		stream.RecentCompletedExecs = make(map[uint32]time.Time)
	}
	for execID, tombstone := range stream.ShellExecTombstones {
		if tombstone.CompletedAt.Before(cutoff) {
			delete(stream.ShellExecTombstones, execID)
		}
	}
	stream.ShellExecTombstones[pending.ExecID] = shellExecTombstone{MessageID: pending.MessageID, Generation: pending.ProviderPass, CompletedAt: now}
	delete(stream.PendingExecs, pending.ExecID)
	markTaskBatchTerminalLocked(stream, current)
	if pending.MessageID != 0 {
		for messageID, completedAt := range stream.RecentCompletedExecs {
			if completedAt.Before(cutoff) {
				delete(stream.RecentCompletedExecs, messageID)
			}
		}
		stream.RecentCompletedExecs[pending.MessageID] = now
	}
	stream.UpdatedAt = now
	stream.mu.Unlock()

	pending = current
	clearStreamTimer(stream, providerTimerKey(streamTimerShellSupervision, pending.ExecID))
	result := fmt.Sprintf("Shell did not provide a terminal result (%s). The execution was closed locally after a per-command grace period.", reason)
	if reason == shellRecoveryReasonForegroundDeadline {
		result = "Shell timed out: no terminal result arrived before the foreground deadline and an abort was requested. The tool call was closed locally as a timeout; the command outcome is unknown."
	}
	if reason == shellRecoveryReasonSkipped {
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
			"activity_generation": pending.ShellActivityGeneration,
			"recovery_state":      pending.ShellRecoveryState,
			"terminal_owner":      "local_recovery",
			"reason":              reason,
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
