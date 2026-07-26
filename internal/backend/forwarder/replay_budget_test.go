package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

func newBudgetTestToolResultEntry(seq int64, turnSeq int64, toolCallID string, resultBytes int) HistoryEntry {
	payload, _ := json.Marshal(toolResultEntryPayload{
		ToolCallID: toolCallID,
		ToolName:   "Read",
		ResultText: strings.Repeat("a", resultBytes),
	})
	return HistoryEntry{
		Seq:        seq,
		TurnSeq:    turnSeq,
		RequestID:  "request",
		Role:       "tool",
		Kind:       "tool_result",
		ToolCallID: toolCallID,
		Payload:    payload,
	}
}

func newBudgetTestToolCallEntry(t *testing.T, seq int64, turnSeq int64, toolCallID string) HistoryEntry {
	t.Helper()
	toolCallJSON, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{Args: &agentv1.ReadToolArgs{Path: "a.go"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(toolCallEntryPayload{
		ToolCallID: toolCallID,
		ToolName:   "Read",
		ToolCall:   toolCallJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	return HistoryEntry{
		Seq:        seq,
		TurnSeq:    turnSeq,
		RequestID:  "request",
		Role:       "assistant",
		Kind:       "tool_call",
		ToolCallID: toolCallID,
		Payload:    payload,
	}
}

func TestMaybeAdvanceReplayBudgetBoundaryMonotonic(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request", "conversation", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "test")
	if err != nil {
		t.Fatal(err)
	}
	conversation := &ConversationFile{ConversationID: "conversation", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 100}
	// 12 个 64KiB 结果 = 768KiB，超过 512KiB 预算。
	const resultCount = 12
	for index := 0; index < resultCount; index++ {
		conversation.Entries = append(conversation.Entries, newBudgetTestToolResultEntry(int64(index+1), 1, fmt.Sprintf("tool-%d", index+1), 64*1024))
	}
	stream.CheckpointConversation = conversation
	service := &Service{broker: broker, projector: NewHistoryProjector(), debug: newDebugRecorder("", broker, nil)}

	if err := service.maybeAdvanceReplayBudgetBoundary(stream, conversation); err != nil {
		t.Fatal(err)
	}
	boundary := replayBudgetBoundarySeq(stream.CheckpointConversation.Entries)
	wantBoundary := int64(resultCount - replayBudgetKeepFullResults) // 第 (N-8) 条：最新 8 条豁免
	if boundary != wantBoundary {
		t.Fatalf("boundary = %d, want %d", boundary, wantBoundary)
	}

	// 再次调用不得回退或重复推进（边界之外剩余字节已在预算内）。
	entriesBefore := len(stream.CheckpointConversation.Entries)
	if err := service.maybeAdvanceReplayBudgetBoundary(stream, stream.CheckpointConversation); err != nil {
		t.Fatal(err)
	}
	if len(stream.CheckpointConversation.Entries) != entriesBefore {
		t.Fatal("second call appended another boundary without budget growth")
	}
	if again := replayBudgetBoundarySeq(stream.CheckpointConversation.Entries); again != boundary {
		t.Fatalf("boundary moved without growth: %d -> %d", boundary, again)
	}
}

func TestProjectPromptReplayAppliesBudgetBoundaryDeterministically(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation", Mode: "agent"}
	const resultCount = 10
	seq := int64(0)
	resultSeqs := make([]int64, 0, resultCount)
	for index := 0; index < resultCount; index++ {
		toolCallID := fmt.Sprintf("tool-%d", index+1)
		seq++
		conversation.Entries = append(conversation.Entries, newBudgetTestToolCallEntry(t, seq, 1, toolCallID))
		seq++
		conversation.Entries = append(conversation.Entries, newBudgetTestToolResultEntry(seq, 1, toolCallID, 16*1024))
		resultSeqs = append(resultSeqs, seq)
	}
	conversation.Entries = append(conversation.Entries, newMetadataEntry(1, "request", "replay_budget_boundary", map[string]any{
		"boundary_seq": resultSeqs[1],
	}))
	projector := NewHistoryProjector()
	first, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatal(err)
	}
	compressed, full := 0, 0
	for _, message := range first {
		if message.Role != "tool" {
			continue
		}
		if len(message.Content) <= replayBudgetCompressedLimit {
			compressed++
		} else {
			full++
		}
	}
	if compressed != 2 || full != resultCount-2 {
		t.Fatalf("compressed=%d full=%d, want 2/%d (boundary_seq=2)", compressed, full, resultCount-2)
	}
	// 前缀稳定性：同一历史多次投影必须逐字节一致。
	second, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("replay length changed across passes: %d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index].Content != second[index].Content {
			t.Fatalf("replay content differs across passes at index %d", index)
		}
	}
}

func TestCompactionSoftThresholdHysteresis(t *testing.T) {
	const window = int64(200000)
	tests := []struct {
		name     string
		tokens   int64
		baseline int64
		want     bool
	}{
		{name: "below soft threshold", tokens: 140000, want: false},
		{name: "above threshold no baseline", tokens: 140001, want: true},
		{name: "above threshold but under rearm growth", tokens: 150000, baseline: 141000, want: false},
		{name: "above threshold with rearm growth", tokens: 181000, baseline: 141000, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conversation := &ConversationFile{SoftCompactionBaselineTokens: tt.baseline}
			if got := compactionSoftThresholdExceeded(conversation, tt.tokens, window); got != tt.want {
				t.Fatalf("compactionSoftThresholdExceeded(%d, baseline=%d) = %t, want %t", tt.tokens, tt.baseline, got, tt.want)
			}
		})
	}
}
