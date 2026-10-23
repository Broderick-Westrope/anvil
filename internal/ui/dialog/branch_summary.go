package dialog

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// BranchSummaryPickerID is the identifier for the branch summary picker.
const BranchSummaryPickerID = "branch_summary_picker"

// RewindOffer is a rewind that could be summarized.
type RewindOffer struct {
	SessionID  string
	FromLeafID string
	TargetID   string
	// Agents are the summarizers to offer, in order.
	Agents []string
	// DefaultAgent is selected first; when it isn't offered, the first
	// agent is.
	DefaultAgent string
}

// ActionBranchSummaryPick is the user's choice for a rewind. An empty
// Agent means no summary.
type ActionBranchSummaryPick struct {
	SessionID  string
	FromLeafID string
	TargetID   string
	Agent      string
	Focus      string
}

// BranchSummaryPicker asks whether, and with which summarizer, to
// summarize the branch the user just rewound from. The list picks the
// summarizer; anything typed is focus text for it.
type BranchSummaryPicker struct {
	com      *common.Common
	offer    RewindOffer
	selected int // 0 is "No summary"; i is offer.Agents[i-1].
	input    textinput.Model
	help     help.Model

	keyMap struct {
		Prev, Next, Confirm, Close key.Binding
	}
}

var _ Dialog = (*BranchSummaryPicker)(nil)

// NewBranchSummaryPicker creates the picker for offer.
func NewBranchSummaryPicker(com *common.Common, offer RewindOffer) *BranchSummaryPicker {
	p := &BranchSummaryPicker{com: com, offer: offer, selected: 1}
	if i := slices.Index(offer.Agents, offer.DefaultAgent); i != -1 {
		p.selected = i + 1
	}

	p.input = textinput.New()
	p.input.SetVirtualCursor(false)
	p.input.Placeholder = "Optional focus, e.g. the PR and what's left…"
	p.input.CharLimit = branchsummary.MaxFocusChars
	p.input.SetStyles(com.Styles.TextInput)
	p.input.Focus()

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	p.help = h

	p.keyMap.Prev = key.NewBinding(key.WithKeys("up", "shift+tab"), key.WithHelp("↑", "previous"))
	p.keyMap.Next = key.NewBinding(key.WithKeys("down", "tab"), key.WithHelp("↓", "next"))
	p.keyMap.Confirm = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm"))
	p.keyMap.Close = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel branch"))
	return p
}

// Offer returns the rewind the picker is for.
func (p *BranchSummaryPicker) Offer() RewindOffer { return p.offer }

// ID implements [Dialog].
func (*BranchSummaryPicker) ID() string { return BranchSummaryPickerID }

// HandleMsg implements [Dialog].
func (p *BranchSummaryPicker) HandleMsg(msg tea.Msg) Action {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	options := len(p.offer.Agents) + 1
	switch {
	case key.Matches(keyMsg, p.keyMap.Close):
		// "No summary" covers skipping; esc undoes the branch itself.
		return ActionReturnToPreBranch{Pending: true}
	case key.Matches(keyMsg, p.keyMap.Prev):
		p.selected = (p.selected + options - 1) % options
	case key.Matches(keyMsg, p.keyMap.Next):
		p.selected = (p.selected + 1) % options
	case key.Matches(keyMsg, p.keyMap.Confirm):
		if p.selected == 0 {
			return p.pick("", "")
		}
		return p.pick(p.offer.Agents[p.selected-1], strings.TrimSpace(p.input.Value()))
	default:
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(keyMsg)
		return ActionCmd{cmd}
	}
	return nil
}

func (p *BranchSummaryPicker) pick(agent, focus string) ActionBranchSummaryPick {
	return ActionBranchSummaryPick{
		SessionID: p.offer.SessionID, FromLeafID: p.offer.FromLeafID, TargetID: p.offer.TargetID,
		Agent: agent, Focus: focus,
	}
}

// Draw implements [Dialog].
func (p *BranchSummaryPicker) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := p.com.Styles
	width := max(0, min(defaultDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	rc := NewRenderContext(t, width)
	rc.Title = "Summarize the branch you left?"
	labels := append([]string{"No summary"}, p.offer.Agents...)
	for i, label := range labels {
		style := t.Dialog.NormalItem
		if i == p.selected {
			style = t.Dialog.SelectedItem
		}
		rc.AddPart(style.Render(ansi.Truncate(label, innerWidth-style.GetHorizontalFrameSize(), "…")))
	}
	// InputCursor places the caret as if the input came straight after the
	// title, so move it down past the option rows drawn above it.
	rowsAbove := linesIn(rc.Parts)
	p.input.SetWidth(dialogInputTextWidth(t, p.input, innerWidth))
	rc.AddPart(t.Dialog.InputPrompt.Render(p.input.View()))
	cur := InputCursor(t, p.input.Cursor())
	if cur != nil {
		cur.Y += rowsAbove
	}
	rc.Help = renderDialogHelp(t, &p.help, p, innerWidth)

	DrawCenterCursor(scr, area, rc.Render(), cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (p *BranchSummaryPicker) ShortHelp() []key.Binding {
	return []key.Binding{p.keyMap.Prev, p.keyMap.Next, p.keyMap.Confirm, p.keyMap.Close}
}

// FullHelp implements [help.KeyMap].
func (p *BranchSummaryPicker) FullHelp() [][]key.Binding {
	return [][]key.Binding{p.ShortHelp()}
}
