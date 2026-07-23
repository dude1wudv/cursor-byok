package execbridge

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"cursor/gen/agentv1"
)

func TestCursor31217AgentProtocolFieldNumbers(t *testing.T) {
	tests := []struct {
		message  any
		field    string
		want     int
		wantKind protoreflect.Kind
		wantMap  bool
	}{
		{message: (*agentv1.ShellArgs)(nil), field: "conversation_id", want: 21, wantKind: protoreflect.StringKind},
		{message: (*agentv1.ConversationStateStructure)(nil), field: "subagent_runs_by_parent_tool_call_id", want: 30, wantKind: protoreflect.MessageKind, wantMap: true},
		{message: (*agentv1.ConversationStateStructure)(nil), field: "subagent_state_refs", want: 31, wantKind: protoreflect.BytesKind, wantMap: true},
	}
	for _, tt := range tests {
		var descriptor = agentv1.File_agent_v1_proto.Messages()
		var messageName string
		switch tt.message.(type) {
		case *agentv1.ShellArgs:
			messageName = "ShellArgs"
		case *agentv1.ConversationStateStructure:
			messageName = "ConversationStateStructure"
		}
		message := descriptor.ByName(protoreflect.Name(messageName))
		if message == nil {
			t.Fatalf("message %s not found", messageName)
		}
		field := message.Fields().ByName(protoreflect.Name(tt.field))
		if field == nil || int(field.Number()) != tt.want {
			t.Fatalf("%s.%s number = %v, want %d", messageName, tt.field, field, tt.want)
		}
		if field.IsMap() != tt.wantMap {
			t.Fatalf("%s.%s IsMap = %t, want %t", messageName, tt.field, field.IsMap(), tt.wantMap)
		}
		kind := field.Kind()
		if field.IsMap() {
			kind = field.MapValue().Kind()
		}
		if kind != tt.wantKind {
			t.Fatalf("%s.%s kind = %s, want %s", messageName, tt.field, kind, tt.wantKind)
		}
	}

	if agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_RUNNING != 1 ||
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_BACKGROUNDED != 2 ||
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_SUCCESS != 3 ||
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ERROR != 4 ||
		agentv1.SubagentRunStatus_SUBAGENT_RUN_STATUS_ABORTED != 5 {
		t.Fatal("SubagentRunStatus values do not match Cursor 3.12.17")
	}
}
