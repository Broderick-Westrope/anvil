package model

import (
	"context"
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/app/branchsummaries"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// branchSummaryWorkspace is what the branch summary feature asks of the
// workspace.
type branchSummaryWorkspace interface {
	GetBranchPath(ctx context.Context, leafMessageID string) ([]message.Message, error)
	AgentStartBranchSummary(req agent.BranchSummaryRequest) (string, error)
	AgentCancelBranchSummary(sessionID string)
	AgentBranchSummaryRunning(sessionID string) bool
	AgentDiscardBranchSummary(ctx context.Context, sessionID, summaryID string) error
	AgentBranchSummarizers() []string
	Config() *config.Config
}

// branchSummaryRun is what the UI knows about a session's latest summary,
// built from the service's events: running, stored and not yet followed
// by a message, or failed.
type branchSummaryRun struct {
	runID     string
	agent     string
	state     branchsummaries.State
	summaryID string
	err       error
	// used is set when a user message is created while the run is
	// running. Sends queue behind the summary, so that message follows
	// it, even when its event arrives before the summary's.
	used bool
}

// branchSummaryOfferMsg says a rewind left something worth summarizing.
type branchSummaryOfferMsg struct{ offer dialog.BranchSummaryOffer }

// branchSummaryDiscardedMsg reports a discard.
type branchSummaryDiscardedMsg struct{ err error }

// actionBranchSummaryCancel cancels a running summary from the palette.
type actionBranchSummaryCancel struct{}

// actionBranchSummaryDismiss discards a stored summary, or forgets a
// failed one, from the palette.
type actionBranchSummaryDismiss struct{}

// branchSummaryFeature offers to summarize the branch a rewind left
// behind, starts the chosen summarizer, and shows the runs the
// branchsummaries service reports, with controls to cancel a running
// summary or discard a stored one before sending.
type branchSummaryFeature struct {
	host featureHost
	com  *common.Common
	ws   branchSummaryWorkspace

	// lastAgent is the summarizer picked last, offered first next time.
	lastAgent string
	// runs holds each root session's latest summary.
	runs map[string]*branchSummaryRun
	// pending is the latest rewind waiting for its offer. An offer for any
	// other rewind is stale and dropped.
	pending dialog.BranchSummaryOffer
}

func newBranchSummaryFeature(host featureHost, com *common.Common, ws branchSummaryWorkspace) *branchSummaryFeature {
	return &branchSummaryFeature{host: host, com: com, ws: ws, runs: map[string]*branchSummaryRun{}}
}

// Busy reports whether a summary is running for sessionID.
func (b *branchSummaryFeature) Busy(sessionID string) bool {
	return b.ws.AgentBranchSummaryRunning(sessionID)
}

// Cancel cancels sessionID's running summary. The rewind stays.
func (b *branchSummaryFeature) Cancel(sessionID string) tea.Cmd {
	return func() tea.Msg {
		b.ws.AgentCancelBranchSummary(sessionID)
		return util.NewInfoMsg("Cancelling branch summary...")
	}
}

// Observe implements feature. After a rewind it offers a summary; on a
// return to the pre-branch conversation it drops the summary; once a
// message follows a stored summary, the summary can't be discarded.
func (b *branchSummaryFeature) Observe(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case navigateTreeDoneMsg:
		return b.observeRewind(msg)
	case pubsub.Event[message.Message]:
		if msg.Type != pubsub.CreatedEvent {
			return nil
		}
		if r := b.runs[msg.Payload.SessionID]; r != nil {
			switch {
			case r.summaryID != "" && msg.Payload.ParentMessageID == r.summaryID:
				delete(b.runs, msg.Payload.SessionID)
			case r.state == branchsummaries.StateRunning && isSentMessage(msg.Payload):
				r.used = true
			}
		}
		if isSentMessage(msg.Payload) && msg.Payload.SessionID == b.pending.SessionID {
			b.pending = dialog.BranchSummaryOffer{}
		}
	}
	return nil
}

func (b *branchSummaryFeature) observeRewind(done navigateTreeDoneMsg) tea.Cmd {
	if done.err != nil || done.session == nil {
		return nil
	}
	sessionID := done.session.ID
	b.pending = dialog.BranchSummaryOffer{}
	if done.restore != nil {
		return b.drop(sessionID)
	}
	// A stored summary the user rewinds away from is part of the branch
	// left behind now; the next summary carries it forward.
	if r := b.runs[sessionID]; r != nil && r.state == branchsummaries.StateStored {
		delete(b.runs, sessionID)
	}
	if done.snapshot == nil || done.snapshot.leafID == "" || done.snapshot.leafID == done.leafID {
		return nil
	}
	if b.ws.Config().Options.BranchSummaryPrompt() == config.BranchSummaryNever {
		return nil
	}
	offer := dialog.BranchSummaryOffer{SessionID: sessionID, FromLeafID: done.snapshot.leafID, TargetID: done.leafID}
	b.pending = offer
	return func() tea.Msg {
		ctx := context.Background()
		from, err := b.ws.GetBranchPath(ctx, offer.FromLeafID)
		if err != nil {
			slog.Warn("Failed to load the branch left by a rewind", "error", err)
			return nil
		}
		var target []message.Message
		if offer.TargetID != "" {
			if target, err = b.ws.GetBranchPath(ctx, offer.TargetID); err != nil {
				slog.Warn("Failed to load the branch a rewind moved to", "error", err)
				return nil
			}
		}
		if !branchsummary.Worthwhile(branchsummary.Abandoned(from, target)) {
			return nil
		}
		return branchSummaryOfferMsg{offer: offer}
	}
}

// isSentMessage reports whether m is a message the user sent, not a
// metadata entry such as a branch summary, which is also a user message.
func isSentMessage(m message.Message) bool {
	return m.Role == message.User && (m.MessageType == message.MessageTypeMessage || m.MessageType == "")
}

func sameRewind(a, b dialog.BranchSummaryOffer) bool {
	return a.SessionID == b.SessionID && a.FromLeafID == b.FromLeafID && a.TargetID == b.TargetID
}

// drop cancels sessionID's running summary or discards its stored one.
func (b *branchSummaryFeature) drop(sessionID string) tea.Cmd {
	r := b.runs[sessionID]
	switch {
	case r == nil:
		return nil
	case r.state == branchsummaries.StateRunning:
		return b.Cancel(sessionID)
	default:
		return b.dismiss(sessionID)
	}
}

// HandleMsg implements feature.
func (b *branchSummaryFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case branchSummaryOfferMsg:
		return b.offer(msg.offer), true
	case pubsub.Event[branchsummaries.Event]:
		return b.event(msg.Payload), true
	case branchSummaryDiscardedMsg:
		switch {
		case errors.Is(msg.err, agent.ErrBranchSummaryInUse):
			return util.ReportWarn("The branch summary is already part of the conversation."), true
		case msg.err != nil:
			return util.ReportError(msg.err), true
		}
		return util.ReportInfo("Branch summary discarded."), true
	}
	return nil, false
}

func (b *branchSummaryFeature) offer(offer dialog.BranchSummaryOffer) tea.Cmd {
	if !sameRewind(offer, b.pending) || b.host.SessionID() != offer.SessionID {
		return nil
	}
	b.pending = dialog.BranchSummaryOffer{}
	offer.Agents = b.ws.AgentBranchSummarizers()
	if len(offer.Agents) == 0 {
		return util.ReportWarn("No branch summarizer is available.")
	}
	offer.DefaultAgent = b.lastAgent
	if b.ws.Config().Options.BranchSummaryPrompt() == config.BranchSummaryAlways {
		agentName := offer.Agents[0]
		for _, a := range offer.Agents {
			if a == b.lastAgent {
				agentName = a
			}
		}
		return b.start(dialog.ActionBranchSummaryPick{
			SessionID: offer.SessionID, FromLeafID: offer.FromLeafID, TargetID: offer.TargetID, Agent: agentName,
		})
	}
	b.host.Dialogs().OpenDialog(dialog.NewBranchSummaryPicker(b.com, offer))
	return nil
}

func (b *branchSummaryFeature) event(ev branchsummaries.Event) tea.Cmd {
	defer b.host.Relayout()
	if ev.State == branchsummaries.StateRunning {
		b.runs[ev.SessionID] = &branchSummaryRun{runID: ev.RunID, agent: ev.Agent, state: ev.State}
		return nil
	}
	r := b.runs[ev.SessionID]
	if r == nil || r.runID != ev.RunID {
		return nil
	}
	switch ev.State {
	case branchsummaries.StateStored:
		if r.used {
			delete(b.runs, ev.SessionID)
			return nil
		}
		r.state, r.summaryID = ev.State, ev.SummaryID
	case branchsummaries.StateFailed:
		r.state, r.err = ev.State, ev.Err
	case branchsummaries.StateCancelled:
		delete(b.runs, ev.SessionID)
		return util.ReportInfo("Branch summary cancelled.")
	default:
		delete(b.runs, ev.SessionID)
	}
	return nil
}

// HandleAction implements feature.
func (b *branchSummaryFeature) HandleAction(action dialog.Action) (tea.Cmd, bool) {
	switch action := action.(type) {
	case dialog.ActionBranchSummaryPick:
		b.host.Dialogs().CloseDialog(dialog.BranchSummaryPickerID)
		if action.Agent == "" {
			return nil, true
		}
		return b.start(action), true
	case dialog.ActionToggleLazyMCP, dialog.ActionSelectModel, dialog.ActionSelectReasoningEffort:
		// Each is stored as a message under the leaf, which would move the
		// leaf off the target the running summary is stored under.
		if b.Busy(b.host.SessionID()) {
			return util.ReportWarn("Wait for the branch summary, or cancel it, before changing this."), true
		}
		return nil, false
	case actionBranchSummaryCancel:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.Cancel(b.host.SessionID()), true
	case actionBranchSummaryDismiss:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.dismiss(b.host.SessionID()), true
	}
	return nil, false
}

// start starts the summary here rather than in a command: starting
// reserves the session, and it must do so before any message the user
// sends next, whose send runs in a command.
func (b *branchSummaryFeature) start(pick dialog.ActionBranchSummaryPick) tea.Cmd {
	b.lastAgent = pick.Agent
	_, err := b.ws.AgentStartBranchSummary(agent.BranchSummaryRequest{
		SessionID: pick.SessionID, FromLeafID: pick.FromLeafID, TargetID: pick.TargetID,
		Agent: pick.Agent, Focus: pick.Focus,
	})
	if err != nil {
		return util.ReportError(err)
	}
	return nil
}

// dismiss discards sessionID's stored summary, or forgets a failed one.
func (b *branchSummaryFeature) dismiss(sessionID string) tea.Cmd {
	r := b.runs[sessionID]
	if r == nil || r.state == branchsummaries.StateRunning {
		return nil
	}
	delete(b.runs, sessionID)
	b.host.Relayout()
	if r.summaryID == "" {
		return nil
	}
	return func() tea.Msg {
		return branchSummaryDiscardedMsg{err: b.ws.AgentDiscardBranchSummary(context.Background(), sessionID, r.summaryID)}
	}
}

// HandleKey implements feature.
func (b *branchSummaryFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool) { return nil, false }

// PaletteEntries implements paletteFeature.
func (b *branchSummaryFeature) PaletteEntries() []*dialog.CommandItem {
	r := b.runs[b.host.SessionID()]
	if r == nil {
		return nil
	}
	id, title, action := "branch_summary_dismiss", "Discard Branch Summary", dialog.Action(actionBranchSummaryDismiss{})
	switch r.state {
	case branchsummaries.StateRunning:
		id, title, action = "branch_summary_cancel", "Cancel Branch Summary", actionBranchSummaryCancel{}
	case branchsummaries.StateFailed:
		title = "Dismiss Branch Summary Error"
	}
	return []*dialog.CommandItem{dialog.NewCommandItem(b.com.Styles, id, title, "", action)}
}

// Panel implements panelFeature: one line while the summary runs or after
// it failed.
func (b *branchSummaryFeature) Panel() panelView {
	r := b.runs[b.host.SessionID()]
	if r == nil || r.state == branchsummaries.StateStored {
		return nil
	}
	return branchSummaryPanel{styles: b.com.Styles, agent: r.agent, err: r.err}
}

type branchSummaryPanel struct {
	styles *styles.Styles
	agent  string
	err    error
}

func (branchSummaryPanel) Lines() int { return 1 }

func (p branchSummaryPanel) Render(width int) string {
	t := p.styles
	sep := t.Pills.HelpText.Render(" · ")
	line := t.Pills.TodoLabel.Render("Branch summary") + sep + t.Pills.TodoProgress.Render(p.agent) + sep
	hint := t.Pills.HelpKey.Render("esc") + " " + t.Pills.HelpText.Render("cancel")
	if p.err != nil {
		line += t.Pills.QueueLabel.Render("failed: " + firstLine(p.err.Error()))
		hint = t.Pills.HelpKey.Render("ctrl+p") + " " + t.Pills.HelpText.Render("dismiss")
	} else {
		line += t.Pills.HelpText.Render("summarizing the branch you left…")
	}
	available := width - lipgloss.Width(hint) - 2
	line = ansi.Truncate(line, max(available, 0), "…")
	return ansi.Truncate(line+"  "+hint, width, "…")
}
