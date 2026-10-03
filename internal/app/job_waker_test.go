package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/jobevents"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/shell"
	"github.com/stretchr/testify/require"
)

// fakeWakeAgent mimics the session agent's RunWake: it refuses when
// eligible is false and otherwise returns err (nil means it woke and
// delivered the session's pending events).
type fakeWakeAgent struct {
	store  *jobevents.Store // Wakes deliver the session's pending events.
	mu     sync.Mutex
	err    error
	before func(sessionID string) // Runs before eligible is checked.
	tries  []string
	wakes  []string
}

func (f *fakeWakeAgent) RunWake(_ context.Context, sessionID string, eligible func() bool) (*fantasy.AgentResult, error) {
	f.mu.Lock()
	before := f.before
	f.mu.Unlock()
	if before != nil {
		before(sessionID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tries = append(f.tries, sessionID)
	if !eligible() {
		return nil, agent.ErrWakeNotAllowed
	}
	if f.err != nil {
		return nil, f.err
	}
	f.wakes = append(f.wakes, sessionID)
	events, _ := f.store.Claim(sessionID, 100)
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	f.store.MarkDelivered(ids)
	return &fantasy.AgentResult{}, nil
}

func (f *fakeWakeAgent) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeWakeAgent) counts() (tries, wakes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tries), len(f.wakes)
}

type fakeSessions map[string]session.Session

func (f fakeSessions) Get(_ context.Context, id string) (session.Session, error) {
	s, ok := f[id]
	if !ok {
		return session.Session{}, errors.New("not found")
	}
	return s, nil
}

type wakerFixture struct {
	waker *jobWaker
	agent *fakeWakeAgent
	store *jobevents.Store
}

const (
	topSession   = "top"
	childSession = "child"
)

func newWakerFixture(t *testing.T) *wakerFixture {
	t.Helper()
	owners := map[string]string{"J-top": topSession, "J-child": childSession}
	store := jobevents.NewStore(func(id string) (string, bool) {
		s, ok := owners[id]
		return s, ok
	})
	sessions := fakeSessions{
		topSession:   {ID: topSession},
		childSession: {ID: childSession, ParentSessionID: topSession},
	}
	f := &wakerFixture{
		waker: newJobWaker(store, sessions),
		agent: &fakeWakeAgent{store: store},
		store: store,
	}
	f.waker.setAgent(f.agent)
	f.waker.enabled.Store(true)
	t.Cleanup(f.waker.wait)
	return f
}

func (f *wakerFixture) complete(jobID, sessionID string) {
	f.store.JobCompleted(shell.JobInfo{ID: jobID, SessionID: sessionID, Done: true}, "")
}

// checkNow runs one synchronous wake decision and waits for any wake it
// started, so the fake's counts are final.
func (f *wakerFixture) checkNow(t *testing.T, sessionID string) (tries, wakes int) {
	t.Helper()
	f.waker.check(t.Context(), sessionID)
	f.waker.wait()
	return f.agent.counts()
}

func (f *wakerFixture) startLoop(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.waker.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (f *wakerFixture) requireWakes(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, wakes := f.agent.counts()
		return wakes == n
	}, 5*time.Second, 5*time.Millisecond)
}

func TestJobWaker_IdleOpenSessionWakesOnEvent(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.startLoop(t)

	f.waker.SetComposerState(topSession, true, false, false)
	f.complete("J-top", topSession)
	f.requireWakes(t, 1)
}

func TestJobWaker_NotOpenNeverWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.complete("J-top", topSession)

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)

	f.waker.SetComposerState(topSession, true, false, false)
	f.waker.SetComposerState(topSession, false, false, false)
	tries, _ = f.checkNow(t, topSession)
	require.Zero(t, tries)
}

func TestJobWaker_BusyThenIdleWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, false, false)
	f.complete("J-top", topSession)

	f.agent.setErr(agent.ErrSessionBusy)
	tries, wakes := f.checkNow(t, topSession)
	require.Equal(t, 1, tries)
	require.Zero(t, wakes)

	// The busy run ends and reports idle through OnIdle.
	f.agent.setErr(nil)
	f.startLoop(t)
	f.waker.trigger(topSession)
	f.requireWakes(t, 1)
}

func TestJobWaker_DraftBlocksUntilCleared(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, true, false)
	f.complete("J-top", topSession)

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)

	f.startLoop(t)
	f.waker.SetComposerState(topSession, true, false, false)
	f.requireWakes(t, 1)
}

func TestJobWaker_NavigatingBlocks(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, false, true)
	f.complete("J-top", topSession)

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)
}

func TestJobWaker_EligibilityRecheckedAtWake(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, false, false)
	f.complete("J-top", topSession)
	// The user starts typing between the decision and the wake.
	f.agent.before = func(id string) { f.waker.SetComposerState(id, true, true, false) }

	tries, wakes := f.checkNow(t, topSession)
	require.Equal(t, 1, tries)
	require.Zero(t, wakes)
}

func TestJobWaker_ChildSessionNeverWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(childSession, true, false, false)
	f.complete("J-child", childSession)

	tries, _ := f.checkNow(t, childSession)
	require.Zero(t, tries)
}

func TestJobWaker_DisabledNeverWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.enabled.Store(false)
	f.waker.SetComposerState(topSession, true, false, false)
	f.complete("J-top", topSession)

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)
}

func TestJobWaker_ClosedNeverWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, false, false)
	f.complete("J-top", topSession)
	f.waker.close()

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)
}

func TestJobWaker_NothingPendingNeverWakes(t *testing.T) {
	t.Parallel()
	f := newWakerFixture(t)
	f.waker.SetComposerState(topSession, true, false, false)

	tries, _ := f.checkNow(t, topSession)
	require.Zero(t, tries)
}

func TestEnableJobWake_FollowsConfig(t *testing.T) {
	t.Parallel()

	newApp := func(t *testing.T) *App {
		t.Helper()
		store, err := config.Init(t.TempDir(), "", false)
		require.NoError(t, err)
		return &App{config: store, jobWaker: newJobWaker(jobevents.NewStore(nil), fakeSessions{})}
	}

	t.Run("default never wakes", func(t *testing.T) {
		t.Parallel()
		app := newApp(t)
		app.Config().Options.BackgroundJobs = nil
		app.EnableJobWake()
		require.False(t, app.jobWaker.enabled.Load())
	})
	t.Run("wake_on_event enables", func(t *testing.T) {
		t.Parallel()
		app := newApp(t)
		enabled := true
		app.Config().Options.BackgroundJobs = &config.BackgroundJobsOptions{WakeOnEvent: &enabled}
		app.EnableJobWake()
		require.True(t, app.jobWaker.enabled.Load())
	})
}
