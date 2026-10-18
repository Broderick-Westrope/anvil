package model

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
)

// subagentSteerFooterLines is the height of the footer under a drilled-in
// subagent's chat: the input (or a hint) and a help line. It's fixed, so
// the chat doesn't resize as the subagent starts and stops.
const subagentSteerFooterLines = 2

// subagentSteerWorkspace is what steering a subagent needs from the
// workspace.
type subagentSteerWorkspace interface {
	AgentSteerSubagent(childSessionID, text string, interrupt bool) error
	AgentCancelSubagent(childSessionID string) bool
	AgentSubagentRunning(childSessionID string) bool
	AgentSubagentPendingSteers(childSessionID string) []string
	AgentTakeReturnedSteers() []agent.SubagentSteersReturned
}

// subagentSteer lets a human drilled into a running subagent send it a
// message (steer), cancel it (Esc), or cut off its current attempt and
// continue with a message (interrupt+steer). It keeps a draft per
// subagent, and gives back steers a task call ended without reading.
type subagentSteer struct {
	host   featureHost
	com    *common.Common
	ws     subagentSteerWorkspace
	input  textarea.Model
	keys   subagentSteerKeys
	child  string
	drafts map[string]string
}

type subagentSteerKeys struct {
	Focus, Send, Interrupt, Cancel key.Binding
}

func newSubagentSteer(host featureHost, com *common.Common, ws subagentSteerWorkspace) *subagentSteer {
	input := textarea.New()
	input.SetStyles(com.Styles.Editor.Textarea)
	input.ShowLineNumbers = false
	input.CharLimit = -1
	input.Prompt = "> "
	input.Placeholder = "Message this subagent"
	input.SetHeight(1)
	km := host.KeyMap()
	return &subagentSteer{
		host:  host,
		com:   com,
		ws:    ws,
		input: input,
		keys: subagentSteerKeys{
			Focus:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "message")),
			Send:      km.Editor.SendMessage,
			Interrupt: km.SubagentInterrupt,
			Cancel:    km.Chat.Cancel,
		},
		drafts: make(map[string]string),
	}
}

// sync switches the input to the draft of the subagent on screen.
func (f *subagentSteer) sync() string {
	child := f.host.ViewedSubagentID()
	if child == f.child {
		return child
	}
	if f.child != "" {
		f.drafts[f.child] = f.input.Value()
	}
	f.child = child
	f.input.SetValue(f.drafts[child])
	delete(f.drafts, child)
	f.input.Blur()
	return child
}

// PreemptInput implements preemptInputFeature. While a subagent's chat is
// on screen it claims Esc (so Esc can't cancel the main run from here),
// and keys and pastes while the input has focus.
func (f *subagentSteer) PreemptInput(msg tea.Msg) (tea.Cmd, bool) {
	child := f.sync()
	if child == "" {
		return nil, false
	}
	if paste, ok := msg.(tea.PasteMsg); ok {
		if !f.input.Focused() {
			return nil, false
		}
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(paste)
		return cmd, true
	}
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	return f.handleKey(child, keyMsg)
}

func (f *subagentSteer) handleKey(child string, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	running := f.ws.AgentSubagentRunning(child)
	if key.Matches(msg, f.keys.Cancel) {
		f.input.Blur()
		if running && f.ws.AgentCancelSubagent(child) {
			return util.ReportInfo("Cancelled subagent"), true
		}
		return util.ReportInfo("This subagent isn't running. Go back to cancel the main run."), true
	}
	if !running {
		f.input.Blur()
		return nil, false
	}
	if !f.input.Focused() {
		if key.Matches(msg, f.keys.Focus) {
			return f.input.Focus(), true
		}
		return nil, false
	}
	switch {
	case key.Matches(msg, f.keys.Focus):
		f.input.Blur()
		return nil, true
	case key.Matches(msg, f.keys.Interrupt):
		return f.send(child, true), true
	case key.Matches(msg, f.keys.Send):
		return f.send(child, false), true
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd, true
}

func (f *subagentSteer) send(child string, interrupt bool) tea.Cmd {
	text := strings.TrimSpace(f.input.Value())
	if text == "" {
		return nil
	}
	if err := f.ws.AgentSteerSubagent(child, text, interrupt); err != nil {
		return util.ReportWarn("Message not sent: " + err.Error())
	}
	f.input.Reset()
	if interrupt {
		return util.ReportInfo("Interrupting subagent with your message")
	}
	return util.ReportInfo("Message queued for the subagent's next step")
}

// Observe implements feature. On every message it gives back the steers
// task calls ended without delivering, so a dropped nudge event can't
// lose them. Their call is over, so the subagent's input is closed; they
// go to the main editor, where the human can pass them to the
// orchestrator.
func (f *subagentSteer) Observe(tea.Msg) tea.Cmd {
	returned := f.ws.AgentTakeReturnedSteers()
	if len(returned) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(returned))
	for _, r := range returned {
		f.host.RestoreToComposer(strings.Join(r.Steers, "\n\n"))
		cmds = append(cmds, util.ReportWarn(r.Reason+" Your message is back in the main editor."))
	}
	return tea.Batch(cmds...)
}

// HandleMsg implements feature. It claims the nudge that steers were
// handed back; Observe has already taken them.
func (f *subagentSteer) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	_, ok := msg.(pubsub.Event[agent.SubagentSteersReturned])
	return nil, ok
}

func (f *subagentSteer) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (f *subagentSteer) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }

func bindingHelp(k key.Binding) string { return k.Help().Key + " " + k.Help().Desc }

// DrillInFooter implements drillInFeature.
func (f *subagentSteer) DrillInFooter() panelView {
	child := f.sync()
	if child == "" {
		return nil
	}
	return subagentSteerFooter{
		f:       f,
		running: f.ws.AgentSubagentRunning(child),
		queued:  len(f.ws.AgentSubagentPendingSteers(child)),
	}
}

// subagentSteerFooter is the footer's view-model.
type subagentSteerFooter struct {
	f       *subagentSteer
	running bool
	queued  int
}

func (v subagentSteerFooter) Lines() int { return subagentSteerFooterLines }

func (v subagentSteerFooter) Render(width int) string {
	t := v.f.com.Styles
	muted := t.Help.ShortDesc
	if !v.running {
		hint := muted.Render("This subagent isn't running. To continue it, ask the orchestrator to resume it.")
		return ansi.Truncate(hint, width, "…") + "\n"
	}
	input := v.f.input
	input.SetWidth(width)
	cancel := v.f.keys.Cancel.Help().Key + " cancel subagent"
	help := []string{v.f.keys.Focus.Help().Key + " message subagent", cancel}
	if input.Focused() {
		help = []string{bindingHelp(v.f.keys.Send), bindingHelp(v.f.keys.Interrupt), v.f.keys.Focus.Help().Key + " back to chat", cancel}
	}
	if v.queued > 0 {
		help = append(help, fmt.Sprintf("%d queued", v.queued))
	}
	line := muted.Render(strings.Join(help, " · "))
	return ansi.Truncate(firstLine(input.View()), width, "…") + "\n" + ansi.Truncate(line, width, "…")
}
