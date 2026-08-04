package forwarder

import (
	"fmt"
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestReserveTaskDispatchLimitsBySubagentDepth(t *testing.T) {
	stream := &ActiveStream{ProviderPassCount: 1}
	for index := 1; index <= 4; index++ {
		invocation := runtimecore.ToolInvocation{ToolName: "Task", CallID: fmt.Sprintf("task-%d", index)}
		if dispatch, err := reserveTaskDispatch(stream, invocation); err != nil || !dispatch {
			t.Fatalf("dispatch %d failed: %v", index, err)
		}
	}
	if _, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-5"}); err == nil {
		t.Fatal("fifth task should be rejected")
	}
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-1"}); err != nil || dispatch {
		t.Fatalf("replayed call should be suppressed without error: dispatch=%t err=%v", dispatch, err)
	}
	if _, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task"}); err == nil {
		t.Fatal("task without call ID should be rejected")
	}
	stream.ProviderPassCount = 2
	if _, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-5"}); err == nil {
		t.Fatal("provider pass must not reset the first-level tree budget")
	}
	stream.SubagentDepth = 1
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "child-1"}); err != nil || !dispatch {
		t.Fatalf("second-level dispatch 1 failed: %v", err)
	}
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "child-2"}); err != nil || !dispatch {
		t.Fatalf("second-level dispatch 2 failed: %v", err)
	}
	if _, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "child-3"}); err == nil {
		t.Fatal("third second-level task should be rejected")
	}
	stream.SubagentDepth = 2
	if _, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "grandchild-1"}); err == nil {
		t.Fatal("third-level dispatch should be rejected")
	}
}

func TestTaskDispatchSlotReleasedAfterSubagentTerminal(t *testing.T) {
	stream := &ActiveStream{}
	for index := 1; index <= 4; index++ {
		callID := fmt.Sprintf("task-%d", index)
		if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: callID}); err != nil || !dispatch {
			t.Fatalf("reserve %s: dispatch=%t err=%v", callID, dispatch, err)
		}
	}
	markExecCompleted(stream, runtimecore.PendingExec{ExecID: "exec-1", ExecKind: "subagent", ToolCallID: "task-1"})
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-5"}); err != nil || !dispatch {
		t.Fatalf("released first-level slot was not reusable: dispatch=%t err=%v", dispatch, err)
	}
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-1"}); err != nil || dispatch {
		t.Fatalf("completed CallID replay must remain suppressed: dispatch=%t err=%v", dispatch, err)
	}
}

func TestSecondLevelTaskDispatchSlotReleasedAfterSubagentTerminal(t *testing.T) {
	stream := &ActiveStream{SubagentDepth: 1}
	for index := 1; index <= 2; index++ {
		callID := fmt.Sprintf("child-%d", index)
		if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: callID}); err != nil || !dispatch {
			t.Fatalf("reserve %s: dispatch=%t err=%v", callID, dispatch, err)
		}
	}
	markExecCompleted(stream, runtimecore.PendingExec{ExecID: "exec-child-1", ExecKind: "subagent", ToolCallID: "child-1"})
	if dispatch, err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "child-3"}); err != nil || !dispatch {
		t.Fatalf("released second-level slot was not reusable: dispatch=%t err=%v", dispatch, err)
	}
}
