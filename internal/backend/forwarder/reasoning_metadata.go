// reasoning_metadata.go 保存 history/replay 共用的 reasoning metadata 判定。
package forwarder

import (
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
)

func hasReplayableReasoningPayload(reasoningContent string, reasoningSignature string, reasoningSignatureSource string) bool {
	if strings.TrimSpace(reasoningContent) != "" {
		return true
	}
	if strings.TrimSpace(reasoningSignature) == "" {
		return false
	}
	switch strings.TrimSpace(reasoningSignatureSource) {
	case modeladapter.ReasoningSignatureSourceOpenAIResponses,
		modeladapter.ReasoningSignatureSourceAnthropicRedacted:
		return true
	default:
		return false
	}
}
