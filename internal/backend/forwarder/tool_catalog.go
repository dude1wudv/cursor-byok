// tool_catalog.go 负责从静态 prompt 资产中装载并筛选 canonical tool catalog。
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

type DefaultToolCatalog struct {
	models modeladapter.SubagentModelDirectory
}

// NewToolCatalog 创建默认工具目录实现。
func NewToolCatalog(models ...modeladapter.SubagentModelDirectory) *DefaultToolCatalog {
	catalog := &DefaultToolCatalog{}
	if len(models) > 0 {
		catalog.models = models[0]
	}
	return catalog
}

// Load 按 mode 读取工具资产，并过滤出当前阶段真正允许暴露的工具。
func (catalog *DefaultToolCatalog) Load(mode agentv1.AgentMode, subagentTypeName string) ([]json.RawMessage, []string, error) {
	assetMode, err := toolAssetModeForConversation(mode, subagentTypeName)
	if err != nil {
		return nil, nil, err
	}
	rawTools, err := promptassets.ReadTools(assetMode)
	if err != nil {
		return nil, nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawTools, &items); err != nil {
		return nil, nil, fmt.Errorf("decode tools asset failed: %w", err)
	}
	filtered := make([]json.RawMessage, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, err
		}
		if !isToolAllowedInMode(mode, subagentTypeName, name) {
			continue
		}
		if name == "Task" && !isChildConversationSubagentTypeName(subagentTypeName) {
			item, err = catalog.rewriteRootTaskTool(item)
			if err != nil {
				return nil, nil, err
			}
			if item == nil {
				continue
			}
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil
}

func (catalog *DefaultToolCatalog) rewriteRootTaskTool(item json.RawMessage) (json.RawMessage, error) {
	if catalog == nil || catalog.models == nil {
		return nil, nil
	}
	models := append([]modeladapter.SubagentModel(nil), catalog.models.EnabledSubagentModels()...)
	if len(models) == 0 {
		return nil, nil
	}
	enum := make([]string, 0, len(models))
	details := make([]string, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		enum = append(enum, id)
		roles := make([]string, 0, len(model.Roles))
		for _, role := range model.Roles {
			if normalized := normalizeSubagentRole(role); normalized != "" {
				roles = append(roles, normalized)
			}
		}
		details = append(details, fmt.Sprintf("- roles=%s | %s | adapter=%s | model=%s | %s", strings.Join(roles, ","), strings.TrimSpace(model.DisplayName), id, strings.TrimSpace(model.ModelID), strings.TrimSpace(model.TooltipData)))
	}
	if len(enum) == 0 {
		return nil, nil
	}
	var tool map[string]any
	if err := json.Unmarshal(item, &tool); err != nil {
		return nil, fmt.Errorf("decode Task tool descriptor: %w", err)
	}
	function, _ := tool["function"].(map[string]any)
	parameters, _ := function["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	model, _ := properties["model"].(map[string]any)
	if function == nil || parameters == nil || properties == nil || model == nil {
		return nil, fmt.Errorf("Task tool descriptor model schema is missing")
	}
	model["enum"] = enum
	roleEnum := []string{"simple_explore", "medium_explore", "complex_debug"}
	roleProperty := map[string]any{
		"type": "string", "enum": roleEnum,
		"description": "任务角色：simple_explore 简单探索、medium_explore 中等探索、complex_debug 复杂调试。",
	}
	properties["task_role"] = roleProperty
	required, _ := parameters["required"].([]any)
	filteredRequired := make([]any, 0, len(required)+1)
	for _, field := range required {
		if fieldText, ok := field.(string); ok && fieldText == "model" {
			continue
		}
		filteredRequired = append(filteredRequired, field)
	}
	parameters["required"] = append(filteredRequired, "task_role")
	model["description"] = "Optional adapter override for a new subagent. Omit this field for automatic task_role routing; do not copy the parent model or choose a role candidate yourself. Set it only when intentionally overriding automatic routing with a specific enabled adapter ID. Explicit model IDs take priority and must be enabled. Enabled adapters (roles | display name | adapter ID | provider model | note):\n" + strings.Join(details, "\n")
	if description, ok := function["description"].(string); ok {
		function["description"] = description + "\n\nRole-based routing: always set task_role. For automatic routing, omit model entirely; the backend selects the first enabled adapter for that role in configuration order. Set model only for an intentional explicit override. If thinking_effort is omitted, role defaults apply (simple_explore=low, medium_explore=medium, complex_debug=medium)."
	}
	return json.Marshal(tool)
}

var agentModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"GenerateImage":        {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"SwitchMode":           {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var multitaskModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"GenerateImage":        {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"SwitchMode":           {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var debugModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var askModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"Delete":               {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"PatchEdit":            {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
	"Write":                {},
}

var planModeToolNames = map[string]struct{}{
	"AskQuestion":          {},
	"CallMcpTool":          {},
	"CreatePlan":           {},
	"FetchMcpResource":     {},
	"Glob":                 {},
	"Grep":                 {},
	"Ls":                   {},
	"Read":                 {},
	"ReadLints":            {},
	"Shell":                {},
	"AwaitShell":           {},
	"WriteShellStdin":      {},
	"ForceBackgroundShell": {},
	"Task":                 {},
	"TodoWrite":            {},
	"WebFetch":             {},
	"WebSearch":            {},
}

var readonlySubagentToolNames = map[string]struct{}{
	"FetchMcpResource": {},
	"Glob":             {},
	"Grep":             {},
	"Ls":               {},
	"Read":             {},
	"ReadLints":        {},
	"WebFetch":         {},
	"WebSearch":        {},
}

var childConversationDisallowedAgentToolNames = map[string]struct{}{
	"AskQuestion": {},
}

func supportedToolNamesForMode(mode agentv1.AgentMode) map[string]struct{} {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return agentModeToolNames
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return askModeToolNames
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return planModeToolNames
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return debugModeToolNames
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return multitaskModeToolNames
	default:
		return nil
	}
}

func isToolAllowedInMode(mode agentv1.AgentMode, subagentTypeName string, toolName string) bool {
	trimmedToolName := strings.TrimSpace(toolName)
	if trimmedToolName == "" {
		return false
	}
	if isChildConversationSubagentTypeName(subagentTypeName) {
		return isToolAllowedForSubagentMode(mode, subagentTypeName, trimmedToolName)
	}
	supported := supportedToolNamesForMode(mode)
	if supported == nil {
		return false
	}
	_, ok := supported[trimmedToolName]
	return ok
}

func isToolAllowedForSubagentMode(mode agentv1.AgentMode, subagentTypeName string, toolName string) bool {
	readonly := normalizeMode(mode) == agentv1.AgentMode_AGENT_MODE_PLAN
	return isToolAllowedForSubagentCapability(subagentTypeName, readonly, toolName)
}

func isToolAllowedForSubagent(subagentTypeName string, toolName string) bool {
	return isToolAllowedForSubagentCapability(subagentTypeName, strings.EqualFold(strings.TrimSpace(subagentTypeName), "explore"), toolName)
}

func isToolAllowedForSubagentCapability(subagentTypeName string, readonly bool, toolName string) bool {
	capability, err := runtimecore.ResolveSubagentCapability(subagentTypeName, readonly)
	if err != nil {
		return false
	}
	if capability.Readonly {
		_, ok := readonlySubagentToolNames[toolName]
		return ok
	}
	if _, disallowed := childConversationDisallowedAgentToolNames[toolName]; disallowed {
		return false
	}
	_, ok := agentModeToolNames[toolName]
	return ok
}

func validateTaskSubagentCapability(argsJSON []byte) error {
	args, err := runtimecore.DecodeArgsMap(argsJSON)
	if err != nil {
		return fmt.Errorf("decode Task args: %w", err)
	}
	value, found := args["readonly"]
	if !found {
		value, found = args["readOnly"]
	}
	var readonly *bool
	if found {
		parsed, ok := value.(bool)
		if !ok {
			return fmt.Errorf("task readonly must be boolean")
		}
		readonly = &parsed
	}
	_, err = runtimecore.ResolveTaskSubagentCapability(runtimecore.ReadStringArg(args, "subagent_type", "subagentType"), readonly)
	return err
}

func validateSubagentToolInvocation(mode agentv1.AgentMode, subagentTypeName string, toolName string, argsJSON []byte) error {
	if !isToolAllowedInMode(mode, subagentTypeName, toolName) {
		return fmt.Errorf("tool invocation is not enabled in mode %s: %s", mode.String(), toolName)
	}
	if !isChildConversationSubagentTypeName(subagentTypeName) || normalizeMode(mode) != agentv1.AgentMode_AGENT_MODE_PLAN || strings.TrimSpace(toolName) != "FetchMcpResource" {
		return nil
	}
	args, err := runtimecore.DecodeArgsMap(argsJSON)
	if err != nil {
		return fmt.Errorf("decode FetchMcpResource args: %w", err)
	}
	if strings.TrimSpace(runtimecore.ReadStringArg(args, "downloadPath", "download_path")) != "" {
		return fmt.Errorf("FetchMcpResource downloadPath is not allowed for readonly subagents")
	}
	return nil
}

func isChildConversationSubagentTypeName(subagentTypeName string) bool {
	return strings.TrimSpace(subagentTypeName) != ""
}

func filterTaskToolNamesForSubagentRole(conversation *ConversationFile, names []string) []string {
	if conversation == nil || !isChildConversationSubagentTypeName(conversation.SubagentTypeName) || normalizeSubagentRole(conversation.SubagentRole) == "complex_debug" {
		return names
	}
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != "Task" {
			filtered = append(filtered, name)
		}
	}
	return filtered
}
func filterTaskToolForSubagentRole(conversation *ConversationFile, items []json.RawMessage) ([]json.RawMessage, []string, error) {
	if conversation == nil || !isChildConversationSubagentTypeName(conversation.SubagentTypeName) || normalizeSubagentRole(conversation.SubagentRole) == "complex_debug" {
		names := make([]string, 0, len(items))
		for _, item := range items {
			name, err := extractToolName(item)
			if err != nil {
				return nil, nil, err
			}
			names = append(names, name)
		}
		return items, names, nil
	}
	filtered := make([]json.RawMessage, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, err
		}
		if name == "Task" {
			continue
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil
}
func selectToolsByOrderedNames(items []json.RawMessage, orderedNames []string) ([]json.RawMessage, []string, error) {
	byName := make(map[string]json.RawMessage, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := byName[name]; !exists {
			byName[name] = item
		}
	}
	filtered := make([]json.RawMessage, 0, len(orderedNames))
	names := make([]string, 0, len(orderedNames))
	for _, name := range orderedNames {
		item, ok := byName[name]
		if !ok {
			return nil, nil, fmt.Errorf("tool descriptor %q not found in prompt asset", name)
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil
}

func toolAssetModeForConversation(mode agentv1.AgentMode, subagentTypeName string) (promptassets.Mode, error) {
	if isChildConversationSubagentTypeName(subagentTypeName) {
		return promptassets.ModeAgent, nil
	}
	return mapPromptMode(mode)
}

func promptAssetModeForConversation(mode agentv1.AgentMode, subagentTypeName string) (promptassets.Mode, error) {
	if isChildConversationSubagentTypeName(subagentTypeName) {
		return promptassets.ModeSubagent, nil
	}
	return mapPromptMode(mode)
}

// mapPromptMode 把协议 mode 映射为静态 prompt 资产对应的目录名。
func mapPromptMode(mode agentv1.AgentMode) (promptassets.Mode, error) {
	switch normalizeMode(mode) {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return promptassets.ModeAgent, nil
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return promptassets.ModeAsk, nil
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return promptassets.ModePlan, nil
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return promptassets.ModeDebug, nil
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return promptassets.ModeMultitask, nil
	default:
		return "", fmt.Errorf("unsupported prompt asset mode: %s", mode.String())
	}
}

// extractToolName 从原始 tool descriptor JSON 中提取函数名。
func extractToolName(raw json.RawMessage) (string, error) {
	var wrapper struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", fmt.Errorf("decode tool descriptor failed: %w", err)
	}
	name := strings.TrimSpace(wrapper.Function.Name)
	if name == "" {
		return "", fmt.Errorf("tool descriptor name is required")
	}
	return name, nil
}

// sanitizePromptAsset 去掉资产文件中的说明性标题，只保留真正的 prompt 文本。
func sanitizePromptAsset(text string, modelName string) string {
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "# 通用系统提示词", "# 模式静态补充", "---":
			continue
		default:
			filtered = append(filtered, line)
		}
	}
	return promptassets.RenderPromptTemplate(strings.TrimSpace(strings.Join(filtered, "\n")), modelName)
}
