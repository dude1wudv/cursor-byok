package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	subagentDispatchLimit       = 4
	subagentMaximumDepth        = 3
	subagentDispatchReservation = "subagent_dispatch_reserved"
)

type pendingSubagentLaunch struct {
	ParentConversationID string
	ParentToolCallID     string
	RootConversationID   string
	SubagentType         string
	ModelID              string
	PromptHash           string
	ThinkingEffort       string
	CreatedAt            time.Time
}

type subagentDispatchDecision struct {
	Depth        int
	Used         int
	Limit        int
	QuotaScope   string
	SubagentType string
	Resume       bool
	Duplicate    bool
}

func (service *Service) validateAndReserveSubagentDispatch(stream *ActiveStream, invocation runtimecore.ToolInvocation) (subagentDispatchDecision, error) {
	decision := subagentDispatchDecision{Limit: subagentDispatchLimit}
	if service == nil || stream == nil {
		return decision, fmt.Errorf("subagent dispatch context is unavailable")
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return decision, fmt.Errorf("decode Task args: %w", err)
	}
	decision.SubagentType = readStringMapValue(args, "subagent_type", "subagentType")
	requestedThinkingEffort := normalizeTaskThinkingEffort(readStringMapValue(args, "thinking_effort", "thinkingEffort"))
	callID := strings.TrimSpace(invocation.CallID)
	if callID == "" {
		return decision, fmt.Errorf("Task tool_call_id is required")
	}
	if strings.TrimSpace(readStringMapValue(args, "resume", "resume_agent_id", "resumeAgentId")) != "" {
		decision.Resume = true
		decision.QuotaScope = "resume"
		return decision, nil
	}

	stream.mu.Lock()
	conversation := cloneConversationFile(stream.CheckpointConversation)
	turnSeq := stream.TurnSeq
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	parentThinkingEffort := normalizeTaskThinkingEffort(stream.ThinkingEffort)
	stream.mu.Unlock()
	if conversation == nil {
		return decision, fmt.Errorf("subagent dispatch conversation is unavailable")
	}
	depth, err := service.resolveSubagentDepth(conversation)
	if err != nil {
		return decision, err
	}
	decision.Depth = depth
	if depth >= subagentMaximumDepth {
		return decision, fmt.Errorf("subagent depth limit reached: maximum depth is %d", subagentMaximumDepth)
	}
	decision.QuotaScope = "conversation"
	if depth == 1 {
		decision.QuotaScope = "root_turn"
	}
	used, duplicate := countSubagentDispatchReservations(conversation.Entries, invocation.CallID, depth == 1, turnSeq)
	decision.Used = used
	decision.Duplicate = duplicate
	if duplicate {
		return decision, nil
	}
	if used >= subagentDispatchLimit {
		if depth == 1 {
			return decision, fmt.Errorf("Task limit reached: %d direct subagents per root turn", subagentDispatchLimit)
		}
		return decision, fmt.Errorf("Task limit reached: %d direct subagents per subagent conversation", subagentDispatchLimit)
	}
	effectiveThinkingEffort := requestedThinkingEffort
	if effectiveThinkingEffort == "" {
		effectiveThinkingEffort = parentThinkingEffort
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, subagentDispatchReservation, map[string]any{
			"tool_call_id":              strings.TrimSpace(invocation.CallID),
			"turn_seq":                  turnSeq,
			"parent_conversation_id":    strings.TrimSpace(conversationID),
			"depth":                     depth,
			"subagent_type":             strings.TrimSpace(decision.SubagentType),
			"requested_model_id":        readStringMapValue(args, "model", "model_id", "modelId"),
			"requested_thinking_effort": requestedThinkingEffort,
			"effective_thinking_effort": effectiveThinkingEffort,
			"quota_scope":               decision.QuotaScope,
		}),
	}); err != nil {
		return decision, fmt.Errorf("reserve subagent dispatch: %w", err)
	}
	service.registerPendingSubagentLaunch(pendingSubagentLaunch{
		ParentConversationID: strings.TrimSpace(conversationID),
		ParentToolCallID:     strings.TrimSpace(invocation.CallID),
		RootConversationID:   strings.TrimSpace(conversation.RootConversationID),
		SubagentType:         strings.TrimSpace(decision.SubagentType),
		ModelID:              readStringMapValue(args, "model", "model_id", "modelId"),
		PromptHash:           planContentHash(readStringMapValue(args, "prompt")),
		ThinkingEffort:       effectiveThinkingEffort,
		CreatedAt:            time.Now().UTC(),
	})
	decision.Used++
	return decision, nil
}

func (service *Service) resolveSubagentDepth(conversation *ConversationFile) (int, error) {
	if conversation == nil {
		return 0, fmt.Errorf("subagent conversation is unavailable")
	}
	current := conversation
	seen := make(map[string]struct{}, subagentMaximumDepth)
	depth := 1
	for {
		conversationID := strings.TrimSpace(current.ConversationID)
		if conversationID == "" {
			return 0, fmt.Errorf("subagent conversation_id is required")
		}
		if _, exists := seen[conversationID]; exists {
			return 0, fmt.Errorf("subagent parent chain contains a cycle")
		}
		seen[conversationID] = struct{}{}
		parentID := strings.TrimSpace(current.ParentConversationID)
		if parentID == "" {
			if depth == 1 && isChildConversationSubagentTypeName(current.SubagentTypeName) {
				return 0, fmt.Errorf("subagent parent conversation is missing")
			}
			return depth, nil
		}
		depth++
		if depth > subagentMaximumDepth {
			return depth, fmt.Errorf("subagent depth limit reached: maximum depth is %d", subagentMaximumDepth)
		}
		if service.store == nil {
			return 0, fmt.Errorf("subagent parent conversation store is unavailable")
		}
		parent, err := service.store.LoadConversation(parentID)
		if err != nil {
			return 0, fmt.Errorf("load subagent parent conversation: %w", err)
		}
		if parent == nil {
			return 0, fmt.Errorf("subagent parent conversation %q was not found", parentID)
		}
		current = parent
	}
}

func (service *Service) registerPendingSubagentLaunch(launch pendingSubagentLaunch) {
	if service == nil || launch.PromptHash == "" {
		return
	}
	service.subagentLaunchMu.Lock()
	defer service.subagentLaunchMu.Unlock()
	cutoff := time.Now().UTC().Add(-subagentExecutionTimeout)
	retained := service.pendingSubagents[:0]
	for _, item := range service.pendingSubagents {
		if item.CreatedAt.After(cutoff) {
			retained = append(retained, item)
		}
	}
	service.pendingSubagents = append(retained, launch)
}

func (service *Service) consumePendingSubagentLaunch(subagentType string, modelID string, prompt string) (pendingSubagentLaunch, bool) {
	if service == nil {
		return pendingSubagentLaunch{}, false
	}
	promptHash := planContentHash(prompt)
	if promptHash == "" {
		return pendingSubagentLaunch{}, false
	}
	normalizedType := strings.TrimSpace(subagentType)
	normalizedModel := strings.TrimSpace(modelID)
	service.subagentLaunchMu.Lock()
	defer service.subagentLaunchMu.Unlock()
	cutoff := time.Now().UTC().Add(-subagentExecutionTimeout)
	for index, item := range service.pendingSubagents {
		if item.CreatedAt.Before(cutoff) || item.PromptHash != promptHash || !strings.EqualFold(item.SubagentType, normalizedType) {
			continue
		}
		if isConcreteTaskModelSelection(item.ModelID) && !strings.EqualFold(item.ModelID, normalizedModel) {
			continue
		}
		service.pendingSubagents = append(service.pendingSubagents[:index], service.pendingSubagents[index+1:]...)
		return item, true
	}
	return pendingSubagentLaunch{}, false
}

func normalizeTaskThinkingEffort(raw string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(raw)); normalized {
	case "disabled", "low", "medium", "high", "xhigh", "max":
		return normalized
	default:
		return ""
	}
}

func taskThinkingEffortDisplayName(effort string) string {
	switch normalizeTaskThinkingEffort(effort) {
	case "disabled":
		return "Disabled"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "XHigh"
	case "max":
		return "Max"
	default:
		return ""
	}
}

func (service *Service) resolveSubagentDispatchThinkingEffort(conversation *ConversationFile) string {
	if service == nil || service.store == nil || conversation == nil {
		return ""
	}
	parentConversationID := strings.TrimSpace(conversation.ParentConversationID)
	parentToolCallID := strings.TrimSpace(conversation.ParentToolCallID)
	if parentConversationID == "" || parentToolCallID == "" {
		return ""
	}
	parent, err := service.store.LoadConversation(parentConversationID)
	if err != nil || parent == nil {
		return ""
	}
	for index := len(parent.Entries) - 1; index >= 0; index-- {
		entry := parent.Entries[index]
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Type) != subagentDispatchReservation {
			continue
		}
		if strings.TrimSpace(readStringValue(payload.Value["tool_call_id"])) != parentToolCallID {
			continue
		}
		return normalizeTaskThinkingEffort(readStringValue(payload.Value["effective_thinking_effort"]))
	}
	return ""
}

func countSubagentDispatchReservations(entries []HistoryEntry, callID string, currentTurnOnly bool, turnSeq int64) (int, bool) {
	seen := make(map[string]struct{})
	duplicate := false
	callID = strings.TrimSpace(callID)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "metadata" || currentTurnOnly && entry.TurnSeq != turnSeq {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil || strings.TrimSpace(payload.Type) != subagentDispatchReservation {
			continue
		}
		reservedCallID := strings.TrimSpace(readStringValue(payload.Value["tool_call_id"]))
		if reservedCallID == "" {
			continue
		}
		seen[reservedCallID] = struct{}{}
		if callID != "" && reservedCallID == callID {
			duplicate = true
		}
	}
	return len(seen), duplicate
}

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
