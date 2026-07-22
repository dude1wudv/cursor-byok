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
			name:              "complex powershell command",
			command:           "$files = Get-ChildItem | Where-Object { $_.Length -gt 0 }",
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

			message, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{
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
