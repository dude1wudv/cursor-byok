package forwarder

import (
	"fmt"
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestReserveTaskDispatchLimitsEachProviderPass(t *testing.T) {
	stream := &ActiveStream{ProviderPassCount: 1}
	for index := 1; index <= 4; index++ {
		invocation := runtimecore.ToolInvocation{ToolName: "Task", CallID: fmt.Sprintf("task-%d", index)}
		if err := reserveTaskDispatch(stream, invocation); err != nil {
			t.Fatalf("dispatch %d failed: %v", index, err)
		}
	}
	if err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-5"}); err == nil {
		t.Fatal("fifth task should be rejected")
	}
	if err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-1"}); err != nil {
		t.Fatalf("replayed call should be idempotent: %v", err)
	}
	if err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task"}); err == nil {
		t.Fatal("task without call ID should be rejected")
	}
	stream.ProviderPassCount = 2
	if err := reserveTaskDispatch(stream, runtimecore.ToolInvocation{ToolName: "Task", CallID: "task-5"}); err != nil {
		t.Fatalf("new provider pass should have a fresh limit: %v", err)
	}
}
