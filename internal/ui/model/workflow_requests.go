package model

import "github.com/Broderick-Westrope/anvil/internal/app/workflowhost"

// workflowRequests tracks the approval and ask requests of each session's
// current invocation: a workflow command being approved, then the run it
// became. Requests are kept per invoking session, not per run, because an
// approval comes before its run exists.
//
// A request is open while its dialog waits for an answer, then answered.
// The host republishes a request until it's answered, so a copy of either
// is ignored. Both are forgotten when the invocation finishes or another
// invocation in the session replaces it.
type workflowRequests struct {
	bySession map[string]*invocationRequests
}

// invocationRequests are the requests of one invocation.
type invocationRequests struct {
	// runID is "" until the invocation's run starts.
	runID    string
	open     map[string]workflowhost.Event
	answered map[string]bool
}

func newInvocationRequests(runID string) *invocationRequests {
	return &invocationRequests{runID: runID, open: map[string]workflowhost.Event{}, answered: map[string]bool{}}
}

// seen reports whether ev's request is open or was answered.
func (r *workflowRequests) seen(ev workflowhost.Event) bool {
	inv := r.bySession[ev.SessionID]
	if inv == nil {
		return false
	}
	_, open := inv.open[ev.RequestID]
	return open || inv.answered[ev.RequestID]
}

// observeRun notes that an event of runID arrived in sessionID. The
// session's provisional invocation becomes that run; an invocation that
// already has another run was replaced, its finish lost.
func (r *workflowRequests) observeRun(sessionID, runID string) {
	inv := r.bySession[sessionID]
	switch {
	case inv == nil || runID == "":
	case inv.runID == "":
		inv.runID = runID
	case inv.runID != runID:
		delete(r.bySession, sessionID)
	}
}

// add opens ev, a request not seen before. An approval starts a new
// invocation, replacing any before it in the session.
func (r *workflowRequests) add(ev workflowhost.Event) {
	inv := r.bySession[ev.SessionID]
	if inv == nil || ev.Kind == workflowhost.EventApproval {
		if r.bySession == nil {
			r.bySession = map[string]*invocationRequests{}
		}
		inv = newInvocationRequests(ev.RunID)
		r.bySession[ev.SessionID] = inv
	}
	inv.open[ev.RequestID] = ev
}

// take closes the open request id and returns it.
func (r *workflowRequests) take(id string) (workflowhost.Event, bool) {
	for _, inv := range r.bySession {
		if ev, ok := inv.open[id]; ok {
			delete(inv.open, id)
			return ev, true
		}
	}
	return workflowhost.Event{}, false
}

// answer marks ev, a request taken, as answered.
func (r *workflowRequests) answer(ev workflowhost.Event) {
	if inv := r.bySession[ev.SessionID]; inv != nil {
		inv.answered[ev.RequestID] = true
	}
}

// finish forgets sessionID's invocation and returns the IDs of its
// requests still open.
func (r *workflowRequests) finish(sessionID string) []string {
	inv := r.bySession[sessionID]
	if inv == nil {
		return nil
	}
	delete(r.bySession, sessionID)
	ids := make([]string, 0, len(inv.open))
	for id := range inv.open {
		ids = append(ids, id)
	}
	return ids
}
