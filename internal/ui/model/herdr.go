package model

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/herdr"
	"github.com/Broderick-Westrope/anvil/internal/home"
)

// herdrPollInterval bounds how stale reported state can be when it
// changes without a UI message, e.g. a background run ending on an
// error path.
const herdrPollInterval = 250 * time.Millisecond

type herdrTickMsg struct{}

func herdrTick() tea.Cmd {
	return tea.Tick(herdrPollInterval, func(time.Time) tea.Msg { return herdrTickMsg{} })
}

// herdrTickCmd arms the next poll tick unless one is pending or the last
// reported status is idle. From idle, every way a run or prompt starts
// produces a UI message, while a busy run can end with no message (error
// path), so polling is only needed while not idle.
func (m *UI) herdrTickCmd() tea.Cmd {
	if m.herdrHandler == nil || m.herdrTickPending || m.herdrState.Status == herdr.StatusIdle {
		return nil
	}
	m.herdrTickPending = true
	return herdrTick()
}

// SetHerdrHandler registers the Herdr reporter. It must be called
// before the program starts.
func (m *UI) SetHerdrHandler(handler func(herdr.State)) { m.herdrHandler = handler }

func (m *UI) herdrSnapshot() herdr.State {
	var s herdr.State
	if m.hasSession() {
		s.SessionID = m.session.ID
		s.SessionTitle = headerSessionTitle(m.session)
	}
	if req, ok := m.com.Workspace.PermissionPending(); ok {
		s.Status = herdr.StatusBlocked
		s.Message = "Permission required: " + req.ToolName
		return s
	}
	if m.isAgentBusy() {
		s.Status = herdr.StatusWorking
	} else {
		s.Status = herdr.StatusIdle
	}
	return s
}

func (m *UI) trackHerdrState() {
	if m.herdrHandler == nil {
		return
	}
	s := m.herdrSnapshot()
	if m.herdrSent && s == m.herdrState {
		return
	}
	m.herdrState, m.herdrSent = s, true
	m.herdrHandler(s)
}

// windowTitle leads with the session title so terminal multiplexers
// listing panes by title can tell sessions apart. sessionTitle must already
// have placeholder names removed.
func windowTitle(sessionTitle, workingDir string) string {
	title := herdr.CleanTitle(sessionTitle)
	if title == "" {
		return "anvil " + home.Short(workingDir)
	}
	return title + " · anvil"
}
