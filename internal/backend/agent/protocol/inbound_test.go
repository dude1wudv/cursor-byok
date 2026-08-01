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
}
