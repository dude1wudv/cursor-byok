package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

const promptContextSourceSelectedCursorCommands = "selected_cursor_commands"

func newPromptContextMessage(source string, message modeladapter.Message, persist bool) PromptContextMessage {
	context := PromptContextMessage{
		Source:  strings.TrimSpace(source),
		Message: message,
		Persist: persist,
	}
	context.ContentHash = promptContextContentHash(context.Message)
	return context
}

func normalizePromptContextMessage(context PromptContextMessage) PromptContextMessage {
	context.Source = strings.TrimSpace(context.Source)
	context.Message.Role = strings.TrimSpace(context.Message.Role)
	context.Message.Content = strings.TrimSpace(context.Message.Content)
	context.ContentHash = strings.TrimSpace(context.ContentHash)
	if context.ContentHash == "" {
		context.ContentHash = promptContextContentHash(context.Message)
	}
	return context
}

func clonePromptContextMessage(context PromptContextMessage) PromptContextMessage {
	cloned := normalizePromptContextMessage(context)
	cloned.Message.ContentParts = append([]modeladapter.ContentPart(nil), context.Message.ContentParts...)
	for index := range cloned.Message.ContentParts {
		if image := cloned.Message.ContentParts[index].Image; image != nil {
			imageCopy := *image
			imageCopy.Data = append([]byte(nil), image.Data...)
			cloned.Message.ContentParts[index].Image = &imageCopy
		}
	}
	cloned.Message.ToolCalls = append([]modeladapter.ToolCallDescriptor(nil), context.Message.ToolCalls...)
	cloned.Message.OpenAIResponsesReasoningSummary = append([]byte(nil), context.Message.OpenAIResponsesReasoningSummary...)
	return cloned
}

func clonePromptContextMessages(contexts []PromptContextMessage) []PromptContextMessage {
	if len(contexts) == 0 {
		return nil
	}
	cloned := make([]PromptContextMessage, 0, len(contexts))
	for _, context := range contexts {
		cloned = append(cloned, clonePromptContextMessage(context))
	}
	return cloned
}

func snapshotPromptCompileOptions(stream *ActiveStream) PromptCompileOptions {
	if stream == nil {
		return PromptCompileOptions{}
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	options := stream.PromptCompileOptions
	options.LatestRequestContext = cloneRequestContext(stream.LatestRequestContext)
	options.TransientPromptContexts = clonePromptContextMessages(stream.TransientPromptContexts)
	return options
}

func appendTransientPromptContext(stream *ActiveStream, context PromptContextMessage) bool {
	if stream == nil {
		return false
	}
	context = clonePromptContextMessage(context)
	if !isReplayablePromptContext(context) {
		return false
	}
	key := promptContextKey(context)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, existing := range stream.TransientPromptContexts {
		if promptContextKey(existing) == key {
			return false
		}
	}
	context.Persist = false
	stream.TransientPromptContexts = append(stream.TransientPromptContexts, context)
	stream.UpdatedAt = time.Now().UTC()
	return true
}

func hasTransientPromptContextSource(stream *ActiveStream, source string) bool {
	if stream == nil || strings.TrimSpace(source) == "" {
		return false
	}
	needle := strings.TrimSpace(source)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	for _, context := range stream.TransientPromptContexts {
		if strings.TrimSpace(context.Source) == needle {
			return true
		}
	}
	return false
}

func promptContextContentHash(message modeladapter.Message) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(message.Role) + "\x00" + strings.TrimSpace(message.Content)))
	return hex.EncodeToString(sum[:])
}

func filterCurrentTurnPromptContexts(conversation *ConversationFile, contexts []PromptContextMessage) []PromptContextMessage {
	if len(contexts) == 0 {
		return nil
	}
	seen := collectCurrentTurnPromptContextKeys(conversation)
	filtered := make([]PromptContextMessage, 0, len(contexts))
	for _, context := range contexts {
		context = normalizePromptContextMessage(context)
		if !isReplayablePromptContext(context) {
			continue
		}
		if context.Persist {
			if _, ok := seen[promptContextKey(context)]; ok {
				continue
			}
		}
		filtered = append(filtered, context)
	}
	return filtered
}

func collectCurrentTurnPromptContextKeys(conversation *ConversationFile) map[string]struct{} {
	keys := make(map[string]struct{})
	if conversation == nil {
		return keys
	}
	currentTurnSeq := conversation.NextTurnSeq - 1
	if currentTurnSeq <= 0 {
		return keys
	}
	for _, entry := range conversation.Entries {
		if strings.TrimSpace(entry.Kind) != "prompt_context" {
			continue
		}
		var payload promptContextEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if entry.TurnSeq != currentTurnSeq && strings.TrimSpace(payload.Source) != promptContextSourceSubagentContract {
			continue
		}
		context := normalizePromptContextMessage(PromptContextMessage{
			Source:      payload.Source,
			ContentHash: payload.ContentHash,
			Message: modeladapter.Message{
				Role:    payload.Role,
				Content: payload.Content,
			},
			Persist: true,
		})
		if !isReplayablePromptContext(context) {
			continue
		}
		keys[promptContextKey(context)] = struct{}{}
	}
	return keys
}

func promptContextKey(context PromptContextMessage) string {
	context = normalizePromptContextMessage(context)
	return context.Source + "\x00" + context.ContentHash
}

func isReplayablePromptContext(context PromptContextMessage) bool {
	context = normalizePromptContextMessage(context)
	if context.Source == "" || strings.TrimSpace(context.Message.Content) == "" {
		return false
	}
	if context.Message.Role == "" {
		context.Message.Role = "user"
	}
	if context.Message.Role != "user" && context.Message.Role != "system" {
		return false
	}
	if strings.TrimSpace(context.Message.Name) != "" || strings.TrimSpace(context.Message.ToolCallID) != "" {
		return false
	}
	return len(context.Message.ToolCalls) == 0 && len(context.Message.ContentParts) == 0
}

func newPromptContextEntry(turnSeq int64, requestID string, context PromptContextMessage) HistoryEntry {
	context = normalizePromptContextMessage(context)
	payload, _ := json.Marshal(promptContextEntryPayload{
		Source:      context.Source,
		Role:        firstNonEmpty(strings.TrimSpace(context.Message.Role), "user"),
		Content:     strings.TrimSpace(context.Message.Content),
		ContentHash: context.ContentHash,
	})
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: strings.TrimSpace(requestID),
		Role:      "system",
		Kind:      "prompt_context",
		Payload:   payload,
	}
}
