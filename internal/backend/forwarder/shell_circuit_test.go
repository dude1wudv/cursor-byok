package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

func TestShouldOpenShellCircuit(t *testing.T) {
	tests := []struct {
		name            string
		state           shellCircuitState
		rejectionClass  string
		wantOpenCircuit bool
	}{
		{name: "capability rejection", rejectionClass: "capability", wantOpenCircuit: true},
		{name: "permission rejection", rejectionClass: "permission", wantOpenCircuit: true},
		{name: "first parser rejection", rejectionClass: "command_parse"},
		{name: "second parser rejection", state: shellCircuitState{ParseRejections: 1}, rejectionClass: "command_parse", wantOpenCircuit: true},
		{name: "already open", state: shellCircuitState{Open: true}, rejectionClass: "capability"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldOpenShellCircuit(tt.state, tt.rejectionClass); got != tt.wantOpenCircuit {
				t.Fatalf("shouldOpenShellCircuit() = %t, want %t", got, tt.wantOpenCircuit)
			}
		})
	}
}

func TestShellTerminalRejectionClassifiesSkippedStream(t *testing.T) {
	message := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ShellStream{
			ShellStream: &agentv1.ShellStream{
				Event: &agentv1.ShellStream_Rejected{
					Rejected: &agentv1.ShellRejected{Reason: "Skipped by Cursor"},
				},
			},
		},
	}
	reason, class := shellTerminalRejection(message)
	if reason != "Skipped by Cursor" || class != "capability" {
		t.Fatalf("shellTerminalRejection() = (%q, %q), want skipped capability rejection", reason, class)
	}
}

func TestCurrentTurnShellCircuitIgnoresTransportStall(t *testing.T) {
	const turnSeq = int64(7)
	stream := &ActiveStream{
		TurnSeq: turnSeq,
		CheckpointConversation: &ConversationFile{Entries: []HistoryEntry{
			newMetadataEntry(turnSeq, "request", "shell_stream_stalled", map[string]any{
				"reason": "transport_closed",
			}),
		}},
	}
	if circuit := currentTurnShellCircuit(stream); circuit.Open {
		t.Fatal("transport-only shell stall opened the rejection circuit")
	}
}
