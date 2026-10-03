package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// jobWaker starts a turn for an idle session when its background jobs
// produce events. It runs a single goroutine fed by the job event
// store's Pending channel and OnIdle/composer triggers, so producers
// never block on it.
type jobWaker struct {
	store    *jobevents.Store
	sessions sessionGetter
	enabled  atomic.Bool
	closed   atomic.Bool
	// signal wakes the loop to drain triggered; capacity 1, non-blocking
	// sends, so triggers are coalesced but never lost.
	signal chan struct{}

	mu        sync.Mutex
	agent     wakeAgent
	composer  map[string]composerState // Present only for sessions open in the TUI.
	triggered map[string]bool          // Sessions to re-check; "" means all open sessions.
	inflight  map[string]bool          // Sessions with a RunWake call in progress.
	recheck   map[string]bool          // Triggered while in flight; re-checked when it returns.
	wg        sync.WaitGroup
}

type wakeAgent interface {
	RunWake(ctx context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error)
}

type sessionGetter interface {
	Get(ctx context.Context, id string) (session.Session, error)
}

type composerState struct {
	hasDraft   bool
	navigating bool
}

func (s composerState) idle() bool {
	return !s.hasDraft && !s.navigating
}

func newJobWaker(store *jobevents.Store, sessions sessionGetter) *jobWaker {
	return &jobWaker{
		store:     store,
		sessions:  sessions,
		signal:    make(chan struct{}, 1),
		composer:  make(map[string]composerState),
		triggered: make(map[string]bool),
		inflight:  make(map[string]bool),
		recheck:   make(map[string]bool),
	}
}

// setAgent sets the agent that runs wakes. Until it is set, no session
// is woken.
func (w *jobWaker) setAgent(a wakeAgent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agent = a
}

// SetComposerState records whether the session is open in the TUI and
// whether its composer has a draft or is navigating the branch tree.
func (w *jobWaker) SetComposerState(sessionID string, open, hasDraft, navigating bool) {
	if sessionID == "" {
		return
	}
	w.mu.Lock()
	prev, wasOpen := w.composer[sessionID]
	next := composerState{hasDraft: hasDraft, navigating: navigating}
	if open {
		w.composer[sessionID] = next
	} else {
		delete(w.composer, sessionID)
	}
	w.mu.Unlock()

	if open && next.idle() && (!wasOpen || !prev.idle()) {
		w.trigger(sessionID)
	}
}

// trigger asks the loop to re-check sessionID ("" for every open
// session). It never blocks.
func (w *jobWaker) trigger(sessionID string) {
	w.mu.Lock()
	w.triggered[sessionID] = true
	w.mu.Unlock()
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// close stops all future wakes. In-flight wake runs are canceled by the
// agent's CancelAll.
func (w *jobWaker) close() {
	w.closed.Store(true)
}

// wait blocks until in-flight RunWake calls return or ctx is done, and
// reports whether they all returned.
func (w *jobWaker) wait(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// run is the single loop that decides which sessions to wake.
func (w *jobWaker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.store.Pending():
			w.checkAll(ctx)
		case <-w.signal:
			w.mu.Lock()
			ids := w.triggered
			w.triggered = make(map[string]bool)
			w.mu.Unlock()
			if ids[""] {
				w.checkAll(ctx)
				continue
			}
			for id := range ids {
				w.check(ctx, id)
			}
		}
	}
}

func (w *jobWaker) checkAll(ctx context.Context) {
	w.mu.Lock()
	ids := make([]string, 0, len(w.composer))
	for id := range w.composer {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	for _, id := range ids {
		w.check(ctx, id)
	}
}

// eligible reports whether the session may be woken right now, from the
// TUI's point of view.
func (w *jobWaker) eligible(sessionID string) bool {
	if !w.enabled.Load() || w.closed.Load() {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	state, open := w.composer[sessionID]
	return open && state.idle()
}

func (w *jobWaker) check(ctx context.Context, sessionID string) {
	if !w.eligible(sessionID) {
		return
	}
	w.mu.Lock()
	wakeAgent := w.agent
	if wakeAgent == nil {
		w.mu.Unlock()
		return
	}
	if w.inflight[sessionID] {
		w.recheck[sessionID] = true
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	sess, err := w.sessions.Get(ctx, sessionID)
	if err != nil {
		slog.Debug("Failed to load session for job wake", "session_id", sessionID, "error", err)
		return
	}
	if sess.ParentSessionID != "" || !w.store.HasPending(sessionID) {
		return
	}

	w.mu.Lock()
	if w.inflight[sessionID] {
		w.recheck[sessionID] = true
		w.mu.Unlock()
		return
	}
	w.inflight[sessionID] = true
	w.wg.Add(1)
	w.mu.Unlock()

	go func() {
		defer w.wg.Done()
		_, err := wakeAgent.RunWake(ctx, sessionID, func() bool { return w.eligible(sessionID) })
		if err != nil && !errors.Is(err, agent.ErrSessionBusy) &&
			!errors.Is(err, agent.ErrWakeNotAllowed) && !errors.Is(err, context.Canceled) {
			slog.Error("Failed to wake session for background job events", "session_id", sessionID, "error", err)
		}

		w.mu.Lock()
		delete(w.inflight, sessionID)
		again := w.recheck[sessionID]
		delete(w.recheck, sessionID)
		w.mu.Unlock()
		if again {
			w.trigger(sessionID)
		}
	}()
}
