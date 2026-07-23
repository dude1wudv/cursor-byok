package execbridge

import (
	"encoding/json"
	"reflect"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenShellBuildsCursorCompatibleParsingMetadata(t *testing.T) {
	tests := []struct {
		name               string
		command            string
		wantParsingFailed  bool
		wantSimpleCommands []string
		wantExecutable     string
	}{
		{
			name:               "simple command",
			command:            "git status --short --branch",
			wantSimpleCommands: []string{"git"},
			wantExecutable:     "git",
		},
		{
			name:              "powershell environment assignment",
			command:           "$env:HTTP_PROXY = 'http://127.0.0.1:7890'",
			wantParsingFailed: true,
		},
		{
			name:              "powershell variable assignment",
			command:           "$files = Get-ChildItem | Where-Object { $_.Length -gt 0 }",
			wantParsingFailed: true,
		},
		{
			name:              "powershell here string pipeline",
			command:           "@'\nhello\n'@ | Set-Content output.txt",
			wantParsingFailed: true,
		},
		{
			name:              "quoted gofmt command",
			command:           `gofmt -w "internal/backend/agent/bridge/exec/bridge.go"`,
			wantParsingFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"command":           tt.command,
				"working_directory": `E:\workspace`,
			})
			if err != nil {
				t.Fatal(err)
			}

			message, pending, err := NewBridge().OpenExec(OpenExecContext{ConversationID: "conversation-31217"}, runtimecore.ToolInvocation{
				CallID:   "tool-call",
				ToolName: "Shell",
				ArgsJSON: payload,
			})
			if err != nil {
				t.Fatal(err)
			}
			args := message.GetExecServerMessage().GetShellStreamArgs()
			if args == nil {
				t.Fatal("ShellStreamArgs is nil")
			}
			if args.GetParsingResult() == nil {
				t.Fatal("ParsingResult is nil; Cursor rejects shell streams without it")
			}
			if args.GetParsingResult().GetParsingFailed() != tt.wantParsingFailed {
				t.Fatalf("ParsingFailed = %t, want %t", args.GetParsingResult().GetParsingFailed(), tt.wantParsingFailed)
			}
			if !reflect.DeepEqual(args.GetSimpleCommands(), tt.wantSimpleCommands) {
				t.Fatalf("SimpleCommands = %#v, want %#v", args.GetSimpleCommands(), tt.wantSimpleCommands)
			}
			executables := args.GetParsingResult().GetExecutableCommands()
			if tt.wantExecutable == "" {
				if len(executables) != 0 {
					t.Fatalf("ExecutableCommands = %#v, want none", executables)
				}
			} else if len(executables) != 1 || executables[0].GetName() != tt.wantExecutable || executables[0].GetFullText() != tt.command {
				t.Fatalf("ExecutableCommands = %#v, want one %q command", executables, tt.wantExecutable)
			}
			if args.GetConversationId() != "conversation-31217" || pending.ConversationID != "conversation-31217" {
				t.Fatalf("ConversationId = %q pending=%q, want conversation-31217", args.GetConversationId(), pending.ConversationID)
			}
			if args.GetCommand() != tt.command {
				t.Fatalf("Command = %q, want unchanged %q", args.GetCommand(), tt.command)
			}
			if args.GetTimeout() != 30000 {
				t.Fatalf("Timeout = %d, want 30000", args.GetTimeout())
			}
			if args.FileOutputThresholdBytes == nil || args.GetFileOutputThresholdBytes() != 40000 {
				t.Fatalf("FileOutputThresholdBytes = %v, want 40000", args.FileOutputThresholdBytes)
			}
			if args.GetTimeoutBehavior() != agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND {
				t.Fatalf("TimeoutBehavior = %s, want background", args.GetTimeoutBehavior())
			}
			if args.HardTimeout == nil || args.GetHardTimeout() != 86400000 {
				t.Fatalf("HardTimeout = %v, want 86400000", args.HardTimeout)
			}
		})
	}
}

func TestSubagentBackgroundAckIsNonTerminal(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:  11,
		ExecID:     "exec-subagent",
		ToolCallID: "tool-subagent",
		ExecKind:   "subagent",
		ArgsJSON:   []byte(`{"description":"inspect","prompt":"check"}`),
	}
	ack := &agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_SubagentResult{SubagentResult: &agentv1.SubagentResult{
			Result: &agentv1.SubagentResult_Success{Success: &agentv1.SubagentSuccess{
				AgentId:          "agent-1",
				BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_USER_REQUEST,
			}},
		}},
	}
	result, err := NewBridge().ApplyExecClientMessage(ack, pending)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsTerminal {
		t.Fatal("background ack without final message was terminal")
	}
	if result.ToolCall == nil || !result.ToolCall.GetTaskToolCall().GetResult().GetSuccess().GetIsBackground() {
		t.Fatalf("background projection missing: %#v", result.ToolCall)
	}

	ack.GetSubagentResult().GetSuccess().FinalMessage = stringPtr("done")
	result, err = NewBridge().ApplyExecClientMessage(ack, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsTerminal {
		t.Fatal("final subagent message did not become terminal")
	}
}

func TestShellStreamCloseRemainsPendingUntilLateExit(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:      8,
		ExecID:         "exec-shell-close",
		ConversationID: "conversation-31217",
		ToolCallID:     "tool-shell-close",
		ExecKind:       "shell",
		ArgsJSON:       []byte(`{"command":"git status"}`),
	}
	bridge := NewBridge()
	control, err := bridge.ApplyExecClientControl(&agentv1.ExecClientControlMessage{
		Message: &agentv1.ExecClientControlMessage_StreamClose{StreamClose: &agentv1.ExecClientStreamClose{Id: pending.MessageID}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if control.IsTerminal || control.ToolCall != nil {
		t.Fatalf("stream close finalized shell: %#v", control)
	}
	late, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id: pending.MessageID, ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Exit{Exit: &agentv1.ShellStreamExit{Code: 0}}}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !late.IsTerminal {
		t.Fatal("late exit did not finalize shell after stream close")
	}
}

func TestShellApprovalSkipAndUnknownPayloadRemainPending(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:      7,
		ExecID:         "exec-shell",
		ConversationID: "conversation-31217",
		ToolCallID:     "tool-shell",
		ExecKind:       "shell",
		ArgsJSON:       []byte(`{"command":"git status"}`),
	}
	bridge := NewBridge()
	for _, message := range []*agentv1.ExecClientMessage{
		{Id: 7, ExecId: "exec-shell"},
		{Id: 7, ExecId: "exec-shell", Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{}}},
		{Id: 7, ExecId: "exec-shell", Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Rejected{Rejected: &agentv1.ShellRejected{Reason: "Skipped by Cursor"}}}}},
	} {
		result, err := bridge.ApplyExecClientMessage(message, pending)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsTerminal || result.ToolCall != nil {
			t.Fatalf("non-authoritative payload finalized shell: %#v", result)
		}
	}

	result, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id: 7, ExecId: "exec-shell",
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{Event: &agentv1.ShellStream_Exit{Exit: &agentv1.ShellStreamExit{Code: 0}}}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsTerminal || result.ToolCall.GetShellToolCall().GetArgs().GetConversationId() != "conversation-31217" {
		t.Fatalf("authoritative exit did not finalize with conversation id: %#v", result)
	}
}
