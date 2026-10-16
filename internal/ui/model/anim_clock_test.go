package model

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// newClockUI returns a chat UI showing one spinning assistant message, with
// a controllable clock.
func newClockUI(t *testing.T) (*UI, *time.Time) {
	t.Helper()
	s := newSpinScene(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	s.u.now = func() time.Time { return now }
	s.u.Update(created(sceneSession, message.Message{ID: "a-think", Role: message.Assistant}))
	return s.u, &now
}

// spinnerVersion is the assistant item's list-cache version, which moves
// once per advanced frame.
func spinnerVersion(u *UI) uint64 {
	return u.chat.MessageItem("a-think").(interface{ Version() uint64 }).Version()
}

func TestAnimClock_OneTickOutstanding(t *testing.T) {
	t.Parallel()
	u, _ := newClockUI(t)
	require.True(t, u.animClock.running, "a visible spinner arms the clock")
	gen := u.animClock.gen

	// Further updates must not arm a second tick while one is outstanding.
	for range 5 {
		u.Update(nil)
	}
	require.Equal(t, gen, u.animClock.gen)
}

func TestAnimClock_StaleTickIgnored(t *testing.T) {
	t.Parallel()
	u, _ := newClockUI(t)
	stale := animTickMsg{gen: u.animClock.gen}
	u.animClock.gen++ // A newer clock supersedes the armed one.

	before := spinnerVersion(u)
	require.Nil(t, u.handleAnimTick(stale), "a stale tick must not re-arm")
	require.Equal(t, before, spinnerVersion(u),
		"a stale tick must not advance anything")
}

func TestAnimClock_TickAdvancesAndRearms(t *testing.T) {
	t.Parallel()
	u, _ := newClockUI(t)
	before := spinnerVersion(u)

	cmd := u.handleAnimTick(animTickMsg{gen: u.animClock.gen})
	require.NotNil(t, cmd, "a spinner still on screen keeps the clock going")
	require.True(t, u.animClock.running)
	require.Greater(t, spinnerVersion(u), before)
}

func TestAnimClock_StopsOutsideChat(t *testing.T) {
	t.Parallel()
	u, _ := newClockUI(t)
	u.state = uiLanding
	require.Nil(t, u.handleAnimTick(animTickMsg{gen: u.animClock.gen}))
	require.False(t, u.animClock.running)
	require.Nil(t, u.ensureAnimating())
}

func TestAnimClock_RecoversLostTick(t *testing.T) {
	t.Parallel()
	u, now := newClockUI(t)
	gen := u.animClock.gen

	*now = now.Add(animClockLostAfter / 2)
	require.Nil(t, u.ensureAnimating(), "a recent tick is not lost")

	*now = now.Add(animClockLostAfter)
	require.NotNil(t, u.ensureAnimating(), "a tick overdue past the limit is presumed lost")
	require.Greater(t, u.animClock.gen, gen, "the replacement clock retires the old tick")
}

func TestChat_FrozenSpinners(t *testing.T) {
	t.Parallel()
	u, _ := newClockUI(t)
	require.True(t, u.chat.HasVisibleAnimation())

	u.chat.FreezeSpinners()
	require.False(t, u.chat.HasVisibleAnimation())
	require.False(t, u.chat.AdvanceVisible())

	u.chat.UnfreezeSpinners("a-think")
	require.True(t, u.chat.HasVisibleAnimation())

	// Replacing the chat's messages forgets old freezes.
	u.chat.FreezeSpinners()
	u.chat.SetMessages()
	require.Empty(t, u.chat.frozenSpinners)
}

// TestAnimation_GhostSpinnerResumesOnActivity pins the other half of the
// reload guard: once its message is live again, a frozen spinner moves.
func TestAnimation_GhostSpinnerResumesOnActivity(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	s.ws.busy.Store(false)
	s.u.setSessionMessages([]message.Message{
		{ID: "a-think", SessionID: sceneSession, Role: message.Assistant},
	})
	s.r.send(nil)
	require.False(t, s.u.animClock.running)

	seen := s.track(func() *Chat { return s.u.chat })
	s.ws.busy.Store(true)
	s.r.send(updated(sceneSession, message.Message{ID: "a-think", Role: message.Assistant}))
	s.r.run(600 * time.Millisecond)
	require.GreaterOrEqual(t, len(seen["a-think"]), 3)
}

func TestAnimClock_TickOverdueByExactlyTheLimitIsLost(t *testing.T) {
	t.Parallel()
	u, now := newClockUI(t)

	*now = now.Add(animClockLostAfter)
	require.NotNil(t, u.ensureAnimating())
}
