package protocol

import (
	"encoding/hex"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestDecodeBidiAppendAgentClientMessageHexAndBinary(t *testing.T) {
	conversationID := "conversation-binary"
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{ConversationId: &conversationID},
		},
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	hexPayload := hex.EncodeToString(payload)

	tests := []struct {
		name       string
		data       string
		dataBinary []byte
	}{
		{name: "legacy hex", data: hexPayload},
		{name: "binary", dataBinary: payload},
		{name: "matching mixed", data: strings.ToUpper(hexPayload), dataBinary: payload},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decoded, kind, canonical, err := DecodeBidiAppendAgentClientMessage(tt.data, tt.dataBinary)
			if err != nil {
				t.Fatal(err)
			}
			if kind != "run_request" || decoded.GetRunRequest().GetConversationId() != conversationID {
				t.Fatalf("decoded kind/message = %q/%v", kind, decoded)
			}
			if canonical != hexPayload {
				t.Fatalf("canonical payload = %q, want %q", canonical, hexPayload)
			}
		})
	}
}

func TestDecodeBidiAppendAgentClientMessageRejectsConflictingPayloads(t *testing.T) {
	conversationID := "conversation-binary"
	message := &agentv1.AgentClientMessage{
		Message: &agentv1.AgentClientMessage_RunRequest{
			RunRequest: &agentv1.AgentRunRequest{ConversationId: &conversationID},
		},
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	otherPayload := append([]byte(nil), payload...)
	otherPayload[len(otherPayload)-1]++
	_, _, _, err = DecodeBidiAppendAgentClientMessage(hex.EncodeToString(payload), otherPayload)
	if err == nil || !strings.Contains(err.Error(), "data_binary conflict") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestDecodeBidiAppendAgentClientMessageAllowsEmptyPayload(t *testing.T) {
	message, kind, canonical, err := DecodeBidiAppendAgentClientMessage("", nil)
	if err != nil || message != nil || kind != "" || canonical != "" {
		t.Fatalf("empty payload = message=%v kind=%q canonical=%q err=%v", message, kind, canonical, err)
	}
}

func TestCursor31321ProtocolFieldNumbers(t *testing.T) {
	appendField := aiserverv1.File_aiserver_v1_proto.Messages().ByName("BidiAppendRequest").Fields().ByName("data_binary")
	if appendField == nil || appendField.Number() != 4 || appendField.Kind() != protoreflect.BytesKind {
		t.Fatalf("BidiAppendRequest.data_binary = %v, want bytes field 4", appendField)
	}
	shellStream := agentv1.File_agent_v1_proto.Messages().ByName("ShellStream")
	for _, field := range []struct {
		name string
		num  protoreflect.FieldNumber
	}{
		{name: "hook_context", num: 8},
		{name: "sandbox_unsupported", num: 9},
	} {
		item := shellStream.Fields().ByName(protoreflect.Name(field.name))
		if item == nil || item.Number() != field.num || item.Kind() != protoreflect.MessageKind {
			t.Fatalf("ShellStream.%s = %v, want message field %d", field.name, item, field.num)
		}
	}

	assertField := func(message protoreflect.MessageDescriptor, name string, number protoreflect.FieldNumber, kind protoreflect.Kind) {
		t.Helper()
		field := message.Fields().ByName(protoreflect.Name(name))
		if field == nil || field.Number() != number || field.Kind() != kind {
			t.Fatalf("%s.%s = %v, want %s field %d", message.FullName(), name, field, kind, number)
		}
	}
	assertOneofFields := func(message protoreflect.MessageDescriptor, fields map[string]protoreflect.FieldNumber) {
		t.Helper()
		for name, number := range fields {
			assertField(message, name, number, protoreflect.MessageKind)
		}
	}

	assertField(agentv1.File_agent_v1_proto.Messages().ByName("AgentRunRequest"), "client_supports_send_to_user", 23, protoreflect.BoolKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("AgentServerMessage"), "ttft_breakdown", 8, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ConversationAction"), "request_context_parts", 17, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ConversationAction"), "subscription_notification_action", 16, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("InteractionQuery"), "connect_scm_request_query", 14, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("InteractionResponse"), "connect_scm_request_response", 14, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("InteractionUpdate"), "response_comparison", 22, protoreflect.MessageKind)

	execClient := agentv1.File_agent_v1_proto.Messages().ByName("ExecClientMessage")
	assertField(execClient, "local_execution_time_ms", 39, protoreflect.Int32Kind)
	assertField(execClient, "hook_additional_contexts", 45, protoreflect.MessageKind)
	assertOneofFields(execClient, map[string]protoreflect.FieldNumber{
		"canvas_diagnostics_result":           40,
		"shell_allowlist_precheck_result":     41,
		"mcp_allowlist_precheck_result":       42,
		"web_fetch_allowlist_precheck_result": 43,
		"git_diff_response":                   44,
		"pi_read_result":                      46,
		"pi_bash_result":                      47,
		"pi_edit_result":                      48,
		"pi_write_result":                     49,
		"pi_grep_result":                      50,
		"pi_find_result":                      51,
		"pi_ls_result":                        52,
		"conversation_search_result":          53,
		"agent_store_conflict_result":         54,
		"mini_swe_agent_bash_result":          55,
	})

	execServer := agentv1.File_agent_v1_proto.Messages().ByName("ExecServerMessage")
	assertField(execServer, "accept_hook_additional_contexts", 55, protoreflect.BoolKind)
	assertOneofFields(execServer, map[string]protoreflect.FieldNumber{
		"canvas_diagnostics_args":           40,
		"shell_allowlist_precheck_args":     41,
		"mcp_allowlist_precheck_args":       42,
		"web_fetch_allowlist_precheck_args": 43,
		"git_diff_request":                  44,
		"pi_read_args":                      45,
		"pi_bash_args":                      46,
		"pi_edit_args":                      47,
		"pi_write_args":                     48,
		"pi_grep_args":                      49,
		"pi_find_args":                      50,
		"pi_ls_args":                        51,
		"mini_swe_agent_bash_args":          52,
		"conversation_search_args":          53,
		"agent_store_conflict_args":         54,
	})

	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ConversationTokenDetails"), "prompt_context_usage_tree", 4, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ShellArgs"), "output_notification", 18, protoreflect.BytesKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ShellFailure"), "elided_chars", 15, protoreflect.Uint32Kind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ShellSuccess"), "elided_chars", 17, protoreflect.Uint32Kind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ShellStreamHookContext"), "hook_additional_contexts", 1, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ShellSandboxUnsupported"), "reason", 4, protoreflect.StringKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ToolCall"), "hook_additional_contexts", 54, protoreflect.MessageKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ToolCall"), "tool_call_id", 57, protoreflect.StringKind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ToolCall"), "started_at_ms", 59, protoreflect.Uint64Kind)
	assertField(agentv1.File_agent_v1_proto.Messages().ByName("ToolCall"), "completed_at_ms", 60, protoreflect.Uint64Kind)
}
