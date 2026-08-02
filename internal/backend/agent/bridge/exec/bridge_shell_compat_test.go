package execbridge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

func TestBuildShellRejectedToolCallCarriesPreDispatchReason(t *testing.T) {
	toolCall := BuildShellRejectedToolCall("tool-shell", []byte(`{"command":"git push","working_directory":"E:\\repo"}`), "pre-dispatch rejection: policy")
	shell := toolCall.GetShellToolCall()
	if shell == nil || shell.GetResult().GetRejected() == nil {
		t.Fatalf("explicit rejected shell result missing: %#v", toolCall)
	}
	if shell.GetResult().GetRejected().GetReason() != "pre-dispatch rejection: policy" {
		t.Fatalf("rejection reason=%q", shell.GetResult().GetRejected().GetReason())
	}
	if shell.GetArgs().GetCommand() != "git push" || shell.GetArgs().GetToolCallId() != "tool-shell" {
		t.Fatalf("rejected shell args=%#v", shell.GetArgs())
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

func TestShellHookContextIsActivityButNotTerminal(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:  9,
		ExecID:     "exec-shell-hook",
		ToolCallID: "tool-shell-hook",
		ExecKind:   "shell",
		ArgsJSON:   []byte(`{"command":"git status"}`),
	}
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{
			Event: &agentv1.ShellStream_HookContext{HookContext: &agentv1.ShellStreamHookContext{
				HookAdditionalContexts: []*agentv1.HookAdditionalContext{{HookEventName: "before_shell", Content: "hook"}},
			}},
		}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsTerminal || result.ShellRecoveryCandidate != "" {
		t.Fatalf("hook context finalized or became recovery candidate: %#v", result)
	}
}

func TestShellSandboxUnsupportedIsExplicitTerminalFailure(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:  10,
		ExecID:     "exec-shell-sandbox",
		ToolCallID: "tool-shell-sandbox",
		ExecKind:   "shell",
		ArgsJSON:   []byte(`{"command":"git status"}`),
	}
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellStream{ShellStream: &agentv1.ShellStream{
			Event: &agentv1.ShellStream_SandboxUnsupported{SandboxUnsupported: &agentv1.ShellSandboxUnsupported{Reason: "unsupported"}},
		}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsTerminal || result.ToolCall == nil || result.ToolCall.GetShellToolCall().GetResult().GetRejected() == nil {
		t.Fatalf("sandbox unsupported was not an explicit terminal rejection: %#v", result)
	}
}

func TestLegacyShellOutputWindowIsUsedWhenFullOutputIsMissing(t *testing.T) {
	pending := runtimecore.PendingExec{
		MessageID:  11,
		ExecID:     "exec-shell-window",
		ToolCallID: "tool-shell-window",
		ExecKind:   "shell",
		ArgsJSON:   []byte(`{"command":"git status"}`),
	}
	result, err := NewBridge().ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Id:     pending.MessageID,
		ExecId: pending.ExecID,
		Message: &agentv1.ExecClientMessage_ShellResult{ShellResult: &agentv1.ShellResult{
			Result: &agentv1.ShellResult_Success{Success: &agentv1.ShellSuccess{
				OutputHead:  stringPtr("head"),
				OutputTail:  stringPtr("tail"),
				ElidedChars: uint32Ptr(12),
			}},
		}},
	}, pending)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsTerminal || !strings.Contains(result.ToolResultPayload, "head") || !strings.Contains(result.ToolResultPayload, "tail") {
		t.Fatalf("windowed output was not surfaced: %#v", result)
	}
}

func TestIsSafeInspectShellCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: "git status --short", want: true},
		{command: "git --no-pager --no-optional-locks status --short", want: true},
		{command: `git diff "path with spaces/file.go"`, want: true},
		{command: "tasklist", want: true},
		{command: "TASKLIST.EXE", want: true},
		{command: "git log --oneline -5", want: true},
		{command: "git status | echo bad", want: false},
		{command: "git status; git commit -m bad", want: false},
		{command: "git status > output.txt", want: false},
		{command: "git --no-pager push origin main", want: false},
		{command: "git push origin main", want: false},
		{command: "git commit -m test", want: false},
		{command: "rm -rf /", want: false},
	}
	for _, tt := range tests {
		got := IsSafeInspectShellCommand([]byte(fmt.Sprintf(`{"command":%q}`, tt.command)))
		if got != tt.want {
			t.Fatalf("IsSafeInspectShellCommand(%q)=%t want %t", tt.command, got, tt.want)
		}
	}
}

func TestBuildShellSkippedBackgroundedToolCall(t *testing.T) {
	toolCall := BuildShellSkippedBackgroundedToolCall("tool-git", []byte(`{"command":"git status --short"}`), "Skipped by Cursor")
	shell := toolCall.GetShellToolCall()
	if shell == nil || shell.GetResult() == nil || !shell.GetResult().GetIsBackground() {
		t.Fatalf("expected backgrounded success projection: %#v", toolCall)
	}
	if shell.GetResult().GetRejected() != nil {
		t.Fatal("silent inspect skip must not use rejected result")
	}
	if shell.GetResult().GetSuccess() == nil || shell.GetResult().GetSuccess().GetShellId() != 0 {
		t.Fatalf("synthetic silent skip should use shell_id=0: %#v", shell.GetResult())
	}
}
