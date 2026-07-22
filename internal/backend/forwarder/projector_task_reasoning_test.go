package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestPendingTaskReasoningHasSingleCheckpointSource(t *testing.T) {
	const (
		toolCallID = "task-call-1"
		reasoning  = "delegate this investigation"
	)
	toolCall := &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_TaskToolCall{
			TaskToolCall: &agentv1.TaskToolCall{
				Args: &agentv1.TaskArgs{Description: "investigate", Prompt: "find the cause"},
			},
		},
	}
	toolCallJSON, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatal(err)
	}
	entryPayload, err := json.Marshal(toolCallEntryPayload{
		ToolCallID:       toolCallID,
		ToolName:         "Task",
		ReasoningContent: reasoning,
		ToolCall:         toolCallJSON,
	})
	if err != nil {
		t.Fatal(err)
	}

	state, err := NewHistoryProjector().ProjectLegacyCheckpoint(&ConversationFile{
		Mode:        "agent",
		NextTurnSeq: 2,
		Entries: []HistoryEntry{{
			Seq: 1, TurnSeq: 1, Kind: "tool_call", ToolCallID: toolCallID, Payload: entryPayload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	pending := buildPendingToolCalls([]runtimecore.PendingExec{{
		MessageID:        1,
		ToolCallID:       toolCallID,
		ExecKind:         "subagent",
		ArgsJSON:         []byte(`{"description":"investigate","prompt":"find the cause"}`),
		ReasoningContent: reasoning,
	}}, nil)
	if len(pending) != 1 {
		t.Fatalf("pending tool calls = %d, want 1", len(pending))
	}

	stableReasoningCount := countCheckpointThinkingText(t, state, reasoning)
	pendingReasoningCount := strings.Count(pending[0], reasoning)
	if got := stableReasoningCount + pendingReasoningCount; got != 1 {
		t.Fatalf("reasoning checkpoint sources = %d (stable=%d pending=%d), want 1", got, stableReasoningCount, pendingReasoningCount)
	}
}

func TestCompletedTaskResultReasoningIsNotProjectedAsThinking(t *testing.T) {
	const reasoning = "delegate this investigation"
	toolCallJSON, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_TaskToolCall{
			TaskToolCall: &agentv1.TaskToolCall{Args: &agentv1.TaskArgs{Description: "investigate"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entryPayload, err := json.Marshal(toolResultEntryPayload{
		ToolCallID:       "task-call-1",
		ToolName:         "Task",
		ReasoningContent: reasoning,
		ToolCall:         toolCallJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewHistoryProjector().ProjectLegacyCheckpoint(&ConversationFile{
		Mode:        "agent",
		NextTurnSeq: 2,
		Entries: []HistoryEntry{{
			Seq: 1, TurnSeq: 1, Kind: "tool_result", ToolCallID: "task-call-1", Payload: entryPayload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := countCheckpointThinkingText(t, state, reasoning); got != 0 {
		t.Fatalf("stable Task result reasoning count = %d, want 0", got)
	}
}

func countCheckpointThinkingText(t *testing.T, state *agentv1.ConversationStateStructure, want string) int {
	t.Helper()
	count := 0
	for _, rawTurn := range state.GetTurns() {
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			t.Fatal(err)
		}
		for _, rawStep := range turn.GetAgentConversationTurn().GetSteps() {
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(rawStep, step); err != nil {
				t.Fatal(err)
			}
			if step.GetThinkingMessage().GetText() == want {
				count++
			}
		}
	}
	return count
}
