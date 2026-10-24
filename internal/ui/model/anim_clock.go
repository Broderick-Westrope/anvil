package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
)

// animTickMsg is one frame of the shared animation clock. gen identifies the
// clock that armed it, so a tick from a superseded clock is ignored instead
// of running alongside its replacement.
type animTickMsg struct{ gen uint64 }

// animClockLostAfter is how long an armed clock may go without its tick
// arriving before ensureAnimating assumes the tick was lost and arms a new
// one. Generous enough that a slow frame never trips it.
const animClockLostAfter = 2 * time.Second

// animClock drives every spinner in the UI from one timer, so a frame costs
// one Update and View pass however many spinners are on screen. Only the
// active chat's visible spinners advance: hidden chats and off-screen items
// cost nothing and resume from where they stopped.
type animClock struct {
	// running is true while a tick is outstanding.
	running bool
	gen     uint64
	armedAt time.Time
}

func (m *UI) clockNow() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// ensureAnimating arms the clock when the active chat shows a spinner and no
// tick is outstanding. Update calls it after every message, so anything that
// puts a spinner on screen (a new message, a tool update, a scroll, a session
// load, drilling in or out) starts the clock without per-call-site wiring.
func (m *UI) ensureAnimating() tea.Cmd {
	if m.state != uiChat {
		return nil
	}
	if m.animClock.running && m.clockNow().Sub(m.animClock.armedAt) < animClockLostAfter {
		return nil
	}
	if !m.activeChat().HasVisibleAnimation() {
		m.animClock.running = false
		return nil
	}
	return m.armAnimClock()
}

func (m *UI) armAnimClock() tea.Cmd {
	m.animClock.running = true
	m.animClock.armedAt = m.clockNow()
	m.animClock.gen++
	gen := m.animClock.gen
	return tea.Tick(anim.FrameInterval(), func(time.Time) tea.Msg {
		return animTickMsg{gen: gen}
	})
}

// handleAnimTick advances every visible spinner in the active chat by one
// frame and arms the next tick while any is still spinning. The clock stops
// itself once none is.
func (m *UI) handleAnimTick(msg animTickMsg) tea.Cmd {
	if msg.gen != m.animClock.gen {
		return nil
	}
	m.animClock.running = false
	if m.state != uiChat {
		return nil
	}
	chat := m.activeChat()
	if !chat.AdvanceVisible() {
		return nil
	}
	// Spinners can change an item's height, so keep a following view
	// pinned to the bottom.
	if chat.Follow() {
		chat.ScrollToBottom()
	}
	return m.armAnimClock()
}
