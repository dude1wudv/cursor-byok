package forwarder

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

const (
	subagentDispatchLimit                = 4
	subagentMaximumDepth                 = 3
	subagentDispatchReservation          = "subagent_dispatch_reserved"
	subagentDispatchEffective            = "subagent_dispatch_effective"
	subagentDispatchClosed               = "subagent_dispatch_closed"
	subagentParentTodoReconciled         = "subagent_parent_todo_reconciled"
	subagentRunStateMetadata             = "subagent_run_state"
	subagentTimeoutReasonInactivityLease = "inactivity lease expired"
	subagentTimeoutReasonMaximumRuntime  = "maximum runtime exceeded"
)

type pendingSubagentLaunch struct {
	ParentConversationID string
	ParentToolCallID     string
	RootConversationID   string
	SubagentType         string
	SubagentRole         string
	ModelID              string
	PromptHash           string
	ThinkingEffort       string
	PlanText             string
	Plans                map[string]*agentv1.PlanRegistryEntry
	Todos                []*agentv1.TodoItem
	PlanHash             string
	PlanVersion          int64
	PlanFilePath         string
	PlanFileURI          string
	OwnedPaths           []string
	RelatedPaths         []string
	UserContextSummary   string
	ParentTodoID         string
	DispatchID           string
	CreatedAt            time.Time
}

type subagentDispatchDecision struct {
	Depth                   int
	Used                    int
	Limit                   int
	QuotaScope              string
	SubagentType            string
	SubagentRole            string
	EffectiveThinkingEffort string
	EffectiveModelID        string
	Resume                  bool
	Duplicate               bool
}

type projectedSubagentRun struct {
	Generation int64
	State      *agentv1.SubagentRunState
}

func subagentRunStatusName(status agentv1.SubagentRunStatus) string {
	return strings.TrimPrefix(status.String(), "SUBAGENT_RUN_STATUS_")
}

func parseSubagentRunStatus(value string) agentv1.SubagentRunStatus {
	return agentv1.SubagentRunStatus(agentv1.SubagentRunStatus_value["SUBAGENT_RUN_STATUS_"+strings.ToUpper(strings.TrimSpace(value))])
}

func subagentRunStatusRank(status agentv1.SubagentRunStatus) int {
	switch status {
	case agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_RUNNING:
		return 1
	case agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED:
		return 2
	case agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS,
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR,
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ABORTED:
		return 3
	default:
		return 0
	}
}

func subagentRunStateMetadataEntry(turnSeq int64, requestID string, pending runtimecore.PendingExec, status agentv1.SubagentRunStatus, subagentID string, detail string) HistoryEntry {
	values := map[string]any{
		"tool_call_id": pending.ToolCallID,
		"exec_id":      pending.ExecID,
		"message_id":   pending.MessageID,
		"generation":   pending.ProviderPass,
		"status":       subagentRunStatusName(status),
		"environment":  "LOCAL",
	}
	if value := strings.TrimSpace(subagentID); value != "" {
		values["subagent_id"] = value
	}
	if value := strings.TrimSpace(detail); value != "" {
		values["detail"] = value
	}
	if status == agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS || status == agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR || status == agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ABORTED {
		values["completed_timestamp_ms"] = time.Now().UTC().UnixMilli()
	}
	return newMetadataEntry(turnSeq, requestID, subagentRunStateMetadata, values)
}

func (service *Service) recordSubagentRunState(stream *ActiveStream, pending runtimecore.PendingExec, status agentv1.SubagentRunStatus, subagentID string, detail string) error {
	if service == nil || stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ToolCallID) == "" {
		return nil
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		subagentRunStateMetadataEntry(stream.TurnSeq, stream.RequestID, pending, status, subagentID, detail),
	})
	return err
}

func projectSubagentRunStates(entries []HistoryEntry) map[string]*agentv1.SubagentRunState {
	projected := make(map[string]projectedSubagentRun)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) != nil || strings.TrimSpace(payload.Type) != subagentRunStateMetadata {
			continue
		}
		toolCallID := strings.TrimSpace(readStringValue(payload.Value["tool_call_id"]))
		status := parseSubagentRunStatus(readStringValue(payload.Value["status"]))
		generation := int64Value(payload.Value["generation"])
		if toolCallID == "" || status == agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_UNSPECIFIED {
			continue
		}
		if previous, ok := projected[toolCallID]; ok {
			if generation < previous.Generation {
				continue
			}
			if generation == previous.Generation && subagentRunStatusRank(status) <= subagentRunStatusRank(previous.State.GetStatus()) {
				continue
			}
		}
		state := &agentv1.SubagentRunState{
			ParentToolCallId: toolCallID,
			Environment:      agentv1.SubagentExecutionEnvironment_SUBAGENT_EXECUTION_ENVIRONMENT_LOCAL,
			Status:           status,
		}
		if value := strings.TrimSpace(readStringValue(payload.Value["subagent_id"])); value != "" {
			state.SubagentId = &value
		}
		if value := strings.TrimSpace(readStringValue(payload.Value["detail"])); value != "" {
			state.Detail = &value
		}
		if completed := uint64(int64Value(payload.Value["completed_timestamp_ms"])); completed != 0 {
			state.CompletedTimestampMs = &completed
		}
		projected[toolCallID] = projectedSubagentRun{Generation: generation, State: state}
	}
	if len(projected) == 0 {
		return nil
	}
	result := make(map[string]*agentv1.SubagentRunState, len(projected))
	for toolCallID, item := range projected {
		result[toolCallID] = item.State
	}
	return result
}

func normalizeSubagentRole(value string) string {
	switch strings.TrimSpace(value) {
	case "simple_explore", "medium_explore", "complex_debug":
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func defaultTaskThinkingEffort(role string) string {
	switch normalizeSubagentRole(role) {
	case "simple_explore":
		return "low"
	case "medium_explore", "complex_debug":
		return "medium"
	default:
		return ""
	}
}

func latestSubagentRole(conversation *ConversationFile) string {
	if conversation == nil {
		return ""
	}
	if role := normalizeSubagentRole(conversation.SubagentRole); role != "" {
		return role
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if entry.Kind != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) != nil {
			continue
		}
		if role := normalizeSubagentRole(readStringValue(payload.Value["task_role"])); role != "" {
			return role
		}
	}
	return ""
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
	rawTaskRole := readStringMapValue(args, "task_role", "taskRole")
	decision.SubagentRole = normalizeSubagentRole(rawTaskRole)
	if strings.TrimSpace(rawTaskRole) != "" && decision.SubagentRole == "" {
		return decision, fmt.Errorf("unsupported task_role %q", rawTaskRole)
	}
	requestedModelID := readStringMapValue(args, "model", "model_id", "modelId")
	if decision.SubagentRole != "" {
		resolvedModelID, modelErr := service.resolveTaskModelID(decision.SubagentRole, requestedModelID)
		if modelErr != nil {
			return decision, modelErr
		}
		decision.EffectiveModelID = resolvedModelID
	}
	requestedThinkingEffort := normalizeTaskThinkingEffort(readStringMapValue(args, "thinking_effort", "thinkingEffort"))
	callID := strings.TrimSpace(invocation.CallID)
	if callID == "" {
		return decision, fmt.Errorf("Task tool_call_id is required")
	}
	resume := strings.TrimSpace(readStringMapValue(args, "resume", "resume_agent_id", "resumeAgentId")) != ""

	stream.mu.Lock()
	conversation := cloneConversationFile(stream.CheckpointConversation)
	turnSeq := stream.TurnSeq
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	parentThinkingEffort := normalizeTaskThinkingEffort(stream.ThinkingEffort)
	latestUserText := strings.TrimSpace(stream.LatestUserText)
	stream.mu.Unlock()
	if conversation == nil {
		return decision, fmt.Errorf("subagent dispatch conversation is unavailable")
	}
	depth, err := service.resolveSubagentDepth(conversation)
	if err != nil {
		return decision, err
	}
	decision.Depth = depth
	parentRole := ""
	if depth > 1 {
		parentRole = latestSubagentRole(conversation)
		switch parentRole {
		case "complex_debug":
			if decision.SubagentRole != "simple_explore" && decision.SubagentRole != "medium_explore" {
				return decision, fmt.Errorf("complex_debug subagents may only dispatch simple_explore or medium_explore")
			}
		case "medium_explore":
			decision.Limit = 1
			if decision.SubagentType != runtimecore.SubagentTypeLongContextRead && decision.SubagentType != "explore" {
				return decision, fmt.Errorf("medium_explore subagents may only dispatch longContextRead or explore")
			}
			if decision.SubagentRole != "simple_explore" && decision.SubagentRole != "medium_explore" {
				return decision, fmt.Errorf("medium_explore nested Task task_role must be simple_explore or medium_explore")
			}
			readonlyValue, found := args["readonly"]
			if !found {
				readonlyValue, found = args["readOnly"]
			}
			readonly, ok := readonlyValue.(bool)
			if !found || !ok || !readonly {
				return decision, fmt.Errorf("medium_explore nested Task must explicitly set readonly=true")
			}
		default:
			return decision, fmt.Errorf("only medium_explore or complex_debug subagents may dispatch nested Task calls")
		}
	}
	if depth >= subagentMaximumDepth {
		return decision, fmt.Errorf("subagent depth limit reached: maximum depth is %d", subagentMaximumDepth)
	}
	if resume {
		decision.Resume = true
		decision.QuotaScope = "resume"
		return decision, nil
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
	if used >= decision.Limit {
		if depth == 1 {
			return decision, fmt.Errorf("Task limit reached: %d direct subagents per root turn", decision.Limit)
		}
		return decision, fmt.Errorf("Task limit reached: %d direct subagents per subagent conversation", decision.Limit)
	}
	effectiveThinkingEffort := requestedThinkingEffort
	if effectiveThinkingEffort == "" {
		effectiveThinkingEffort = defaultTaskThinkingEffort(decision.SubagentRole)
		if effectiveThinkingEffort == "" {
			effectiveThinkingEffort = parentThinkingEffort
		}
	}
	decision.EffectiveThinkingEffort = effectiveThinkingEffort
	planHash := subagentParentPlanHash(conversation)
	parentTodoID := matchSubagentParentTodo(conversation.CurrentTodos, args)
	planVersion := conversation.ContextVersion
	planFilePath := strings.TrimSpace(readStringMapValue(args, "plan_file_path", "planPath"))
	planFileURI := strings.TrimSpace(readStringMapValue(args, "plan_file_uri", "planUri"))
	if planFilePath == "" && planFileURI == "" {
		planFilePath, planFileURI = subagentPlanFileHints(conversation)
	}
	ownedPaths := normalizeSubagentPathList(readStringSliceMapValue(args, "owned_paths", "ownedPaths"))
	relatedPaths := normalizeSubagentPathList(readStringSliceMapValue(args, "related_paths", "relatedPaths"))
	userContextSummary := strings.TrimSpace(readStringMapValue(args, "user_context_summary", "userContextSummary"))
	if userContextSummary == "" {
		userContextSummary = truncateSubagentContextSummary(latestUserText)
	}
	dispatchID := strings.TrimSpace(invocation.CallID)
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, subagentDispatchReservation, map[string]any{
			"tool_call_id":              strings.TrimSpace(invocation.CallID),
			"dispatch_id":               dispatchID,
			"turn_seq":                  turnSeq,
			"parent_conversation_id":    strings.TrimSpace(conversationID),
			"parent_todo_id":            parentTodoID,
			"plan_hash":                 planHash,
			"plan_version":              planVersion,
			"plan_file_path":            planFilePath,
			"plan_file_uri":             planFileURI,
			"owned_paths":               ownedPaths,
			"related_paths":             relatedPaths,
			"user_context_summary":      userContextSummary,
			"depth":                     depth,
			"subagent_type":             strings.TrimSpace(decision.SubagentType),
			"task_role":                 strings.TrimSpace(decision.SubagentRole),
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
		SubagentRole:         strings.TrimSpace(decision.SubagentRole),
		ModelID:              firstNonEmpty(decision.EffectiveModelID, readStringMapValue(args, "model", "model_id", "modelId")),
		PromptHash:           planContentHash(readStringMapValue(args, "prompt")),
		ThinkingEffort:       effectiveThinkingEffort,
		PlanText:             strings.TrimSpace(conversation.CurrentPlanText),
		Plans:                clonePlanRegistryEntries(conversation.CurrentPlans),
		Todos:                cloneTodoItems(conversation.CurrentTodos),
		PlanHash:             planHash,
		PlanVersion:          planVersion,
		PlanFilePath:         planFilePath,
		PlanFileURI:          planFileURI,
		OwnedPaths:           append([]string(nil), ownedPaths...),
		RelatedPaths:         append([]string(nil), relatedPaths...),
		UserContextSummary:   userContextSummary,
		ParentTodoID:         parentTodoID,
		DispatchID:           dispatchID,
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
	cutoff := time.Now().UTC().Add(-subagentLaunchCorrelationTTL)
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
	cutoff := time.Now().UTC().Add(-subagentLaunchCorrelationTTL)
	for index, item := range service.pendingSubagents {
		if item.CreatedAt.Before(cutoff) || item.PromptHash != promptHash || !strings.EqualFold(item.SubagentType, normalizedType) {
			continue
		}
		if isConcreteTaskModelSelection(item.ModelID) && !strings.EqualFold(item.ModelID, normalizedModel) {
			continue
		}
		service.pendingSubagents = append(service.pendingSubagents[:index], service.pendingSubagents[index+1:]...)
		item.Plans = clonePlanRegistryEntries(item.Plans)
		item.Todos = cloneTodoItems(item.Todos)
		item.OwnedPaths = append([]string(nil), item.OwnedPaths...)
		item.RelatedPaths = append([]string(nil), item.RelatedPaths...)
		return item, true
	}
	return pendingSubagentLaunch{}, false
}

func subagentPlanFileHints(conversation *ConversationFile) (string, string) {
	if conversation == nil {
		return "", ""
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		content := strings.TrimSpace(payload.Content)
		if content == "" {
			continue
		}
		path := ""
		uri := ""
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "plan_file_path:") {
				path = strings.TrimSpace(strings.TrimPrefix(line, "plan_file_path:"))
			}
			if strings.HasPrefix(line, "plan_file_uri:") {
				uri = strings.TrimSpace(strings.TrimPrefix(line, "plan_file_uri:"))
			}
		}
		if path != "" || uri != "" {
			return path, uri
		}
	}
	return "", ""
}

func truncateSubagentContextSummary(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= 600 {
		return value
	}
	return string([]rune(value)[:600]) + "…"
}

func readStringSliceMapValue(values map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		items := make([]string, 0)
		switch typed := value.(type) {
		case []string:
			items = append(items, typed...)
		case []any:
			for _, item := range typed {
				if text, ok := item.(string); ok {
					items = append(items, text)
				}
			}
		case string:
			items = strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func normalizeSubagentPathList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func subagentDispatchContextContent(launch pendingSubagentLaunch) string {
	parts := make([]string, 0, 5)
	if launch.PlanFilePath != "" || launch.PlanFileURI != "" {
		parts = append(parts, "plan_file_snapshot:\n"+firstNonEmpty(launch.PlanFilePath, launch.PlanFileURI))
		if launch.PlanFilePath != "" && launch.PlanFileURI != "" {
			parts[len(parts)-1] += "\nplan_file_uri: " + launch.PlanFileURI
		}
	}
	if len(launch.OwnedPaths) > 0 {
		parts = append(parts, "owned_paths:\n- "+strings.Join(launch.OwnedPaths, "\n- "))
	}
	if len(launch.RelatedPaths) > 0 {
		parts = append(parts, "related_paths:\n- "+strings.Join(launch.RelatedPaths, "\n- "))
	}
	if launch.UserContextSummary != "" {
		parts = append(parts, "user_context_summary:\n"+launch.UserContextSummary)
	}
	if len(parts) == 0 {
		return ""
	}
	return "<subagent_dispatch_context>\n" + strings.Join(parts, "\n\n") + "\n\nThese values are immutable dispatch-time snapshots. Paths are locating hints; use the plan text snapshot as the plan source of truth and do not infer later plan updates from file changes.\n</subagent_dispatch_context>"
}

func subagentLaunchBootstrapEntries(launch pendingSubagentLaunch, turnSeq int64, requestID string) ([]HistoryEntry, error) {
	entries := make([]HistoryEntry, 0, 3)
	if strings.TrimSpace(launch.PlanText) != "" || len(launch.Plans) > 0 || len(launch.Todos) > 0 {
		payload, err := json.Marshal(runtimeStateEntryPayload{
			PlanText: strings.TrimSpace(launch.PlanText),
			Plans:    clonePlanRegistryEntries(launch.Plans),
			Todos:    cloneTodoItems(launch.Todos),
		})
		if err != nil {
			return nil, fmt.Errorf("encode subagent parent plan snapshot: %w", err)
		}
		entries = append(entries, HistoryEntry{
			TurnSeq:   turnSeq,
			RequestID: strings.TrimSpace(requestID),
			Role:      "system",
			Kind:      "runtime_state",
			Payload:   payload,
		})
	}
	if content := subagentDispatchContextContent(launch); content != "" {
		entries = append(entries, newPromptContextEntry(turnSeq, requestID, newPromptContextMessage(
			"subagent_dispatch_context",
			modeladapter.Message{Role: "system", Content: content},
			true,
		)))
	}
	entries = append(entries, newMetadataEntry(turnSeq, requestID, "subagent_parent_plan_snapshot", map[string]any{
		"parent_conversation_id": strings.TrimSpace(launch.ParentConversationID),
		"parent_tool_call_id":    strings.TrimSpace(launch.ParentToolCallID),
		"parent_todo_id":         strings.TrimSpace(launch.ParentTodoID),
		"dispatch_id":            strings.TrimSpace(launch.DispatchID),
		"plan_hash":              strings.TrimSpace(launch.PlanHash),
		"plan_version":           launch.PlanVersion,
	}))
	return entries, nil
}

func subagentParentPlanHash(conversation *ConversationFile) string {
	if conversation == nil {
		return ""
	}
	payload, err := json.Marshal(struct {
		PlanText string                                `json:"plan_text,omitempty"`
		Plans    map[string]*agentv1.PlanRegistryEntry `json:"plans,omitempty"`
	}{
		PlanText: strings.TrimSpace(conversation.CurrentPlanText),
		Plans:    clonePlanRegistryEntries(conversation.CurrentPlans),
	})
	if err != nil || (strings.TrimSpace(conversation.CurrentPlanText) == "" && len(conversation.CurrentPlans) == 0) {
		return ""
	}
	return planContentHash(string(payload))
}

func matchSubagentParentTodo(todos []*agentv1.TodoItem, args map[string]any) string {
	if len(todos) == 0 {
		return ""
	}
	description := strings.ToLower(readStringMapValue(args, "description"))
	prompt := strings.ToLower(readStringMapValue(args, "prompt"))
	for _, todo := range todos {
		if todo == nil || isTerminalTodoStatus(todo.GetStatus()) {
			continue
		}
		id := strings.TrimSpace(todo.GetId())
		if id != "" && (containsTaskIdentifier(description, id) || containsTaskIdentifier(prompt, id)) {
			return id
		}
	}
	inProgressID := ""
	for _, todo := range todos {
		if todo == nil || todo.GetStatus() != agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS {
			continue
		}
		if inProgressID != "" {
			return ""
		}
		inProgressID = strings.TrimSpace(todo.GetId())
	}
	return inProgressID
}

func containsTaskIdentifier(text string, id string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	id = strings.ToLower(strings.TrimSpace(id))
	if text == "" || id == "" {
		return false
	}
	for offset := 0; ; {
		index := strings.Index(text[offset:], id)
		if index < 0 {
			return false
		}
		index += offset
		leftOK := index == 0 || !isTaskIdentifierRune(rune(text[index-1]))
		right := index + len(id)
		rightOK := right == len(text) || !isTaskIdentifierRune(rune(text[right]))
		if leftOK && rightOK {
			return true
		}
		offset = index + len(id)
		if offset >= len(text) {
			return false
		}
	}
}

func isTaskIdentifierRune(value rune) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '_' || value == '-'
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

func rewriteTaskInvocationModel(invocation runtimecore.ToolInvocation, modelID string) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "Task" || strings.TrimSpace(modelID) == "" {
		return invocation
	}
	var args map[string]any
	if json.Unmarshal(invocation.ArgsJSON, &args) != nil {
		return invocation
	}
	args["model"] = strings.TrimSpace(modelID)
	if encoded, err := json.Marshal(args); err == nil {
		invocation.ArgsJSON = encoded
	}
	return invocation
}
func rewriteTaskInvocationThinkingEffort(invocation runtimecore.ToolInvocation, parentThinkingEffort string) runtimecore.ToolInvocation {
	if strings.TrimSpace(invocation.ToolName) != "Task" {
		return invocation
	}
	var args map[string]any
	if json.Unmarshal(invocation.ArgsJSON, &args) != nil || normalizeTaskThinkingEffort(readStringMapValue(args, "thinking_effort", "thinkingEffort")) != "" {
		return invocation
	}
	effort := normalizeTaskThinkingEffort(parentThinkingEffort)
	if effort == "" {
		return invocation
	}
	args["thinking_effort"] = effort
	encoded, err := json.Marshal(args)
	if err == nil {
		invocation.ArgsJSON = encoded
	}
	return invocation
}

func (service *Service) recordEffectiveSubagentDispatch(stream *ActiveStream, invocation runtimecore.ToolInvocation) error {
	if service == nil || stream == nil || strings.TrimSpace(invocation.ToolName) != "Task" {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(invocation.ArgsJSON, &args); err != nil {
		return fmt.Errorf("decode effective Task args: %w", err)
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, subagentDispatchEffective, map[string]any{
			"tool_call_id":              strings.TrimSpace(invocation.CallID),
			"effective_model_id":        readStringMapValue(args, "model", "model_id", "modelId"),
			"effective_thinking_effort": normalizeTaskThinkingEffort(readStringMapValue(args, "thinking_effort", "thinkingEffort")),
		}),
	})
	return err
}

func (service *Service) removePendingSubagentLaunch(parentConversationID string, toolCallID string) {
	if service == nil || strings.TrimSpace(toolCallID) == "" {
		return
	}
	service.subagentLaunchMu.Lock()
	defer service.subagentLaunchMu.Unlock()
	retained := service.pendingSubagents[:0]
	for _, item := range service.pendingSubagents {
		if strings.TrimSpace(item.ParentConversationID) == strings.TrimSpace(parentConversationID) &&
			strings.TrimSpace(item.ParentToolCallID) == strings.TrimSpace(toolCallID) {
			continue
		}
		retained = append(retained, item)
	}
	service.pendingSubagents = retained
}

func (service *Service) rollbackSubagentDispatch(stream *ActiveStream, pending runtimecore.PendingExec, reason string) {
	if service == nil || stream == nil || strings.TrimSpace(pending.ToolCallID) == "" {
		return
	}
	stream.mu.Lock()
	markTaskBatchTerminalLocked(stream, pending)
	delete(stream.PendingExecs, pending.ExecID)
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerSubagentResult, pending.ExecID))
	service.removePendingSubagentLaunch(stream.ConversationID, pending.ToolCallID)
	if err := service.closeSubagentDispatch(stream, pending, firstNonEmpty(strings.TrimSpace(reason), "dispatch_failed")); err != nil {
		log.Printf("forwarder subagent rollback metadata failed request_id=%s tool_call_id=%s err=%v", strings.TrimSpace(stream.RequestID), strings.TrimSpace(pending.ToolCallID), err)
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
	reserved := make(map[string]struct{})
	closed := make(map[string]struct{})
	duplicate := false
	callID = strings.TrimSpace(callID)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "metadata" || currentTurnOnly && entry.TurnSeq != turnSeq {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		kind := strings.TrimSpace(payload.Type)
		if kind != subagentDispatchReservation && kind != subagentDispatchClosed {
			continue
		}
		toolCallID := strings.TrimSpace(readStringValue(payload.Value["tool_call_id"]))
		if toolCallID == "" {
			continue
		}
		if kind == subagentDispatchClosed {
			closed[toolCallID] = struct{}{}
			continue
		}
		reserved[toolCallID] = struct{}{}
		if callID != "" && toolCallID == callID {
			duplicate = true
		}
	}
	used := 0
	for toolCallID := range reserved {
		if _, isClosed := closed[toolCallID]; !isClosed {
			used++
		}
	}
	return used, duplicate
}

func initializePendingSubagentLease(pending runtimecore.PendingExec, now time.Time) runtimecore.PendingExec {
	if strings.TrimSpace(pending.ExecKind) != "subagent" {
		return pending
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if pending.OpenedAt.IsZero() {
		pending.OpenedAt = now
	}
	if pending.LastSubagentProgressAt.IsZero() {
		pending.LastSubagentProgressAt = pending.OpenedAt
	}
	if pending.SubagentLeaseDeadline.IsZero() {
		pending.SubagentLeaseDeadline = pending.LastSubagentProgressAt.Add(subagentInactivityTimeout)
	}
	if pending.SubagentHardDeadline.IsZero() {
		pending.SubagentHardDeadline = pending.OpenedAt.Add(subagentLaunchCorrelationTTL)
	}
	return pending
}

func subagentTimeoutDelayAndReason(pending runtimecore.PendingExec, now time.Time) (time.Duration, string) {
	legacyHardDeadline := pending.OpenedAt.IsZero() && !pending.SubagentHardDeadline.IsZero()
	pending = initializePendingSubagentLease(pending, now)
	if legacyHardDeadline && pending.SubagentHardDeadline.Before(pending.SubagentLeaseDeadline) {
		return pending.SubagentHardDeadline.Sub(now), subagentTimeoutReasonMaximumRuntime
	}
	return pending.SubagentLeaseDeadline.Sub(now), subagentTimeoutReasonInactivityLease
}

func subagentAwaitingResult(streamState string) bool {
	switch strings.TrimSpace(streamState) {
	case "completed_without_result", "failed_without_result", "aborted_without_result":
		return true
	default:
		return false
	}
}

func isEffectiveChildSubagentActivity(event modeladapter.ModelEvent) bool {
	switch event.Kind {
	case modeladapter.ModelEventKindTextDelta, modeladapter.ModelEventKindThinkingDelta:
		return strings.TrimSpace(event.Text) != ""
	case modeladapter.ModelEventKindPartialToolCall:
		return strings.TrimSpace(event.ToolCallID) != "" && event.ToolCall != nil
	case modeladapter.ModelEventKindToolCallDelta:
		return strings.TrimSpace(event.ToolCallID) != "" && event.ToolCallDelta != nil
	case modeladapter.ModelEventKindToolLikeCompleted:
		return event.ToolInvocation != nil
	default:
		return false
	}
}

func (service *Service) renewParentSubagentLeaseFromChild(child *ActiveStream, event modeladapter.ModelEvent) {
	if service == nil || service.broker == nil || child == nil || !isEffectiveChildSubagentActivity(event) {
		return
	}
	child.mu.Lock()
	conversation := child.CheckpointConversation
	parentConversationID := ""
	parentToolCallID := ""
	if conversation != nil {
		parentConversationID = strings.TrimSpace(conversation.ParentConversationID)
		parentToolCallID = strings.TrimSpace(conversation.ParentToolCallID)
	}
	child.mu.Unlock()
	if parentConversationID == "" || parentToolCallID == "" {
		return
	}

	parents := make([]*ActiveStream, 0, 1)
	service.broker.mu.RLock()
	for _, candidate := range service.broker.streams {
		if candidate != nil && candidate != child {
			parents = append(parents, candidate)
		}
	}
	service.broker.mu.RUnlock()
	now := time.Now().UTC()
	for _, parent := range parents {
		parent.mu.Lock()
		if strings.TrimSpace(parent.ConversationID) != parentConversationID {
			parent.mu.Unlock()
			continue
		}
		pending, found := findSubagentPendingLocked(parent, parentToolCallID)
		if !found || subagentAwaitingResult(pending.StreamState) {
			parent.mu.Unlock()
			continue
		}
		pending = initializePendingSubagentLease(pending, now)
		pending.LastSubagentProgressAt = now
		pending.SubagentLeaseDeadline = now.Add(subagentInactivityTimeout)
		parent.PendingExecs[pending.ExecID] = pending
		parent.UpdatedAt = now
		parent.mu.Unlock()
		return
	}
}

func isBackgroundSubagentAck(result *agentv1.SubagentResult) bool {
	if result == nil || result.GetSuccess() == nil {
		return false
	}
	success := result.GetSuccess()
	return strings.TrimSpace(success.GetFinalMessage()) == "" &&
		(success.GetBackgroundReason() != agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED || strings.TrimSpace(success.GetTranscriptPath()) != "")
}

func subagentResultRunState(result *agentv1.SubagentResult) (agentv1.SubagentRunStatus, string, string) {
	if result == nil {
		return agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_UNSPECIFIED, "", ""
	}
	if success := result.GetSuccess(); success != nil {
		return agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS, strings.TrimSpace(success.GetAgentId()), strings.TrimSpace(success.GetFinalMessage())
	}
	if failure := result.GetError(); failure != nil {
		return agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR, strings.TrimSpace(failure.GetAgentId()), strings.TrimSpace(failure.GetError())
	}
	return agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_UNSPECIFIED, "", ""
}

func (service *Service) acceptBackgroundSubagentAck(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) error {
	if service == nil || stream == nil || message == nil || !isBackgroundSubagentAck(message.GetSubagentResult()) {
		return nil
	}
	if !subagentGenerationIsCurrent(stream, pending) {
		return nil
	}
	success := message.GetSubagentResult().GetSuccess()
	pending.StreamState = "backgrounded"
	pending.SubagentResumeID = strings.TrimSpace(success.GetAgentId())
	stream.mu.Lock()
	if current, ok := stream.PendingExecs[pending.ExecID]; ok && current.ProviderPass == pending.ProviderPass && current.MessageID == pending.MessageID {
		current.StreamState = pending.StreamState
		current.SubagentResumeID = pending.SubagentResumeID
		stream.PendingExecs[pending.ExecID] = current
	}
	stream.mu.Unlock()
	if err := service.recordSubagentRunState(stream, pending, agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED, pending.SubagentResumeID, ""); err != nil {
		return err
	}
	updateSubagentFinalization(stream, pending, func(item *SubagentFinalizationState) {
		item.BackgroundAcknowledged = true
		item.Pending = pending
	})
	releaseTaskBatchParentWait(stream, pending)
	detachPendingSubagentExec(stream, pending)
	clearStreamTimer(stream, providerTimerKey(streamTimerSubagentResult, pending.ExecID))
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.reconcileStream(stream)
}

func observeExplicitSubagentCancellation(stream *ActiveStream, message *agentv1.AgentClientMessage) (runtimecore.PendingExec, bool) {
	if stream == nil || message == nil || message.GetConversationAction() == nil {
		return runtimecore.PendingExec{}, false
	}
	action, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_CancelSubagentAction)
	if !ok || action.CancelSubagentAction == nil {
		return runtimecore.PendingExec{}, false
	}
	identifier := strings.TrimSpace(action.CancelSubagentAction.GetSubagentId())
	if identifier == "" {
		return runtimecore.PendingExec{}, false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	matches := func(pending runtimecore.PendingExec) bool {
		if strings.TrimSpace(pending.ExecKind) != "subagent" || pending.ProviderPass != latestSubagentGenerationLocked(stream, pending.ToolCallID) {
			return false
		}
		return identifier == strings.TrimSpace(pending.SubagentResumeID) || identifier == strings.TrimSpace(pending.ExecID) || identifier == strings.TrimSpace(pending.ToolCallID)
	}
	for _, pending := range stream.PendingExecs {
		if matches(pending) {
			return pending, true
		}
	}
	for _, state := range stream.SubagentFinalizations {
		if state == nil || state.ResultReceived || !state.BackgroundAcknowledged {
			continue
		}
		if matches(state.Pending) {
			return state.Pending, true
		}
	}
	return runtimecore.PendingExec{}, false
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
	pending, detached, found := findSubagentExecutionLocked(stream, toolCallID)
	if !found {
		return toolCallID, false
	}
	wasNew := pending.StreamState != "backgrounded"
	pending.StreamState = "backgrounded"
	if detached != nil {
		detached.Pending = pending
	} else {
		stream.PendingExecs[pending.ExecID] = pending
	}
	stream.UpdatedAt = time.Now().UTC()
	return toolCallID, wasNew
}

func observeBackgroundSubagentCompletions(stream *ActiveStream, message *agentv1.AgentClientMessage) ([]runtimecore.PendingExec, []runtimecore.PendingExec) {
	if stream == nil || message == nil || message.GetConversationAction() == nil {
		return nil, nil
	}
	item, ok := message.GetConversationAction().GetAction().(*agentv1.ConversationAction_BackgroundTaskCompletionAction)
	if !ok || item.BackgroundTaskCompletionAction == nil {
		return nil, nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	now := time.Now().UTC()
	progresses := make([]runtimecore.PendingExec, 0, len(item.BackgroundTaskCompletionAction.GetCompletions()))
	completions := make([]runtimecore.PendingExec, 0, len(item.BackgroundTaskCompletionAction.GetCompletions()))
	for _, completion := range item.BackgroundTaskCompletionAction.GetCompletions() {
		if completion == nil || completion.GetKind() != agentv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT {
			continue
		}
		pending, detached, found := findSubagentExecutionLocked(stream, completion.GetTaskId())
		if !found {
			continue
		}
		if resumeID := strings.TrimSpace(completion.GetThreadId()); resumeID != "" {
			pending.SubagentResumeID = resumeID
		}
		effectiveProgress := completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_TASK_PROGRESS ||
			(completion.GetReason() == agentv1.BackgroundTaskCompletionReason_BACKGROUND_TASK_COMPLETION_REASON_UNSPECIFIED &&
				completion.GetStatus() == agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_UNSPECIFIED &&
				(strings.TrimSpace(completion.GetTitle()) != "" || strings.TrimSpace(completion.GetDetail()) != ""))
		if effectiveProgress {
			pending = initializePendingSubagentLease(pending, now)
			pending.StreamState = "backgrounded"
			pending.LastSubagentProgressAt = now
			pending.SubagentLeaseDeadline = now.Add(subagentInactivityTimeout)
			if detached != nil {
				detached.Pending = pending
			} else {
				stream.PendingExecs[pending.ExecID] = pending
			}
			progresses = append(progresses, pending)
			continue
		}
		switch completion.GetStatus() {
		case agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCESS,
			agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ERROR,
			agentv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ABORTED:
			completions = append(completions, pending)
		default:
			continue
		}
	}
	if len(progresses) > 0 || len(completions) > 0 {
		stream.UpdatedAt = now
	}
	return progresses, completions
}

func findSubagentPendingLocked(stream *ActiveStream, identifier string) (runtimecore.PendingExec, bool) {
	pending, detached, found := findSubagentExecutionLocked(stream, identifier)
	return pending, found && detached == nil
}

func findSubagentExecutionLocked(stream *ActiveStream, identifier string) (runtimecore.PendingExec, *SubagentFinalizationState, bool) {
	identifier = strings.TrimSpace(identifier)
	if stream == nil || identifier == "" {
		return runtimecore.PendingExec{}, nil, false
	}
	matches := func(pending runtimecore.PendingExec) bool {
		return strings.TrimSpace(pending.ExecKind) == "subagent" &&
			(strings.TrimSpace(pending.ToolCallID) == identifier || strings.TrimSpace(pending.ExecID) == identifier || strings.TrimSpace(pending.SubagentResumeID) == identifier)
	}
	for _, pending := range stream.PendingExecs {
		if matches(pending) {
			return pending, nil, true
		}
	}
	for _, state := range stream.SubagentFinalizations {
		if state == nil || !state.BackgroundAcknowledged || state.ResultReceived || state.ExplicitlyCanceled || state.Pending.ProviderPass != latestSubagentGenerationLocked(stream, state.Pending.ToolCallID) {
			continue
		}
		if matches(state.Pending) {
			return state.Pending, state, true
		}
	}
	return runtimecore.PendingExec{}, nil, false
}

func (service *Service) scheduleSubagentLeaseTimeout(requestID string, pending runtimecore.PendingExec) {
	if strings.TrimSpace(pending.ExecKind) != "subagent" {
		return
	}
	delay, reason := subagentTimeoutDelayAndReason(pending, time.Now().UTC())
	if delay <= 0 {
		delay = time.Nanosecond
	}
	service.scheduleSubagentResultTimeout(requestID, pending, delay, reason)
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

func (service *Service) cancelActiveSubagentRuns(parent *ActiveStream, pending runtimecore.PendingExec, reason string) {
	if service == nil || service.broker == nil || parent == nil {
		return
	}
	parentConversationID := strings.TrimSpace(parent.ConversationID)
	parentToolCallID := strings.TrimSpace(pending.ToolCallID)
	if parentConversationID == "" || parentToolCallID == "" {
		return
	}
	type candidate struct {
		requestID string
		stream    *ActiveStream
	}
	candidates := make([]candidate, 0, 1)
	service.broker.mu.RLock()
	for requestID, stream := range service.broker.streams {
		if stream != nil && stream != parent {
			candidates = append(candidates, candidate{requestID: requestID, stream: stream})
		}
	}
	service.broker.mu.RUnlock()
	for _, item := range candidates {
		item.stream.mu.Lock()
		conversation := item.stream.CheckpointConversation
		matches := conversation != nil &&
			strings.TrimSpace(conversation.ParentConversationID) == parentConversationID &&
			strings.TrimSpace(conversation.ParentToolCallID) == parentToolCallID
		terminal := isTerminalStreamStatus(item.stream.Status) ||
			item.stream.Phase == TurnPhaseCanceled || item.stream.Phase == TurnPhaseCompleted || item.stream.Phase == TurnPhaseFailed
		item.stream.mu.Unlock()
		if !matches || terminal {
			continue
		}
		_ = service.postStreamCommandAsync(item.stream, streamCommand{
			Kind: streamCommandCancel,
			Intent: InboundIntent{
				Kind:         "cancel",
				RequestID:    item.requestID,
				CancelReason: firstNonEmpty(strings.TrimSpace(reason), "subagent lease expired"),
			},
		})
	}
}

func (service *Service) recoverSubagentStillRunning(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	return service.recordSubagentResultUnknown(stream, pending, firstNonEmpty(strings.TrimSpace(reason), "subagent progress timeout"))
}

func (service *Service) reconcileParentTodoFromSubagentResult(stream *ActiveStream, pending runtimecore.PendingExec, message *agentv1.ExecClientMessage) error {
	if service == nil || stream == nil || strings.TrimSpace(pending.ToolCallID) == "" || message == nil {
		return nil
	}
	outcome := subagentResultOutcome(message.GetSubagentResult())
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil || conversation == nil {
		return err
	}
	reservation, found := latestSubagentReservation(conversation, pending.ToolCallID)
	if !found || reservation.ParentTodoID == "" || reservation.PlanHash == "" {
		return nil
	}
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) == nil && strings.TrimSpace(payload.Type) == subagentParentTodoReconciled && strings.TrimSpace(readStringValue(payload.Value["dispatch_id"])) == reservation.DispatchID {
			return nil
		}
	}
	currentPlanHash := subagentParentPlanHash(conversation)
	currentAttempt := latestSubagentTodoAttempt(conversation, reservation.PlanHash, reservation.ParentTodoID)
	applied := outcome == "succeeded" && currentPlanHash == reservation.PlanHash && currentAttempt == reservation.DispatchID
	reason := "outcome_not_successful"
	if currentPlanHash != reservation.PlanHash {
		reason = "stale_plan"
	} else if currentAttempt != reservation.DispatchID {
		reason = "stale_attempt"
	} else if applied {
		reason = "completed"
	}
	entries := make([]HistoryEntry, 0, 4)
	if applied {
		state, stateErr := projectConversationStructuredState(conversation)
		if stateErr != nil {
			return stateErr
		}
		nextTodos := cloneTodoItems(state.Todos)
		updated := false
		for _, todo := range nextTodos {
			if todo == nil || strings.TrimSpace(todo.GetId()) != reservation.ParentTodoID || isTerminalTodoStatus(todo.GetStatus()) {
				continue
			}
			todo.Status = agentv1.TodoStatus_TODO_STATUS_COMPLETED
			todo.UpdatedAt = time.Now().UTC().UnixMilli()
			updated = true
			break
		}
		if updated {
			payload, encodeErr := json.Marshal(runtimeStateEntryPayload{
				PlanText: state.PlanText,
				Plans:    clonePlanRegistryEntries(state.Plans),
				Todos:    nextTodos,
			})
			if encodeErr != nil {
				return fmt.Errorf("encode parent todo reconciliation state: %w", encodeErr)
			}
			entries = append(entries, HistoryEntry{
				TurnSeq:   stream.TurnSeq,
				RequestID: strings.TrimSpace(stream.RequestID),
				Role:      "system",
				Kind:      "runtime_state",
				Payload:   payload,
			})
			entries = append(entries, buildPlanWaveTransitionEntries(conversation, state.Todos, nextTodos, stream.TurnSeq, stream.RequestID)...)
		} else {
			applied = false
			reason = "todo_missing_or_terminal"
		}
	}
	entries = append(entries, newMetadataEntry(stream.TurnSeq, stream.RequestID, subagentParentTodoReconciled, map[string]any{
		"tool_call_id":   strings.TrimSpace(pending.ToolCallID),
		"dispatch_id":    reservation.DispatchID,
		"parent_todo_id": reservation.ParentTodoID,
		"plan_hash":      reservation.PlanHash,
		"plan_version":   reservation.PlanVersion,
		"outcome":        outcome,
		"applied":        applied,
		"reason":         reason,
	}))
	_, err = service.appendConversationEntries(stream, stream.ConversationID, entries)
	return err
}

type subagentReservationCorrelation struct {
	DispatchID   string
	ParentTodoID string
	PlanHash     string
	PlanVersion  int64
}

func latestSubagentReservation(conversation *ConversationFile, toolCallID string) (subagentReservationCorrelation, bool) {
	toolCallID = strings.TrimSpace(toolCallID)
	if conversation == nil || toolCallID == "" {
		return subagentReservationCorrelation{}, false
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) != nil || strings.TrimSpace(payload.Type) != subagentDispatchReservation || strings.TrimSpace(readStringValue(payload.Value["tool_call_id"])) != toolCallID {
			continue
		}
		return subagentReservationCorrelation{
			DispatchID:   firstNonEmpty(strings.TrimSpace(readStringValue(payload.Value["dispatch_id"])), toolCallID),
			ParentTodoID: strings.TrimSpace(readStringValue(payload.Value["parent_todo_id"])),
			PlanHash:     strings.TrimSpace(readStringValue(payload.Value["plan_hash"])),
			PlanVersion:  int64Value(payload.Value["plan_version"]),
		}, true
	}
	return subagentReservationCorrelation{}, false
}

func latestSubagentTodoAttempt(conversation *ConversationFile, planHash string, parentTodoID string) string {
	if conversation == nil {
		return ""
	}
	for index := len(conversation.Entries) - 1; index >= 0; index-- {
		entry := conversation.Entries[index]
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) != nil || strings.TrimSpace(payload.Type) != subagentDispatchReservation {
			continue
		}
		if strings.TrimSpace(readStringValue(payload.Value["plan_hash"])) == strings.TrimSpace(planHash) && strings.TrimSpace(readStringValue(payload.Value["parent_todo_id"])) == strings.TrimSpace(parentTodoID) {
			return firstNonEmpty(strings.TrimSpace(readStringValue(payload.Value["dispatch_id"])), strings.TrimSpace(readStringValue(payload.Value["tool_call_id"])))
		}
	}
	return ""
}

func subagentResultOutcome(result *agentv1.SubagentResult) string {
	if result == nil {
		return "missing"
	}
	switch item := result.GetResult().(type) {
	case *agentv1.SubagentResult_Success:
		if strings.TrimSpace(item.Success.GetFinalMessage()) == "" {
			return "background_or_empty"
		}
		return "succeeded"
	case *agentv1.SubagentResult_Error:
		return "failed"
	default:
		return "unknown"
	}
}

func registerTaskBatchMember(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ToolCallID) == "" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.TaskBatches == nil {
		stream.TaskBatches = make(map[int]*TaskBatch)
	}
	batch := stream.TaskBatches[pending.ProviderPass]
	if batch == nil {
		batch = &TaskBatch{Generation: pending.ProviderPass, ProviderPass: pending.ProviderPass, Members: make(map[string]*TaskBatchMember)}
		stream.TaskBatches[pending.ProviderPass] = batch
	}
	if _, exists := batch.Members[pending.ToolCallID]; !exists {
		batch.Members[pending.ToolCallID] = &TaskBatchMember{ToolCallID: strings.TrimSpace(pending.ToolCallID)}
	}
}

func releaseTaskBatchParentWait(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ToolCallID) == "" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	batch := stream.TaskBatches[pending.ProviderPass]
	if batch == nil {
		return
	}
	member := batch.Members[strings.TrimSpace(pending.ToolCallID)]
	if member == nil || member.Terminal {
		return
	}
	member.ParentReleased = true
}

func detachPendingSubagentExec(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	current, ok := stream.PendingExecs[pending.ExecID]
	if !ok || current.MessageID != pending.MessageID || current.ProviderPass != pending.ProviderPass || strings.TrimSpace(current.ToolCallID) != strings.TrimSpace(pending.ToolCallID) {
		return
	}
	delete(stream.PendingExecs, pending.ExecID)
	stream.UpdatedAt = time.Now().UTC()
}

func latestSubagentGenerationLocked(stream *ActiveStream, toolCallID string) int {
	if stream == nil || strings.TrimSpace(toolCallID) == "" {
		return 0
	}
	toolCallID = strings.TrimSpace(toolCallID)
	latest := 0
	for _, pending := range stream.PendingExecs {
		if strings.TrimSpace(pending.ExecKind) == "subagent" && strings.TrimSpace(pending.ToolCallID) == toolCallID && pending.ProviderPass > latest {
			latest = pending.ProviderPass
		}
	}
	for _, state := range stream.SubagentFinalizations {
		if state == nil || strings.TrimSpace(state.Pending.ToolCallID) != toolCallID {
			continue
		}
		if state.Pending.ProviderPass > latest {
			latest = state.Pending.ProviderPass
		}
	}
	return latest
}

func subagentGenerationIsCurrent(stream *ActiveStream, pending runtimecore.PendingExec) bool {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ToolCallID) == "" {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return pending.ProviderPass == latestSubagentGenerationLocked(stream, pending.ToolCallID)
}

func markTaskBatchTerminalLocked(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" || strings.TrimSpace(pending.ToolCallID) == "" {
		return
	}
	batch := stream.TaskBatches[pending.ProviderPass]
	if batch == nil {
		return
	}
	member := batch.Members[strings.TrimSpace(pending.ToolCallID)]
	if member == nil || member.Terminal {
		return
	}
	member.Terminal = true
	member.TerminalSource = firstNonEmpty(strings.TrimSpace(pending.StreamState), "real_result")
	member.TerminalAt = time.Now().UTC()
}

func taskBatchAllowsParentNotification(stream *ActiveStream) (int, bool) {
	if stream == nil {
		return 0, true
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	var selected *TaskBatch
	for _, batch := range stream.TaskBatches {
		if batch == nil || len(batch.Members) == 0 || batch.ParentNotified {
			continue
		}
		if selected == nil || batch.Generation > selected.Generation {
			selected = batch
		}
	}
	if selected == nil {
		return 0, true
	}
	for _, member := range selected.Members {
		if member == nil || (!member.Terminal && !member.ParentReleased) {
			return selected.Generation, false
		}
	}
	selected.ParentNotified = true
	return selected.Generation, true
}
func taskBatchDebugSnapshot(stream *ActiveStream, generation int) []map[string]any {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	batch := stream.TaskBatches[generation]
	if batch == nil {
		return nil
	}
	members := make([]map[string]any, 0, len(batch.Members))
	for _, member := range batch.Members {
		if member == nil {
			continue
		}
		members = append(members, map[string]any{
			"tool_call_id":    member.ToolCallID,
			"parent_released": member.ParentReleased,
			"terminal":        member.Terminal,
			"terminal_source": member.TerminalSource,
		})
	}
	return members
}

func (service *Service) closeSubagentDispatch(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if service == nil || stream == nil || strings.TrimSpace(pending.ToolCallID) == "" {
		return nil
	}
	service.removePendingSubagentLaunch(stream.ConversationID, pending.ToolCallID)
	stream.mu.Lock()
	conversation := cloneConversationFile(stream.CheckpointConversation)
	turnSeq := stream.TurnSeq
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	stream.mu.Unlock()
	if conversation == nil {
		return nil
	}
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) == nil && strings.TrimSpace(payload.Type) == subagentDispatchClosed && strings.TrimSpace(readStringValue(payload.Value["tool_call_id"])) == strings.TrimSpace(pending.ToolCallID) {
			return nil
		}
	}
	reservation, _ := latestSubagentReservation(conversation, pending.ToolCallID)
	_, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, subagentDispatchClosed, map[string]any{
			"tool_call_id":   pending.ToolCallID,
			"exec_id":        pending.ExecID,
			"dispatch_id":    reservation.DispatchID,
			"parent_todo_id": reservation.ParentTodoID,
			"plan_hash":      reservation.PlanHash,
			"plan_version":   reservation.PlanVersion,
			"outcome":        strings.TrimSpace(reason),
			"reason":         strings.TrimSpace(reason),
		}),
	})
	return err
}

func (service *Service) recoverSubagentWithoutResult(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	return service.recordSubagentResultUnknown(stream, pending, firstNonEmpty(strings.TrimSpace(reason), "subagent result was not returned"))
}

func (service *Service) recordSubagentResultUnknown(stream *ActiveStream, pending runtimecore.PendingExec, reason string) error {
	if service == nil || stream == nil || strings.TrimSpace(pending.ExecKind) != "subagent" {
		return nil
	}
	_, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newMetadataEntry(stream.TurnSeq, stream.RequestID, "subagent_result_unknown", map[string]any{
			"tool_call_id": pending.ToolCallID,
			"exec_id":      pending.ExecID,
			"message_id":   pending.MessageID,
			"generation":   pending.ProviderPass,
			"reason":       strings.TrimSpace(reason),
			"terminal":     false,
		}),
	})
	return err
}
