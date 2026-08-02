package forwarder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

const shellTerminalRecoveryGrace = 1500 * time.Millisecond

const (
	shellRecoveryReasonForegroundDeadline = "foreground_deadline"
	shellRecoveryReasonTransportClosed    = "transport_closed"
	shellRecoveryReasonSkipped            = "skipped"
	shellRecoveryReasonPersistenceFailed  = "persistence_failed"
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
	current.StreamState = shellLifecycleUncertain
	stream.PendingExecs[pending.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[pending.ExecID] = current
	}
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

func (service *Service) scheduleShellFinalizationRetry(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	service.scheduleShellRecoveryCandidate(stream.RequestID, pending, shellRecoveryReasonPersistenceFailed)
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

func beginShellFinalization(stream *ActiveStream, pending runtimecore.PendingExec) (runtimecore.PendingExec, bool) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending, true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass {
		return pending, false
	}
	if current.StreamState == shellLifecycleFinalizing || current.ShellTerminalPersisted {
		return current, false
	}
	current.StreamState = shellLifecycleFinalizing
	stream.PendingExecs[pending.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[pending.ExecID] = current
	}
	return current, true
}

func snapshotShellTerminalResult(stream *ActiveStream, pending runtimecore.PendingExec, toolCallID, payload string, toolCall *agentv1.ToolCall) runtimecore.PendingExec {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	var clonedToolCall *agentv1.ToolCall
	if toolCall != nil {
		clonedToolCall, _ = proto.Clone(toolCall).(*agentv1.ToolCall)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass {
		return pending
	}
	current.ShellTerminalSnapshotReady = true
	current.ShellTerminalToolCallID = firstNonEmpty(strings.TrimSpace(toolCallID), strings.TrimSpace(current.ToolCallID))
	current.ShellTerminalResultPayload = payload
	current.ShellTerminalToolCall = clonedToolCall
	stream.PendingExecs[current.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[current.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[current.ExecID] = current
	}
	return current
}

func markShellTerminalPersisted(stream *ActiveStream, pending runtimecore.PendingExec) runtimecore.PendingExec {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return pending
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass {
		return pending
	}
	current.ShellTerminalPersisted = true
	stream.PendingExecs[pending.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[pending.ExecID] = current
	}
	return current
}

func rollbackShellFinalization(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "shell" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || current.ShellTerminalPersisted {
		return
	}
	current.StreamState = pending.StreamState
	stream.PendingExecs[pending.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[pending.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[pending.ExecID] = current
	}
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
	// 候选路径：仅当 PendingExec 上仍登记着同原因、同活动代次的候选时才继续。
	candidateState := current.ShellRecoveryState == shellRecoveryStateCandidate ||
		(reason == shellRecoveryReasonSkipped && current.ShellRecoveryState == shellRecoveryStateAbortRequested)
	if !candidateState ||
		current.ShellRecoveryReason != reason ||
		current.ShellRecoveryGeneration != current.ShellActivityGeneration {
		return nil
	}
	if reason == shellRecoveryReasonSkipped &&
		current.ShellActivityGeneration == 0 &&
		current.ChunkCount == 0 &&
		current.FirstChunkAt.IsZero() &&
		current.ShellAttempt > 0 &&
		current.ShellAttempt < shellMaxTransportAttempts &&
		shellCommandSafeToRetry(current.ArgsJSON) {
		if current.ShellRecoveryState != shellRecoveryStateAbortRequested {
			return service.requestShellAbortForReason(stream, current, shellRecoveryReasonSkipped)
		}
		return service.requeueSkippedForegroundShell(stream, current)
	}
	return service.recoverShellWithoutTerminal(stream, current, reason)
}

// requestShellAbortBeforeRecovery 是 foreground 恢复第一阶段：登记状态并向客户端请求中止，不合成任何终态。
func (service *Service) requestShellAbortBeforeRecovery(stream *ActiveStream, pending runtimecore.PendingExec) error {
	return service.requestShellAbortForReason(stream, pending, shellRecoveryReasonForegroundDeadline)
}

func (service *Service) requestShellAbortForReason(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if service == nil || stream == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	now := time.Now().UTC()
	stream.mu.Lock()
	current, found := stream.PendingExecs[pending.ExecID]
	if !found || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || current.ShellActivityGeneration != pending.ShellActivityGeneration {
		stream.mu.Unlock()
		return nil
	}
	current.ShellRecoveryState = shellRecoveryStateAbortRequested
	current.ShellRecoveryStateAt = now
	current.ShellRecoveryReason = reason
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
			"reason":              reason,
		})
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerShellSupervision, current.ExecID),
		shellTerminalRecoveryGrace,
		streamTimerShellSupervision,
		current.ExecID,
		current.MessageID,
		reason,
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
	if current.StreamState == shellLifecycleFinalizing || current.ShellTerminalPersisted {
		stream.mu.Unlock()
		return nil
	}
	previous := current
	current.StreamState = shellLifecycleFinalizing
	stream.PendingExecs[current.ExecID] = current
	if active, ok := stream.ActiveForegroundShells[current.ExecID]; ok && active.MessageID == current.MessageID {
		stream.ActiveForegroundShells[current.ExecID] = current
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()

	pending = current
	clearStreamTimer(stream, providerTimerKey(streamTimerShellSupervision, pending.ExecID))
	result := fmt.Sprintf("Shell did not provide a terminal result (%s). The execution was closed locally after a per-command grace period.", reason)
	if reason == shellRecoveryReasonForegroundDeadline {
		result = "Shell timed out: no terminal result arrived before the foreground deadline and an abort was requested. The tool call was closed locally as a timeout; the command outcome is unknown."
	}
	silentInspectSkip := false
	if reason == shellRecoveryReasonSkipped {
		result = "Shell execution status is unknown: Cursor reported Skipped and no Start or output event was observed. The command was not automatically replayed unless it was classified as read-only; verify side effects before retrying."
		// 只读 inspect（git status / tasklist 等）被 Cursor 跳过时，UI 用 Rejected 会刷 "Skipped git/tasklist"。
		// 改投影为 backgrounded success，模型仍收到 unknown 文本，checkpoint 与 live UI 不再显示错误态。
		silentInspectSkip = execbridge.IsSafeInspectShellCommand(pending.ArgsJSON)
	}
	toolCallID := pending.ToolCallID
	var completedToolCall *agentv1.ToolCall
	if silentInspectSkip {
		completedToolCall = execbridge.BuildShellSkippedBackgroundedToolCall(toolCallID, pending.ArgsJSON, result)
	} else {
		completedToolCall = execbridge.BuildShellRejectedToolCall(toolCallID, pending.ArgsJSON, result)
	}
	terminalOwner := "local_recovery"
	if silentInspectSkip {
		terminalOwner = "silent_inspect_skip"
	}
	if reason == shellRecoveryReasonPersistenceFailed && pending.ShellTerminalSnapshotReady {
		toolCallID = firstNonEmpty(strings.TrimSpace(pending.ShellTerminalToolCallID), strings.TrimSpace(pending.ToolCallID))
		result = pending.ShellTerminalResultPayload
		completedToolCall = pending.ShellTerminalToolCall
		terminalOwner = "persisted_terminal_retry"
	} else if reason == shellRecoveryReasonPersistenceFailed {
		result = "Shell reached a terminal event, but its original result could not be persisted and no terminal snapshot was available. The execution was not replayed; inspect logs before retrying any command with side effects."
		completedToolCall = execbridge.BuildShellRejectedToolCall(toolCallID, pending.ArgsJSON, result)
	}
	if err := service.appendToolResult(stream, toolCallID, "Shell", pending.ArgsJSON, result, pending.ReasoningContent, completedToolCall); err != nil {
		rollbackShellFinalization(stream, previous)
		service.scheduleShellFinalizationRetry(stream, previous)
		return err
	}
	pending = markShellTerminalPersisted(stream, pending)
	// silent inspect skip 的 backgrounded 是 UI 投影，不是真实后台 shell，不登记 background lease。
	if !silentInspectSkip && shellToolCallIsBackgrounded(completedToolCall) {
		if recordedToolCallID, recorded := recordBackgroundShellActionMemory(stream, toolCallID, time.Now().UTC()); recorded {
			if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
				newBackgroundShellActionMetadataEntry(stream.TurnSeq, stream.RequestID, recordedToolCallID, backgroundShellActionSourceLocalBackgrounded),
			}); err != nil && service.debug != nil {
				service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "background_shell_metadata_failed", map[string]any{
					"tool_call_id": recordedToolCallID,
					"exec_id":      pending.ExecID,
					"error":        err.Error(),
				})
			}
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "shell_stream_recovered", map[string]any{
			"tool_call_id":        toolCallID,
			"logical_shell_id":    pending.LogicalShellID,
			"transport_attempt":   pending.ShellAttempt,
			"message_id":          pending.MessageID,
			"exec_id":             pending.ExecID,
			"generation":          pending.ProviderPass,
			"activity_generation": pending.ShellActivityGeneration,
			"recovery_state":      pending.ShellRecoveryState,
			"terminal_owner":      terminalOwner,
			"reason":              reason,
			"chunk_count":         pending.ChunkCount,
			"stdout_buffer_bytes": len(pending.StdoutBuffer),
			"stderr_buffer_bytes": len(pending.StderrBuffer),
			"silent_inspect_skip": silentInspectSkip,
			"terminal":            true,
		}),
	}); err != nil {
		if service.debug != nil {
			service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "shell_recovery_metadata_failed", map[string]any{
				"tool_call_id": pending.ToolCallID,
				"exec_id":      pending.ExecID,
				"reason":       reason,
				"error":        err.Error(),
			})
		}
	}
	markExecCompleted(stream, pending)
	advanceErr := service.advanceForegroundShellQueue(stream, pending)
	if err := service.publishToolCallCompleted(stream.RequestID, toolCallID, pending.ModelCallID, completedToolCall); err != nil && service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "shell_completed_publish_failed", map[string]any{
			"tool_call_id": toolCallID,
			"exec_id":      pending.ExecID,
			"error":        err.Error(),
		})
	}
	if advanceErr != nil {
		return advanceErr
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, pending.ModelCallID); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}
