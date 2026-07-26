package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	projectedReplayKiB = 1024

	projectedReadReplayLimit       = 24 * projectedReplayKiB
	projectedShellReplayLimit      = 32 * projectedReplayKiB
	projectedShellStreamLimit      = 8 * projectedReplayKiB
	projectedShellInterleavedLimit = 16 * projectedReplayKiB
	projectedGrepReplayLimit       = 16 * projectedReplayKiB
	projectedListReplayLimit       = 16 * projectedReplayKiB
	projectedEditReplayLimit       = 32 * projectedReplayKiB
	projectedPatchEditReplayLimit  = 4 * projectedReplayKiB
	projectedWebFetchReplayLimit   = 24 * projectedReplayKiB
	projectedWebSearchReplayLimit  = 16 * projectedReplayKiB
	projectedMcpReplayLimit        = 24 * projectedReplayKiB

	// replayBudgetTotalBytes 是单回合工具结果回放的软预算；超限时通过持久化的
	// replay_budget_boundary 边界把更早的结果压缩到 replayBudgetCompressedLimit。
	// 边界只前进（单调），因此同一历史在每个 provider pass 的回放字节保持一致，
	// 不会像滑动窗口那样每个 pass 重写前缀、击穿 prompt cache。
	replayBudgetTotalBytes      = 512 * projectedReplayKiB
	replayBudgetKeepFullResults = 8
	replayBudgetCompressedLimit = 4 * projectedReplayKiB
)

// replayBudgetBoundarySeq 返回历史中最新一条 replay_budget_boundary 元数据的边界 seq；
// 边界之内（seq <= boundary）的 tool_result 回放时压缩到 replayBudgetCompressedLimit。
// 历史文件本身保存完整事实，压缩只作用于模型回放。
func replayBudgetBoundarySeq(entries []HistoryEntry) int64 {
	boundary := int64(0)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if json.Unmarshal(entry.Payload, &payload) != nil || strings.TrimSpace(payload.Type) != "replay_budget_boundary" {
			continue
		}
		if seq, ok := payload.Value["boundary_seq"].(float64); ok && int64(seq) > boundary {
			boundary = int64(seq)
		}
	}
	return boundary
}

// applyReplayBudgetCompression 对预算边界内的旧结果强制压缩额度（UTF-8 安全，带截断标记）。
func applyReplayBudgetCompression(toolName string, content string) string {
	if len(content) <= replayBudgetCompressedLimit {
		return content
	}
	return truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "tool"), content, replayBudgetCompressedLimit)
}

func limitProjectedToolResultReplay(toolName string, content string, resultText string, fromStoredToolCall bool, historical bool) string {
	if compacted, ok := compactProjectedGenerateImageResultReplay(toolName, content, resultText); ok {
		return compacted
	}
	if historical {
		if compacted, ok := compactHistoricalEditErrorReplay(toolName, content); ok {
			content = compacted
			fromStoredToolCall = false
		}
	}
	if compacted, ok := compactProjectedEditToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	if compacted, ok := compactProjectedShellToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	limit, ok := projectedToolReplayLimit(toolName)
	if !ok {
		return strings.TrimSpace(content)
	}
	content = strings.TrimSpace(content)
	if len(content) <= limit {
		return content
	}
	if fromStoredToolCall {
		fallback := strings.TrimSpace(resultText)
		notice := fmt.Sprintf("[tool result replay truncated: stored ToolCall result exceeded %d bytes]", limit)
		if fallback == "" {
			fallback = notice
		} else {
			fallback += "\n\n" + notice
		}
		return truncateProjectedReplayText(toolName, fallback, limit)
	}
	return truncateProjectedReplayText(toolName, content, limit)
}

func projectedToolReplayLimit(toolName string) (int, bool) {
	switch strings.TrimSpace(toolName) {
	case "GenerateImage":
		return projectedWebSearchReplayLimit, true
	case "Read":
		return projectedReadReplayLimit, true
	case "Shell":
		return projectedShellReplayLimit, true
	case "Grep":
		return projectedGrepReplayLimit, true
	case "Glob", "Ls":
		return projectedListReplayLimit, true
	case "PatchEdit", "PatchEditLines", "PatchEditSpan":
		return projectedPatchEditReplayLimit, true
	case "Edit", "Write":
		return projectedEditReplayLimit, true
	case "WebFetch":
		return projectedWebFetchReplayLimit, true
	case "WebSearch":
		return projectedWebSearchReplayLimit, true
	case "CallMcpTool", "FetchMcpResource", "ListMcpResources":
		return projectedMcpReplayLimit, true
	default:
		return 0, false
	}
}

func compactProjectedGenerateImageResultReplay(toolName string, content string, resultText string) (string, bool) {
	if strings.TrimSpace(toolName) != "GenerateImage" {
		return "", false
	}
	fallback := strings.TrimSpace(resultText)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		if fallback == "" {
			return "", false
		}
		return fallback, true
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		if fallback == "" {
			return truncateProjectedReplayText("GenerateImage", trimmed, projectedWebSearchReplayLimit), true
		}
		return fallback, true
	}
	if compactGenerateImagePayload(payload) {
		encoded, err := json.Marshal(payload)
		if err == nil {
			return string(encoded), true
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return trimmed, true
}

func compactGenerateImagePayload(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "image_data", "imageData":
					item[key] = fmt.Sprintf("[base64 image data omitted from replay; bytes=%d]", len(strings.TrimSpace(text)))
					changed = true
					continue
				}
			}
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	}
	return changed
}

func patchEditReplayLimit(toolName string) int {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan":
		return projectedPatchEditReplayLimit
	default:
		return projectedEditReplayLimit
	}
}

func compactProjectedShellToolResultReplay(toolName string, content string) (string, bool) {
	if strings.TrimSpace(toolName) != "Shell" {
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	if !compactProjectedShellFields(payload) {
		return "", false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func compactProjectedShellFields(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "stdout", "stderr":
					next := truncateProjectedReplayTextMiddle("Shell "+key, text, projectedShellStreamLimit)
					if next != text {
						item[key] = next
						changed = true
					}
					continue
				case "interleaved_output", "interleavedOutput":
					next := truncateProjectedReplayTextMiddle("Shell interleaved output", text, projectedShellInterleavedLimit)
					if next != text {
						item[key] = next
						changed = true
					}
					continue
				}
			}
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	}
	return changed
}

func compactProjectedEditToolResultReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	success, ok := payload["success"].(map[string]any)
	if !ok {
		return "", false
	}
	diffString := firstJSONText(success, "diff_string", "diffString")
	beforeContent := ""
	afterContent := ""
	if diffString == "" {
		beforeContent = firstJSONText(success, "before_full_file_content", "beforeFullFileContent")
		afterContent = firstJSONText(success, "after_full_file_content", "afterFullFileContent")
		if beforeContent != "" {
			diffString, _, _ = computeEditDiff(beforeContent, afterContent)
		}
	}
	if diffString != "" {
		diffString = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "PatchEdit"), diffString, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"diff_string": diffString,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	if afterContent != "" {
		afterContent = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "Write"), afterContent, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"after_full_file_content": afterContent,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	return "", false
}

func compactHistoricalEditErrorReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		return "", false
	}
	if firstJSONText(errorPayload, "error", "modelVisibleError") == "" {
		return "", false
	}
	encoded, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"error": "historical edit error omitted from replay; re-read the file before editing",
		},
	})
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func firstJSONText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

func truncateProjectedReplayText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return strings.TrimSpace(text)
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateProjectedUTF8(text, limit)
		}
		kept := truncateProjectedUTF8(text, keep)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; showing %d of %d bytes]", toolName, limit, len(kept), original)
		output := strings.TrimRight(kept, "\n") + nextNotice
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateProjectedReplayTextMiddle(toolName string, text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	original := len(text)
	notice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, limit, original)
	for {
		keep := limit - len(notice)
		if keep <= 0 {
			return truncateProjectedUTF8(text, limit)
		}
		headLimit := keep / 2
		tailLimit := keep - headLimit
		head := truncateProjectedUTF8(text, headLimit)
		tail := truncateProjectedUTF8Suffix(text, tailLimit)
		kept := len(head) + len(tail)
		nextNotice := fmt.Sprintf("\n\n[truncated: %s result exceeded %d bytes; omitted middle; showing %d of %d bytes]\n\n", toolName, limit, kept, original)
		output := head + nextNotice + tail
		if len(output) <= limit || nextNotice == notice {
			return output
		}
		notice = nextNotice
	}
}

func truncateProjectedUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	if limit > len(text) {
		limit = len(text)
	}
	truncated := text[:limit]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func truncateProjectedUTF8Suffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	if start < 0 {
		start = 0
	}
	suffix := text[start:]
	for !utf8.ValidString(suffix) && start < len(text) {
		start++
		suffix = text[start:]
	}
	return suffix
}
