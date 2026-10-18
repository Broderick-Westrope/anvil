package agent

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

var (
	// errSubagentRunning rejects a resume of a subagent whose child session
	// is already leased by another task call.
	errSubagentRunning = errors.New("subagent is running")
	// errSteerRejected rejects a steer that arrives after the call started
	// building its final result, or when no call is running.
	errSteerRejected = errors.New("subagent is no longer accepting messages")
	// errSubagentInterrupted is the cause of an attempt cancelled by
	// interrupt+steer; the call continues with the human's message.
	errSubagentInterrupted = errors.New("interrupted by user")
	// errCancelledByHuman is the cause of a call cancelled from the
	// drilled-in subagent view.
	errCancelledByHuman = errors.New("cancelled by human")
)

// subagentLeases holds the lease of every child session a task call is
// running in, so no two calls ever run in one child at once.
type subagentLeases struct {
	mu      sync.Mutex
	byChild map[string]*subagentLease
}

// acquire leases childID to callID, or fails with errSubagentRunning when
// another call holds it. cancelCall ends the whole call.
func (l *subagentLeases) acquire(childID, callID string, cancelCall context.CancelCauseFunc) (*subagentLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byChild[childID] != nil {
		return nil, errSubagentRunning
	}
	if l.byChild == nil {
		l.byChild = make(map[string]*subagentLease)
	}
	lease := &subagentLease{childID: childID, callID: callID, cancelCall: cancelCall, wake: make(chan struct{}, 1)}
	l.byChild[childID] = lease
	return lease, nil
}

func (l *subagentLeases) release(lease *subagentLease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byChild[lease.childID] == lease {
		delete(l.byChild, lease.childID)
	}
}

// get returns the lease on childID, or nil when no call is running there.
func (l *subagentLeases) get(childID string) *subagentLease {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byChild[childID]
}

// subagentLease is one task call's hold on its child session. It carries
// the human's steers until they're delivered, and the cancel functions of
// the call and of its current attempt.
type subagentLease struct {
	childID, callID string
	cancelCall      context.CancelCauseFunc
	// wake is signalled by an interrupt, so a call waiting out a retry
	// backoff continues straight away.
	wake chan struct{}

	mu            sync.Mutex
	cancelAttempt context.CancelCauseFunc
	pending       []message.InjectionContent
	finalising    bool
}

// beginAttempt returns the context of a new attempt within the call. It
// clears any wake left by an interrupt of the previous attempt, so the
// next backoff isn't cut short by it.
func (l *subagentLease) beginAttempt(callCtx context.Context) context.Context {
	select {
	case <-l.wake:
	default:
	}
	ctx, cancel := context.WithCancelCause(callCtx)
	l.mu.Lock()
	l.cancelAttempt = cancel
	l.mu.Unlock()
	return ctx
}

// steer queues a human message for the subagent. With interrupt, it also
// cancels the current attempt so the call continues with the message. It
// fails with errSteerRejected once the call is finalising.
func (l *subagentLease) steer(text string, interrupt bool) error {
	kind := message.InjectionKindSteer
	if interrupt {
		kind = message.InjectionKindInterruptSteer
	}
	l.mu.Lock()
	if l.finalising {
		l.mu.Unlock()
		return errSteerRejected
	}
	l.pending = append(l.pending, message.InjectionContent{
		Source: message.InjectionSourceHuman, Kind: kind, Text: text, CallID: l.callID,
	})
	cancelAttempt := l.cancelAttempt
	l.mu.Unlock()
	if interrupt {
		if cancelAttempt != nil {
			cancelAttempt(errSubagentInterrupted)
		}
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// cancel ends the whole call, recording cause.
func (l *subagentLease) cancel(cause error) {
	l.cancelCall(cause)
}

// takePending claims every pending steer for delivery. A caller that
// fails to deliver them must restore them.
func (l *subagentLease) takePending() []message.InjectionContent {
	l.mu.Lock()
	defer l.mu.Unlock()
	taken := l.pending
	l.pending = nil
	return taken
}

// restore returns claimed steers that couldn't be delivered, ahead of any
// that arrived since.
func (l *subagentLease) restore(claimed []message.InjectionContent) {
	if len(claimed) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending = append(slices.Clone(claimed), l.pending...)
}

// finaliseOrTake is called when an attempt succeeds. It returns the steers
// still pending, which the call must deliver in another turn; when there
// are none it marks the call finalising, so later steers are rejected
// rather than lost.
func (l *subagentLease) finaliseOrTake() []message.InjectionContent {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.pending) > 0 {
		taken := l.pending
		l.pending = nil
		return taken
	}
	l.finalising = true
	return nil
}

// finalise marks the call finalising and returns the steers it never
// delivered, which go back to the human.
func (l *subagentLease) finalise() []message.InjectionContent {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finalising = true
	undelivered := l.pending
	l.pending = nil
	return undelivered
}
