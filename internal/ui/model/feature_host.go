package model

import (
	"image/color"
	"slices"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/notification"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// featureHost is what a feature may ask of UI. A method is added only when
// a second feature needs it, or when it is a UI-owned operation the
// existing methods can't express, and it documents its side effects. It
// never hands out *UI or its fields.
type featureHost interface {
	// SessionID is the root session's ID (UI's m.session), or "" when
	// there is none. It is not the drilled-in child on screen.
	SessionID() string
	// EnsureSession returns the current session, creating it and
	// switching to chat when there's none (assigns the session and
	// honours forced compact mode), and the command that loads a new
	// session. The ID is "" when no session could be had.
	EnsureSession() (string, tea.Cmd, error)
	// Navigate does nothing when sessionID is already the root session.
	// Otherwise it clears any drill-in synchronously and returns the
	// command that loads sessionID.
	Navigate(sessionID string) tea.Cmd
	// DrillInto clears any drill-in synchronously and returns a command
	// that delivers util.AgentDrillInMsg for sessionID, labelled label,
	// asynchronously.
	DrillInto(sessionID, label string) tea.Cmd
	// Dialogs is the dialog overlay.
	Dialogs() *dialog.Overlay
	// Notify sends a desktop notification under UI's notification policy.
	Notify(n notification.Notification) tea.Cmd
	// Relayout recomputes layout after the feature changed its panel.
	Relayout()
	// ComposerHasContent reports unsent editor text or attachments.
	ComposerHasContent() bool
	// Reloading reports an instance reload in progress.
	Reloading() bool
	// KeyMap is a copy of the current key bindings.
	KeyMap() KeyMap
	// InvalidateDiagrams re-renders every diagram item, in the root chat
	// and every drill-in, so an image that just arrived replaces its
	// placeholder. Each chat keeps its scroll position (Chat.
	// InvalidateDiagrams).
	InvalidateDiagrams()
	// AppBackground is the background Anvil paints the terminal with, or
	// nil when the TUI is transparent and the terminal's own shows.
	AppBackground() color.Color
	// PushDrillIn opens c as a drill-in labelled label, scrolled to its
	// top, focuses it and blurs the editor, as the tool and thinking
	// drill-ins do.
	PushDrillIn(c *Chat, label string)
	// DrillInOpen reports whether c is still on the drill-in stack.
	DrillInOpen(c *Chat) bool
}

// uiHost is UI as a featureHost.
type uiHost struct{ m *UI }

func (h uiHost) SessionID() string {
	if h.m.session == nil {
		return ""
	}
	return h.m.session.ID
}

func (h uiHost) EnsureSession() (string, tea.Cmd, error) {
	cmd, err := h.m.ensureSession()
	if err != nil {
		return "", nil, err
	}
	if !h.m.hasSession() {
		return "", cmd, nil
	}
	return h.m.session.ID, cmd, nil
}

func (h uiHost) Navigate(sessionID string) tea.Cmd {
	if h.m.session != nil && h.m.session.ID == sessionID {
		return nil
	}
	h.m.clearDrillStack()
	return h.m.loadSession(sessionID)
}

func (h uiHost) DrillInto(sessionID, label string) tea.Cmd {
	h.m.clearDrillStack()
	return util.CmdHandler(util.AgentDrillInMsg{SessionID: sessionID, Label: label})
}

func (h uiHost) Dialogs() *dialog.Overlay { return h.m.dialog }

func (h uiHost) Notify(n notification.Notification) tea.Cmd { return h.m.sendNotification(n) }

func (h uiHost) Relayout() { h.m.updateLayoutAndSize() }

func (h uiHost) ComposerHasContent() bool {
	return h.m.textarea.Value() != "" || h.m.attachments.HasContent()
}

func (h uiHost) Reloading() bool { return h.m.reloading }

func (h uiHost) KeyMap() KeyMap { return h.m.keyMap }

func (h uiHost) InvalidateDiagrams() {
	if h.m.chat != nil {
		h.m.chat.InvalidateDiagrams()
	}
	for _, entry := range h.m.drillStack {
		if entry.chat != nil {
			entry.chat.InvalidateDiagrams()
		}
	}
}

func (h uiHost) AppBackground() color.Color {
	if h.m.isTransparent {
		return nil
	}
	return h.m.com.Styles.Background
}

func (h uiHost) PushDrillIn(c *Chat, label string) {
	c.SetFollow(false)
	c.Focus()
	h.m.drillStack = append(h.m.drillStack, drillInEntry{chat: c, label: label})
	h.m.textarea.Blur()
	h.m.focus = uiFocusMain
	h.m.updateLayoutAndSize()
	// Sizing a new chat pins it to the bottom; start at the top.
	c.ScrollToTop()
}

func (h uiHost) DrillInOpen(c *Chat) bool {
	return slices.ContainsFunc(h.m.drillStack, func(e drillInEntry) bool { return e.chat == c })
}
