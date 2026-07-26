package forwarder

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

// 非 shell/subagent 的客户端执行工具（Read/Grep/Glob/MCP 等）派发后没有流式活动信号，
// 客户端不回包时回合会永久停在 waiting_external。这里为它们补一层结果超时 watchdog：
// 超时后本地合成错误 tool_result 收口并继续回合。shell 有 foreground deadline 两阶段
// 恢复、subagent 有 lease 超时，均不走此路径。
const (
	defaultExecResultTimeout = 2 * time.Minute
	// mcpExecResultTimeout 放宽 MCP 工具：外部 server 可能合法地长时间执行。
	mcpExecResultTimeout = 10 * time.Minute
	// machineInteractionResultTimeout 约束客户端机器执行的交互工具（web_search/web_fetch/switch_mode）。
	machineInteractionResultTimeout = 3 * time.Minute
	// humanInteractionResultTimeout 约束等待人工输入的交互（ask_question/create_plan）——
	// 给足人类决策时间，仅兜底客户端丢失交互后的永久挂起。
	humanInteractionResultTimeout = 30 * time.Minute
)

const execResultTimeoutReason = "result_timeout"

func execResultTimeoutDuration(execKind string) time.Duration {
	switch strings.TrimSpace(execKind) {
	case "shell", "subagent":
		return 0
	case "mcp", "list_mcp_resources", "read_mcp_resource":
		return mcpExecResultTimeout
	default:
		return defaultExecResultTimeout
	}
}

func interactionResultTimeoutDuration(interactionKind string) time.Duration {
	switch strings.TrimSpace(interactionKind) {
	case "ask_question", "create_plan":
		return humanInteractionResultTimeout
	default:
		return machineInteractionResultTimeout
	}
}

func (service *Service) scheduleExecResultTimeout(requestID string, pending runtimecore.PendingExec) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.ExecID) == "" {
		return
	}
	timeout := execResultTimeoutDuration(pending.ExecKind)
	if timeout <= 0 {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerExecResult, pending.ExecID),
		timeout,
		streamTimerExecResult,
		pending.ExecID,
		pending.MessageID,
		execResultTimeoutReason,
	)
}

func (service *Service) scheduleInteractionResultTimeout(requestID string, pending runtimecore.PendingInteraction) {
	if service == nil || strings.TrimSpace(requestID) == "" || strings.TrimSpace(pending.InteractionID) == "" {
		return
	}
	timeout := interactionResultTimeoutDuration(pending.InteractionKind)
	if timeout <= 0 {
		return
	}
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerInteractionResult, pending.InteractionID),
		timeout,
		streamTimerInteractionResult,
		pending.InteractionID,
		0,
		strings.TrimSpace(pending.InteractionKind),
	)
}

// recoverExecAfterResultTimeout 在结果超时后本地收口 pending exec。正常结果先到时
// pending 已被删除，这里的再校验保证迟到的 timer 是 no-op。
func (service *Service) recoverExecAfterResultTimeout(stream *ActiveStream, execID string, messageID uint32) error {
	if service == nil || stream == nil {
		return nil
	}
	current, status, found := snapshotPendingExecWithStatus(stream, execID)
	if !found || current.MessageID != messageID || isTerminalStreamStatus(status) {
		return nil
	}
	timeout := execResultTimeoutDuration(current.ExecKind)
	if timeout <= 0 {
		return nil
	}
	markExecCompleted(stream, current)
	if service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "exec_result_timeout", map[string]any{
			"tool_call_id": current.ToolCallID,
			"exec_id":      current.ExecID,
			"message_id":   current.MessageID,
			"exec_kind":    current.ExecKind,
			"timeout":      timeout.String(),
		})
	}
	if isHiddenPatchEditExecKind(current.ExecKind) {
		return service.finishHiddenPatchEditAfterTimeout(stream, current)
	}
	toolName := strings.TrimSpace(deriveToolNameFromPendingExec(current))
	resultPayload := fmt.Sprintf(
		"%s timed out: no client result arrived within %s. The tool call was closed locally as a timeout; the actual outcome is unknown.",
		firstNonEmpty(toolName, current.ExecKind, "tool"), timeout,
	)
	if toolName != "" {
		if err := service.appendToolResult(stream, current.ToolCallID, toolName, current.ArgsJSON, resultPayload, current.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "tool_result_timeout", map[string]any{
			"tool_call_id": current.ToolCallID,
			"message_id":   current.MessageID,
			"exec_id":      current.ExecID,
			"exec_kind":    current.ExecKind,
			"timeout":      timeout.String(),
			"payload":      resultPayload,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, current.ModelCallID); err != nil {
		return err
	}
	if err := service.publishToolCallCompleted(stream.RequestID, current.ToolCallID, current.ModelCallID, nil); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

// finishHiddenPatchEditAfterTimeout 与 stream-close 控制消息的收口语义保持一致：
// post-read 只是写后校验读，超时按已写入内容收口为成功；read/write 阶段超时收口为错误。
func (service *Service) finishHiddenPatchEditAfterTimeout(stream *ActiveStream, pending runtimecore.PendingExec) error {
	payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
	if err != nil {
		return err
	}
	if strings.TrimSpace(pending.ExecKind) == patchEditPostReadExecKindName {
		return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildFinalEditSuccessResult(payload.ResolvedPath, payload.AfterContent, patchEditPayloadAsEditPayload(payload)))
	}
	return service.finishPatchEditOperation(stream, pending.ToolCallID, pending.ModelCallID, pending.ProviderPass, pending.ReasoningContent, payload, buildEditErrorResult(payload.ResolvedPath, fmt.Sprintf("patch edit %s timed out without a client result", strings.TrimSpace(pending.ExecKind))))
}

// cancelInteractionAfterResultTimeout 在交互超时后取消该交互并合成超时 tool_result，
// 使回合继续而非静默挂起。用户随后的迟到响应会因 pending 缺失被按失配记录丢弃。
func (service *Service) cancelInteractionAfterResultTimeout(stream *ActiveStream, interactionID string) error {
	if service == nil || stream == nil || strings.TrimSpace(interactionID) == "" {
		return nil
	}
	stream.mu.Lock()
	pending, found := stream.PendingInteractions[strings.TrimSpace(interactionID)]
	status := stream.Status
	stream.mu.Unlock()
	if !found || isTerminalStreamStatus(status) {
		return nil
	}
	markInteractionCompleted(stream, pending)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	timeout := interactionResultTimeoutDuration(pending.InteractionKind)
	resultPayload := fmt.Sprintf(
		"%s interaction timed out: no client response arrived within %s. The interaction was canceled locally.",
		firstNonEmpty(toolName, pending.InteractionKind, "interaction"), timeout,
	)
	if service.debug != nil {
		service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, "interaction_result_timeout", map[string]any{
			"tool_call_id":     pending.ToolCallID,
			"interaction_id":   pending.InteractionID,
			"interaction_kind": pending.InteractionKind,
			"timeout":          timeout.String(),
		})
	}
	if toolName != "" {
		if err := service.appendToolResult(stream, pending.ToolCallID, toolName, pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil); err != nil {
			return err
		}
	}
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "interaction_result_timeout", map[string]any{
			"tool_call_id":     pending.ToolCallID,
			"interaction_id":   pending.InteractionID,
			"interaction_kind": pending.InteractionKind,
			"timeout":          timeout.String(),
			"payload":          resultPayload,
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
