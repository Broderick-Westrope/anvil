package model

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
)

// feature is a fork feature that owns its state and plugs into UI's
// dispatchers through this surface instead of adding cases to them. UI
// decides when each method is called; features are not tea.Models. See
// "Feature Routing" in internal/ui/AGENTS.md.
type feature interface {
	// Observe sees every non-input message before update runs, and never
	// claims one. The commands it returns are batched with update's
	// result, so they survive update's early returns.
	Observe(msg tea.Msg) tea.Cmd
	// HandleMsg is offered messages no update case handled (update's
	// default arm), before they go to the dialog. handled=true claims
	// the message.
	HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool)
	// HandleKey is offered keys no global key binding matched.
	HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool)
	// HandleAction is offered each dialog action before the action switch.
	HandleAction(action dialog.Action) (cmd tea.Cmd, handled bool)
}

// commandFeature runs some custom commands itself, from the slash
// command line (with its raw arguments) or the palette (with none).
type commandFeature interface {
	RunCommand(cmd commands.CustomCommand, rawArgs string) (tea.Cmd, bool)
}

// panelFeature shows a panel above the pills.
type panelFeature interface {
	// Panel returns the feature's panel, or nil when it has none.
	Panel() panelView
}

// panelView is a panel's rendering, built from one view-model so its
// height and its rendering can't disagree.
type panelView interface {
	// Lines is the number of rows, whatever the width.
	Lines() int
	// Render returns exactly Lines() lines, each truncated to width.
	Render(width int) string
}

// paletteFeature contributes entries to the command palette.
type paletteFeature interface {
	PaletteEntries() []*dialog.CommandItem
}

// features is UI's ordered registry of features. Order is precedence:
// the first feature to claim a message, key or action wins.
type features struct {
	all      []feature
	commands []commandFeature
	panels   []panelFeature
	palettes []paletteFeature
}

// register adds f, and the optional surfaces it implements, after the
// features already registered.
func (fs *features) register(f feature) {
	fs.all = append(fs.all, f)
	if c, ok := f.(commandFeature); ok {
		fs.commands = append(fs.commands, c)
	}
	if p, ok := f.(panelFeature); ok {
		fs.panels = append(fs.panels, p)
	}
	if p, ok := f.(paletteFeature); ok {
		fs.palettes = append(fs.palettes, p)
	}
}

// isInput reports whether msg is keyboard, paste or mouse input, which
// features only ever get through HandleKey.
func isInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyMsg, tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg,
		tea.MouseMsg, common.CoalescedWheelMsg:
		return true
	}
	return false
}

func (fs *features) observe(msg tea.Msg) tea.Cmd {
	if isInput(msg) {
		return nil
	}
	var cmds []tea.Cmd
	for _, f := range fs.all {
		if cmd := f.Observe(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (fs *features) handleMsg(msg tea.Msg) (tea.Cmd, bool) {
	if isInput(msg) {
		return nil, false
	}
	for _, f := range fs.all {
		if cmd, ok := f.HandleMsg(msg); ok {
			return cmd, true
		}
	}
	return nil, false
}

func (fs *features) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	for _, f := range fs.all {
		if cmd, ok := f.HandleKey(msg); ok {
			return cmd, true
		}
	}
	return nil, false
}

// handleAction offers action to the features. Running a custom command
// from the palette goes to the command features, with no arguments, and
// closes the palette when one runs it.
func (fs *features) handleAction(overlay *dialog.Overlay, action dialog.Action) (tea.Cmd, bool) {
	if run, ok := action.(dialog.ActionRunCustomCommand); ok {
		cmd, handled := fs.runCommand(run.Command, "")
		if handled {
			overlay.CloseDialog(dialog.CommandsID)
		}
		return cmd, handled
	}
	for _, f := range fs.all {
		if cmd, ok := f.HandleAction(action); ok {
			return cmd, true
		}
	}
	return nil, false
}

// runCommand offers cmd to the command features. A claimed command always
// comes back with a non-nil tea.Cmd, because the slash path takes nil to
// mean "not a command" and would send the input as a prompt.
func (fs *features) runCommand(cmd commands.CustomCommand, rawArgs string) (tea.Cmd, bool) {
	for _, f := range fs.commands {
		if c, ok := f.RunCommand(cmd, rawArgs); ok {
			if c == nil {
				c = func() tea.Msg { return nil }
			}
			return c, true
		}
	}
	return nil, false
}

// panelLines is the number of rows the panels take.
func (fs *features) panelLines() int {
	var n int
	for _, f := range fs.panels {
		if p := f.Panel(); p != nil {
			n += p.Lines()
		}
	}
	return n
}

// renderPanels renders the panels within width, in registration order,
// as panelLines() lines.
func (fs *features) renderPanels(width int) string {
	var panels []string
	for _, f := range fs.panels {
		if p := f.Panel(); p != nil && p.Lines() > 0 {
			panels = append(panels, p.Render(width))
		}
	}
	return strings.Join(panels, "\n")
}

func (fs *features) paletteEntries() []*dialog.CommandItem {
	var items []*dialog.CommandItem
	for _, f := range fs.palettes {
		items = append(items, f.PaletteEntries()...)
	}
	return items
}
