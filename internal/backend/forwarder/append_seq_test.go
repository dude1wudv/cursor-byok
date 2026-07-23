package forwarder

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
)

func TestAppendSequenceOrdersAndDeduplicatesWithinEpoch(t *testing.T) {
	tracker := newAppendSequenceTracker()
	first, stale, err := tracker.Acquire(context.Background(), "request", 1)
	if err != nil || stale {
		t.Fatalf("acquire seq 1: stale=%t err=%v", stale, err)
	}

	type acquireResult struct {
		ticket appendSequenceTicket
		stale  bool
		err    error
	}
	secondResult := make(chan acquireResult, 1)
	go func() {
		ticket, stale, err := tracker.Acquire(context.Background(), "request", 2)
		secondResult <- acquireResult{ticket: ticket, stale: stale, err: err}
	}()
	select {
	case result := <-secondResult:
		t.Fatalf("seq 2 did not wait for seq 1: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}

	first.Release()
	result := <-secondResult
	if result.err != nil || result.stale {
		t.Fatalf("acquire seq 2: stale=%t err=%v", result.stale, result.err)
	}
	result.ticket.Release()

	_, stale, err = tracker.Acquire(context.Background(), "request", 2)
	if err != nil || !stale {
		t.Fatalf("duplicate seq 2: stale=%t err=%v", stale, err)
	}
}

func TestAppendSequenceNewEpochStartsAtOneAndRetiresOldWaiters(t *testing.T) {
	tracker := newAppendSequenceTracker()
	first, stale, err := tracker.Acquire(context.Background(), "request", 1)
	if err != nil || stale {
		t.Fatalf("acquire first epoch: stale=%t err=%v", stale, err)
	}
	first.Release()

	oldWaiter := make(chan bool, 1)
	oldState := first.state
	go func() {
		stale, err := oldState.acquire(context.Background(), 3)
		if err != nil {
			oldWaiter <- false
			return
		}
		oldState.Release(3)
		oldWaiter <- stale
	}()

	candidate, stale, err := tracker.Acquire(context.Background(), "request", 1)
	if err != nil || stale {
		t.Fatalf("acquire epoch candidate: stale=%t err=%v", stale, err)
	}
	if epoch, _, disposition := candidate.Snapshot(); epoch != 2 || disposition != "epoch_candidate" {
		t.Fatalf("candidate snapshot: epoch=%d disposition=%q", epoch, disposition)
	}
	if !candidate.CommitEpoch() {
		t.Fatal("new run did not commit append epoch")
	}
	candidate.Release()
	if stale := <-oldWaiter; !stale {
		t.Fatal("old epoch waiter was not retired as stale")
	}

	second, stale, err := tracker.Acquire(context.Background(), "request", 2)
	if err != nil || stale {
		t.Fatalf("new epoch seq 2: stale=%t err=%v", stale, err)
	}
	second.Release()
}

func TestAppendSequenceStaleTrafficDoesNotRenewEpoch(t *testing.T) {
	tracker := newAppendSequenceTracker()
	first, stale, err := tracker.Acquire(context.Background(), "request", 1)
	if err != nil || stale {
		t.Fatalf("acquire seq 1: stale=%t err=%v", stale, err)
	}
	first.Release()
	second, stale, err := tracker.Acquire(context.Background(), "request", 2)
	if err != nil || stale {
		t.Fatalf("acquire seq 2: stale=%t err=%v", stale, err)
	}
	second.Release()

	request := tracker.states["request"]
	request.mu.Lock()
	state := request.current
	request.mu.Unlock()
	state.mu.Lock()
	before := state.updatedAt
	state.mu.Unlock()
	time.Sleep(time.Millisecond)

	_, stale, err = tracker.Acquire(context.Background(), "request", 2)
	if err != nil || !stale {
		t.Fatalf("stale seq 2: stale=%t err=%v", stale, err)
	}
	state.mu.Lock()
	after := state.updatedAt
	state.mu.Unlock()
	if !after.Equal(before) {
		t.Fatalf("stale traffic renewed epoch: before=%s after=%s", before, after)
	}
}

func TestBidiAppendReusedRequestStartsNewRunEpoch(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	projector := NewHistoryProjector()
	broker := NewStreamBroker()
	service := newServiceWithDependencies(store, projector, nil, nil, broker)
	requestID := "reused-request"

	appendPrewarm(t, service, requestID, "conversation-one", 1)
	firstEpoch := currentAppendEpoch(t, service.appendSeq, requestID)
	if firstEpoch != 1 {
		t.Fatalf("first epoch=%d, want 1", firstEpoch)
	}

	subscriberID, _, _, _, err := broker.Subscribe(requestID)
	if err != nil {
		t.Fatal(err)
	}
	broker.Unsubscribe(requestID, subscriberID)
	appendPrewarm(t, service, requestID, "conversation-one", 1)
	if epoch := currentAppendEpoch(t, service.appendSeq, requestID); epoch != firstEpoch {
		t.Fatalf("RunSSE reconnect/duplicate run reset epoch: got=%d want=%d", epoch, firstEpoch)
	}

	if err := broker.Complete(requestID, "", ""); err != nil {
		t.Fatal(err)
	}
	if !broker.RemoveIfIdle(requestID) {
		t.Fatal("completed first run was not removed")
	}
	appendPrewarm(t, service, requestID, "conversation-two", 1)
	if epoch := currentAppendEpoch(t, service.appendSeq, requestID); epoch != firstEpoch+1 {
		t.Fatalf("second run epoch=%d, want %d", epoch, firstEpoch+1)
	}
	stream, ok := broker.Get(requestID)
	if !ok || stream == nil || stream.ConversationID != "conversation-two" || !stream.RunAccepted {
		t.Fatalf("second run was not accepted: %#v", stream)
	}
}

func appendPrewarm(t *testing.T, service *Service, requestID string, conversationID string, seq int64) {
	t.Helper()
	mode := agentv1.AgentMode_AGENT_MODE_AGENT
	message := &agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_PrewarmRequest{
		PrewarmRequest: &agentv1.PrewarmRequest{
			ConversationId:    stringPtr(conversationID),
			ConversationState: &agentv1.ConversationStateStructure{Mode: &mode},
		},
	}}
	payload, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BidiAppend(context.Background(), connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId:   &aiserverv1.BidiRequestId{RequestId: requestID},
		AppendSeqno: seq,
		Data:        hex.EncodeToString(payload),
	}))
	if err != nil {
		t.Fatalf("BidiAppend seq=%d: %v", seq, err)
	}
}

func currentAppendEpoch(t *testing.T, tracker *appendSequenceTracker, requestID string) uint64 {
	t.Helper()
	tracker.mu.Lock()
	request := tracker.states[requestID]
	tracker.mu.Unlock()
	if request == nil {
		t.Fatal("append request state is missing")
	}
	request.mu.Lock()
	state := request.current
	request.mu.Unlock()
	if state == nil {
		t.Fatal("current append epoch is missing")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.epoch
}
