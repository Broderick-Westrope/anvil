package model

import (
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// ReloadRequest asks the caller of the TUI to replace the process with Exe
// once the program has exited.
type ReloadRequest struct {
	Exe         string
	SessionID   string
	HandoffPath string
	Yolo        config.YoloLevel
}

// reloadHandoff is state restored from the process that reloaded into this
// one.
type reloadHandoff struct {
	draft  string
	notice string
	ack    func()
}

// ReloadRequest returns the reload the user confirmed, or nil.
func (m *UI) ReloadRequest() *ReloadRequest {
	return m.reloadRequest
}

// SetReloadHandoff restores draft into the editor when the UI starts,
// reports notice, then calls ack once.
func (m *UI) SetReloadHandoff(draft, notice string, ack func()) {
	m.reloadHandoff = &reloadHandoff{draft: draft, notice: notice, ack: ack}
}

// applyReloadHandoff restores a pending handoff. ack runs in a command,
// after the draft is back in the editor, because it removes a file.
func (m *UI) applyReloadHandoff() tea.Cmd {
	h := m.reloadHandoff
	if h == nil {
		return nil
	}
	m.reloadHandoff = nil
	if h.draft != "" {
		m.textarea.SetValue(h.draft)
		m.textarea.MoveToEnd()
	}
	cmds := []tea.Cmd{util.ReportInfo(h.notice)}
	if h.ack != nil {
		cmds = append(cmds, func() tea.Msg {
			h.ack()
			return nil
		})
	}
	return tea.Batch(cmds...)
}
