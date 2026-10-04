package dialog

import (
	"fmt"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ReloadConfirmID is the identifier for the reload confirmation dialog.
const ReloadConfirmID = "reload_confirm"

const (
	// reloadConfirmMaxJobs is how many running jobs are listed by name.
	reloadConfirmMaxJobs = 3
	// reloadConfirmJobWidth bounds each listed job's label.
	reloadConfirmJobWidth = 48
)

// ReloadConfirm asks before /reload-instance loses work: running background
// jobs are stopped and attachments are dropped.
type ReloadConfirm struct {
	com         *common.Common
	version     string
	jobs        []string
	attachments bool
	selectedNo  bool
	keyMap      struct {
		LeftRight,
		EnterSpace,
		Yes,
		No,
		Tab,
		Close key.Binding
	}
}

var _ Dialog = (*ReloadConfirm)(nil)

// NewReloadConfirm creates the confirmation for reloading onto version.
// jobs labels the running background jobs; attachments reports whether
// the editor has any.
func NewReloadConfirm(com *common.Common, version string, jobs []string, attachments bool) *ReloadConfirm {
	r := &ReloadConfirm{
		com:         com,
		version:     version,
		jobs:        jobs,
		attachments: attachments,
		// Default to Cancel: confirming stops background jobs and drops
		// attachments, so a stray enter must not do that.
		selectedNo: true,
	}
	r.keyMap.LeftRight = key.NewBinding(
		key.WithKeys("left", "right"),
		key.WithHelp("←/→", "switch options"),
	)
	r.keyMap.EnterSpace = key.NewBinding(
		key.WithKeys("enter", " "),
		key.WithHelp("enter/space", "confirm"),
	)
	r.keyMap.Yes = key.NewBinding(
		key.WithKeys("y", "Y"),
		key.WithHelp("y/Y", "reload"),
	)
	r.keyMap.No = key.NewBinding(
		key.WithKeys("n", "N"),
		key.WithHelp("n/N", "cancel"),
	)
	r.keyMap.Tab = key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch options"),
	)
	r.keyMap.Close = CloseKey
	return r
}

// ID implements [Dialog].
func (*ReloadConfirm) ID() string {
	return ReloadConfirmID
}

// HandleMsg implements [Dialog].
func (r *ReloadConfirm) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch {
	case key.Matches(keyMsg, r.keyMap.LeftRight, r.keyMap.Tab):
		r.selectedNo = !r.selectedNo
	case key.Matches(keyMsg, r.keyMap.EnterSpace):
		if r.selectedNo {
			return ActionReloadInstanceCancel{}
		}
		return ActionReloadInstanceConfirm{}
	case key.Matches(keyMsg, r.keyMap.Yes):
		return ActionReloadInstanceConfirm{}
	case key.Matches(keyMsg, r.keyMap.No, r.keyMap.Close):
		return ActionReloadInstanceCancel{}
	}
	return nil
}

// lines returns the dialog's text, one entry per line.
func (r *ReloadConfirm) lines() []string {
	lines := []string{fmt.Sprintf("Reload onto anvil %s?", r.version)}
	if len(r.jobs) > 0 {
		lines = append(lines, "", "These background jobs will be stopped:")
		for i, job := range r.jobs {
			if i == reloadConfirmMaxJobs {
				lines = append(lines, fmt.Sprintf("+%d more", len(r.jobs)-reloadConfirmMaxJobs))
				break
			}
			lines = append(lines, "• "+ansi.Truncate(job, reloadConfirmJobWidth, "…"))
		}
	}
	if r.attachments {
		lines = append(lines, "", "Attachments will be dropped.")
	}
	return lines
}

// Draw implements [Dialog].
func (r *ReloadConfirm) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	baseStyle := r.com.Styles.Dialog.Quit.Content
	buttons := common.ButtonGroup(r.com.Styles, []common.ButtonOpts{
		{Text: "Reload", Selected: !r.selectedNo, Padding: 3},
		{Text: "Cancel", Selected: r.selectedNo, Padding: 3},
	}, " ")
	body := lipgloss.JoinVertical(lipgloss.Left, r.lines()...)
	content := baseStyle.Render(lipgloss.JoinVertical(lipgloss.Center, body, "", buttons))

	frameStyle := r.com.Styles.Dialog.Quit.Frame
	if area.Dx()-frameStyle.GetHorizontalBorderSize() < lipgloss.Width(content) {
		frameStyle = frameStyle.Padding(1, 0)
	}
	DrawCenter(scr, area, frameStyle.Render(content))
	return nil
}

// ShortHelp implements [help.KeyMap].
func (r *ReloadConfirm) ShortHelp() []key.Binding {
	return []key.Binding{r.keyMap.LeftRight, r.keyMap.EnterSpace}
}

// FullHelp implements [help.KeyMap].
func (r *ReloadConfirm) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{r.keyMap.LeftRight, r.keyMap.EnterSpace, r.keyMap.Yes, r.keyMap.No},
		{r.keyMap.Tab, r.keyMap.Close},
	}
}
