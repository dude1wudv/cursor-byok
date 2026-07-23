package forwarder

import (
	"context"
	"strings"
	"sync"
	"time"
)

const appendSequenceRetention = 10 * time.Minute

type appendSequenceTracker struct {
	mu     sync.Mutex
	states map[string]*appendSequenceRequestState
}

type appendSequenceRequestState struct {
	mu        sync.Mutex
	current   *appendSequenceState
	candidate *appendSequenceState
	nextEpoch uint64
}

type appendSequenceState struct {
	mu         sync.Mutex
	epoch      uint64
	next       int64
	processing bool
	retired    bool
	ready      chan struct{}
	updatedAt  time.Time
}

type appendSequenceTicket struct {
	request     *appendSequenceRequestState
	state       *appendSequenceState
	seq         int64
	disposition string
}

func newAppendSequenceTracker() *appendSequenceTracker {
	return &appendSequenceTracker{
		states: make(map[string]*appendSequenceRequestState),
	}
}

func (tracker *appendSequenceTracker) Acquire(ctx context.Context, requestID string, appendSeq int64) (appendSequenceTicket, bool, error) {
	if tracker == nil || strings.TrimSpace(requestID) == "" || appendSeq <= 0 {
		return appendSequenceTicket{}, false, nil
	}
	request := tracker.state(strings.TrimSpace(requestID))
	state, disposition := request.selectState(appendSeq)
	ticket := appendSequenceTicket{
		request:     request,
		state:       state,
		seq:         appendSeq,
		disposition: disposition,
	}
	stale, err := state.acquire(ctx, appendSeq)
	if err != nil || stale {
		return ticket, stale, err
	}
	return ticket, false, nil
}

func (tracker *appendSequenceTracker) state(requestID string) *appendSequenceRequestState {
	now := time.Now().UTC()
	cutoff := now.Add(-appendSequenceRetention)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for key, state := range tracker.states {
		if state == nil || state.expired(cutoff) {
			delete(tracker.states, key)
		}
	}
	if state, ok := tracker.states[requestID]; ok && state != nil {
		return state
	}
	state := &appendSequenceRequestState{nextEpoch: 1}
	state.current = newAppendSequenceState(state.nextEpoch, now)
	tracker.states[requestID] = state
	return state
}

func newAppendSequenceState(epoch uint64, now time.Time) *appendSequenceState {
	return &appendSequenceState{
		epoch:     epoch,
		next:      1,
		ready:     make(chan struct{}),
		updatedAt: now,
	}
}

func (request *appendSequenceRequestState) selectState(appendSeq int64) (*appendSequenceState, string) {
	now := time.Now().UTC()
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.current == nil {
		if request.nextEpoch == 0 {
			request.nextEpoch = 1
		}
		request.current = newAppendSequenceState(request.nextEpoch, now)
	}
	if appendSeq == 1 && request.current.currentNext() > 1 {
		if request.candidate == nil || request.candidate.isRetired() {
			request.candidate = newAppendSequenceState(request.nextEpoch+1, now)
		}
		return request.candidate, "epoch_candidate"
	}
	return request.current, "current_epoch"
}

func (request *appendSequenceRequestState) commit(candidate *appendSequenceState) bool {
	if request == nil || candidate == nil {
		return false
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.candidate != candidate {
		return false
	}
	previous := request.current
	request.current = candidate
	request.nextEpoch = candidate.epoch
	request.candidate = nil
	if previous != nil && previous != candidate {
		previous.retire()
	}
	return true
}

func (request *appendSequenceRequestState) discard(candidate *appendSequenceState) {
	if request == nil || candidate == nil {
		return
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.candidate != candidate {
		return
	}
	request.candidate = nil
	candidate.retire()
}

func (request *appendSequenceRequestState) expired(cutoff time.Time) bool {
	if request == nil {
		return true
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.current != nil && !request.current.expired(cutoff) {
		return false
	}
	return request.candidate == nil || request.candidate.expired(cutoff)
}

func (state *appendSequenceState) acquire(ctx context.Context, appendSeq int64) (bool, error) {
	for {
		state.mu.Lock()
		if state.retired {
			state.mu.Unlock()
			return true, nil
		}
		if state.next <= 0 {
			state.next = 1
		}
		if state.ready == nil {
			state.ready = make(chan struct{})
		}
		switch {
		case appendSeq < state.next:
			state.mu.Unlock()
			return true, nil
		case appendSeq == state.next && !state.processing:
			state.processing = true
			state.updatedAt = time.Now().UTC()
			state.mu.Unlock()
			return false, nil
		default:
			ready := state.ready
			state.mu.Unlock()
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-ready:
			}
		}
	}
}

func (state *appendSequenceState) Release(seq int64) {
	if state == nil || seq <= 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.retired {
		return
	}
	if state.processing && state.next == seq {
		state.processing = false
		state.next++
		close(state.ready)
		state.ready = make(chan struct{})
	}
	state.updatedAt = time.Now().UTC()
}

func (state *appendSequenceState) retire() {
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.retired {
		return
	}
	state.retired = true
	state.processing = false
	if state.ready != nil {
		close(state.ready)
		state.ready = make(chan struct{})
	}
}

func (state *appendSequenceState) expired(cutoff time.Time) bool {
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.processing {
		return false
	}
	return !state.updatedAt.IsZero() && state.updatedAt.Before(cutoff)
}

func (state *appendSequenceState) currentNext() int64 {
	if state == nil {
		return 1
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.next <= 0 {
		return 1
	}
	return state.next
}

func (state *appendSequenceState) isRetired() bool {
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.retired
}

func (ticket appendSequenceTicket) Release() {
	if ticket.state == nil || ticket.seq <= 0 {
		return
	}
	ticket.state.Release(ticket.seq)
}

func (ticket appendSequenceTicket) CommitEpoch() bool {
	if ticket.disposition != "epoch_candidate" {
		return false
	}
	return ticket.request.commit(ticket.state)
}

func (ticket appendSequenceTicket) DiscardEpochCandidate() {
	if ticket.disposition != "epoch_candidate" {
		return
	}
	ticket.request.discard(ticket.state)
}

func (ticket appendSequenceTicket) Snapshot() (uint64, int64, string) {
	if ticket.state == nil {
		return 0, 0, ticket.disposition
	}
	ticket.state.mu.Lock()
	defer ticket.state.mu.Unlock()
	return ticket.state.epoch, ticket.state.next, ticket.disposition
}
