package model

import (
	"strings"
	"time"
	"unicode"

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
	title := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, sessionTitle)), " ")
	if title == "" {
		return "anvil " + home.Short(workingDir)
	}
	return title + " · anvil"
}
