package modeladapter

import (
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func emitCreatePlanToolProgress(
	sink func(ModelEvent) error,
	provider string,
	model string,
	callID string,
	rawArgs string,
	argsTextDelta string,
	lastSnapshot *string,
) error {
	if sink == nil || lastSnapshot == nil {
		return nil
	}
	trimmedCallID := strings.TrimSpace(callID)
	if trimmedCallID == "" {
		return nil
	}
	args, ok := createPlanArgsProgressSnapshot(rawArgs)
	if !ok {
		// 参数尚不可解析（如 content_block_start 阶段为空、或 Claude 先输出暂不支持前缀
		// 提取的字段）：先发一次空占位 partial，让客户端立即弹出 plan 卡片，后续快照渐进填充。
		if *lastSnapshot != "" {
			return nil
		}
		args = &agentv1.CreatePlanArgs{}
	}
	signatureBytes, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(args)
	if err != nil {
		return err
	}
	signature := string(signatureBytes)
	if signature == "" || signature == *lastSnapshot {
		return nil
	}
	*lastSnapshot = signature
	if err := sink(ModelEvent{
		Kind:          ModelEventKindPartialToolCall,
		OccurredAt:    time.Now().UTC(),
		Provider:      provider,
		Model:         model,
		ToolCallID:    trimmedCallID,
		ArgsTextDelta: argsTextDelta,
		ToolCall: &agentv1.ToolCall{
			Tool: &agentv1.ToolCall_CreatePlanToolCall{
				CreatePlanToolCall: &agentv1.CreatePlanToolCall{
					Args: args,
				},
			},
		},
	}); err != nil {
		return err
	}
	return nil
}

func createPlanArgsProgressSnapshot(rawArgs string) (*agentv1.CreatePlanArgs, bool) {
	trimmed := strings.TrimSpace(rawArgs)
	if trimmed == "" {
		return nil, false
	}
	if args, err := runtimecore.DecodeCreatePlanArgsJSON([]byte(trimmed)); err == nil && hasCreatePlanArgsProgress(args) {
		return args, true
	}

	args := &agentv1.CreatePlanArgs{}
	if value, found, _ := extractJSONStringFieldPrefix(trimmed, "plan"); found {
		args.Plan = value
	}
	if value, found, _ := extractJSONStringFieldPrefix(trimmed, "overview"); found {
		args.Overview = value
	}
	if value, found, complete := extractJSONStringFieldPrefix(trimmed, "name"); found && complete {
		args.Name = strings.TrimSpace(value)
	}
	if todos := extractCreatePlanTodosPrefix(trimmed); len(todos) > 0 {
		args.Todos = todos
	}
	if !hasCreatePlanArgsProgress(args) {
		return nil, false
	}
	return args, true
}

// extractCreatePlanTodosPrefix 从不完整的 CreatePlan 参数 JSON 中提取已完整闭合的 todo 对象，
// 供渐进渲染使用——Claude 可能先输出 todos、后输出 plan/name。
func extractCreatePlanTodosPrefix(input string) []*agentv1.TodoItem {
	keyToken := `"todos"`
	start := strings.Index(input, keyToken)
	if start < 0 {
		return nil
	}
	index := start + len(keyToken)
	for index < len(input) && isJSONWhitespace(input[index]) {
		index++
	}
	if index >= len(input) || input[index] != ':' {
		return nil
	}
	index++
	for index < len(input) && isJSONWhitespace(input[index]) {
		index++
	}
	if index >= len(input) || input[index] != '[' {
		return nil
	}
	index++
	items := make([]any, 0, 8)
	for index < len(input) {
		for index < len(input) && (isJSONWhitespace(input[index]) || input[index] == ',') {
			index++
		}
		if index >= len(input) || input[index] == ']' {
			break
		}
		if input[index] != '{' {
			break
		}
		object, next, complete := scanBalancedJSONObject(input, index)
		if !complete {
			break
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(object), &payload); err != nil {
			break
		}
		items = append(items, payload)
		index = next
	}
	if len(items) == 0 {
		return nil
	}
	return runtimecore.LenientCreatePlanTodoItems(items)
}

// scanBalancedJSONObject 从 start（须为 '{'）扫描到配对的 '}'，正确跳过字符串与转义。
func scanBalancedJSONObject(input string, start int) (string, int, bool) {
	depth := 0
	inString := false
	for i := start; i < len(input); i++ {
		character := input[i]
		if inString {
			if character == '\\' {
				i++
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return input[start : i+1], i + 1, true
			}
		}
	}
	return "", start, false
}

func hasCreatePlanArgsProgress(args *agentv1.CreatePlanArgs) bool {
	if args == nil {
		return false
	}
	return args.GetPlan() != "" ||
		args.GetOverview() != "" ||
		args.GetName() != "" ||
		args.GetIsProject() ||
		len(args.GetTodos()) > 0 ||
		len(args.GetPhases()) > 0
}
