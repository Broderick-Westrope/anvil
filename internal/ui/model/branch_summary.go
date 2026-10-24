package model

import (
	"cmp"
	"context"
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app/branchsummaries"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// branchSummaryWorkspace is what the branch summary feature asks of the
// workspace.
type branchSummaryWorkspace interface {
	AgentBranchWorthSummarizing(ctx context.Context, fromLeaf, target string) (bool, error)
	AgentStartBranchSummary(req agent.BranchSummaryRequest) (string, error)
	AgentCancelBranchSummary(sessionID string)
	AgentBranchSummaryRunning(sessionID string) bool
	AgentIsSessionBusy(sessionID string) bool
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
	// lost is set when the service no longer runs a summary this UI still
	// shows as running: its end event was dropped, or hasn't arrived yet.
	// A lost run shows nothing, but a late end event still applies.
	lost bool
	// used is set when a user message is created while the run is
	// running. Sends queue behind the summary, so that message follows
	// it, even when its event arrives before the summary's.
	used bool
	// toolCallID and childID name the summarizer's chat item and the
	// agent tool session it runs in. They are empty for a run this UI
	// didn't start.
	toolCallID string
	childID    string
}

// branchSummaryRootAnchor anchors the child session of a summary stored
// at the root of the conversation, which has no message to anchor to.
const branchSummaryRootAnchor = "branch-summary"

// branchSummaryOfferMsg says a rewind left something worth summarizing.
type branchSummaryOfferMsg struct{ offer dialog.RewindOffer }

// branchSummaryDiscardedMsg reports a discard.
type branchSummaryDiscardedMsg struct{ err error }

// actionBranchSummaryCancel cancels a running summary from the palette.
type actionBranchSummaryCancel struct{}

// actionBranchSummaryDismiss discards a stored summary, or forgets a
// failed one, from the palette.
type actionBranchSummaryDismiss struct{}

var (
	_ feature         = (*branchSummaryFeature)(nil)
	_ panelFeature    = (*branchSummaryFeature)(nil)
	_ paletteFeature  = (*branchSummaryFeature)(nil)
	_ cancelFeature   = (*branchSummaryFeature)(nil)
	_ navigationGuard = (*branchSummaryFeature)(nil)
)

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
	pending dialog.RewindOffer
	// live holds the running summarizers' chat items by child session,
	// until their stored summary's own item replaces them.
	live map[string]*chat.BranchSummaryItem
}

func newBranchSummaryFeature(host featureHost, com *common.Common, ws branchSummaryWorkspace) *branchSummaryFeature {
	return &branchSummaryFeature{
		host: host, com: com, ws: ws,
		runs: map[string]*branchSummaryRun{}, live: map[string]*chat.BranchSummaryItem{},
	}
}

// Busy reports whether a summary is running for sessionID.
func (b *branchSummaryFeature) Busy(sessionID string) bool {
	return b.ws.AgentBranchSummaryRunning(sessionID)
}

// Cancel implements cancelFeature. It claims the cancel keys while the
// root session's summary runs and nothing else runs in that session: the
// summarizer makes the app busy, but cancelling the root session would
// drop the sends queued behind the summary.
func (b *branchSummaryFeature) Cancel() (tea.Cmd, bool) {
	sessionID := b.host.SessionID()
	if !b.Busy(sessionID) || b.ws.AgentIsSessionBusy(sessionID) {
		return nil, false
	}
	return b.cancelSession(sessionID), true
}

// BlockNavigation implements navigationGuard: nothing may move the leaf
// (a rewind, a return, or a metadata entry such as a model change) while
// the summary of the last rewind runs, or the summary would be dropped.
func (b *branchSummaryFeature) BlockNavigation() tea.Cmd {
	if !b.Busy(b.host.SessionID()) {
		return nil
	}
	return util.ReportWarn("Wait for the branch summary, or cancel it with esc, first.")
}

// cancelSession cancels sessionID's running summary. The rewind stays.
func (b *branchSummaryFeature) cancelSession(sessionID string) tea.Cmd {
	return func() tea.Msg {
		b.ws.AgentCancelBranchSummary(sessionID)
		return util.NewInfoMsg("Cancelling branch summary...")
	}
}

// Observe implements feature. After a rewind it offers a summary; on a
// return to the pre-branch conversation it drops the summary; once a
// message follows a stored summary, the summary can't be discarded. A
// stored summary's message replaces its live item, and deleting it
// removes its item. On every message it marks runs the service has
// finished as lost.
func (b *branchSummaryFeature) Observe(msg tea.Msg) tea.Cmd {
	b.markLost()
	switch msg := msg.(type) {
	case navigateTreeDoneMsg:
		return b.observeRewind(msg)
	case pubsub.Event[message.Message]:
		b.observeSummaryItem(msg)
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
			b.pending = dialog.RewindOffer{}
		}
	}
	return nil
}

// observeSummaryItem keeps a stored summary's item in step with its
// message. It runs before UI appends the message's own item, which has
// the live item's ID. UI removes a deleted message's item by message ID,
// which misses a summary's item: it is keyed by its tool call.
func (b *branchSummaryFeature) observeSummaryItem(ev pubsub.Event[message.Message]) {
	if ev.Payload.MessageType != message.MessageTypeBranchSummary {
		return
	}
	var childID string
	for _, p := range ev.Payload.Parts {
		if s, ok := p.(message.BranchSummaryContent); ok {
			childID = s.ChildSessionID
		}
	}
	switch ev.Type {
	case pubsub.CreatedEvent:
		b.removeLive(childID)
	case pubsub.DeletedEvent:
		if _, toolCallID, ok := session.ParseAgentToolSessionID(childID); ok {
			b.host.RemoveChatItem(toolCallID)
		}
	}
}

// removeLive removes the live item of the summarizer running in childID.
func (b *branchSummaryFeature) removeLive(childID string) {
	if item := b.live[childID]; item != nil {
		delete(b.live, childID)
		b.host.RemoveChatItem(item.ID())
	}
}

func (b *branchSummaryFeature) observeRewind(done navigateTreeDoneMsg) tea.Cmd {
	if done.err != nil || done.session == nil {
		return nil
	}
	sessionID := done.session.ID
	b.pending = dialog.RewindOffer{}
	if done.restore != nil {
		return b.drop(sessionID)
	}
	// A stored summary the user rewinds away from is part of the branch
	// left behind now; the next summary carries it forward. A failure
	// belongs to the rewind it was for.
	if r := b.runs[sessionID]; r != nil && r.state != branchsummaries.StateRunning {
		delete(b.runs, sessionID)
	}
	if done.snapshot == nil || done.snapshot.leafID == "" || done.snapshot.leafID == done.leafID {
		return nil
	}
	if b.ws.Config().Options.BranchSummaryOffer() == config.BranchSummaryNever {
		return nil
	}
	offer := dialog.RewindOffer{SessionID: sessionID, FromLeafID: done.snapshot.leafID, TargetID: done.leafID}
	b.pending = offer
	return func() tea.Msg {
		worth, err := b.ws.AgentBranchWorthSummarizing(context.Background(), offer.FromLeafID, offer.TargetID)
		if err != nil {
			slog.Warn("Failed to check the branch left by a rewind", "error", err)
			return nil
		}
		if !worth {
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

// drop cancels sessionID's running summary or discards its stored one.
func (b *branchSummaryFeature) drop(sessionID string) tea.Cmd {
	r := b.run(sessionID)
	switch {
	case r == nil:
		return nil
	case r.state == branchsummaries.StateRunning:
		return b.cancelSession(sessionID)
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

func (b *branchSummaryFeature) offer(offer dialog.RewindOffer) tea.Cmd {
	if offer.SessionID != b.pending.SessionID || offer.FromLeafID != b.pending.FromLeafID ||
		offer.TargetID != b.pending.TargetID || b.host.SessionID() != offer.SessionID {
		return nil
	}
	b.pending = dialog.RewindOffer{}
	offer.Agents = b.ws.AgentBranchSummarizers()
	if len(offer.Agents) == 0 {
		return util.ReportWarn("No branch summarizer is available.")
	}
	offer.DefaultAgent = b.lastAgent
	if b.ws.Config().Options.BranchSummaryOffer() == config.BranchSummaryAlways {
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

// run returns sessionID's latest summary for display and controls, or nil.
// It only reads state: Observe marks runs lost.
func (b *branchSummaryFeature) run(sessionID string) *branchSummaryRun {
	r := b.runs[sessionID]
	if r == nil || r.lost {
		return nil
	}
	return r
}

// markLost marks the runs shown as running that the service has finished
// as lost, so a dropped end event can't leave a panel stuck, and relays
// out when one changed.
func (b *branchSummaryFeature) markLost() {
	changed := false
	for sessionID, r := range b.runs {
		if r.state == branchsummaries.StateRunning && !r.lost && !b.Busy(sessionID) {
			r.lost = true
			changed = true
		}
	}
	if changed {
		b.host.Relayout()
	}
}

func (b *branchSummaryFeature) event(ev branchsummaries.Event) tea.Cmd {
	defer b.host.Relayout()
	r := b.runs[ev.SessionID]
	if ev.State == branchsummaries.StateRunning {
		if r == nil || r.runID != ev.RunID {
			b.runs[ev.SessionID] = &branchSummaryRun{runID: ev.RunID, agent: ev.Agent, state: ev.State}
		} else {
			r.lost = false
		}
		return nil
	}
	if r == nil || r.runID != ev.RunID {
		return nil
	}
	r.lost = false
	switch ev.State {
	case branchsummaries.StateStored:
		// The live item stays until the summary's message replaces it.
		if r.used {
			delete(b.runs, ev.SessionID)
			return nil
		}
		r.state, r.summaryID = ev.State, ev.SummaryID
	case branchsummaries.StateFailed:
		r.state, r.err = ev.State, ev.Err
		if item := b.live[r.childID]; item != nil {
			delete(b.live, r.childID)
			item.SetFailed(chat.FirstLine(ev.Err.Error()))
		}
	case branchsummaries.StateCancelled:
		b.end(ev.SessionID, r)
		return util.ReportInfo("Branch summary cancelled.")
	case branchsummaries.StateNothing:
		b.end(ev.SessionID, r)
		return util.ReportInfo("Branch summary skipped: there was nothing to summarize.")
	default:
		b.end(ev.SessionID, r)
		return util.ReportInfo("Branch summary not kept: the conversation moved on before it finished.")
	}
	return nil
}

// end forgets sessionID's run r, which stored nothing, and its live item.
func (b *branchSummaryFeature) end(sessionID string, r *branchSummaryRun) {
	delete(b.runs, sessionID)
	b.removeLive(r.childID)
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
	case actionBranchSummaryCancel:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.cancelSession(b.host.SessionID()), true
	case actionBranchSummaryDismiss:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.dismiss(b.host.SessionID()), true
	}
	return nil, false
}

// start starts the summary here rather than in a command: starting
// reserves the session, and it must do so before any message the user
// sends next, whose send runs in a command.
//
// The summarizer runs in an agent tool session of the target and a new
// tool call, so the live item, keyed by that call, gets its child
// session's messages and stats as a task agent's item does.
func (b *branchSummaryFeature) start(pick dialog.ActionBranchSummaryPick) tea.Cmd {
	b.lastAgent = pick.Agent
	anchor := cmp.Or(pick.TargetID, branchSummaryRootAnchor)
	toolCallID := uuid.NewString()
	childID := session.AgentToolSessionID(anchor, toolCallID)
	runID, err := b.ws.AgentStartBranchSummary(agent.BranchSummaryRequest{
		SessionID: pick.SessionID, FromLeafID: pick.FromLeafID, TargetID: pick.TargetID,
		Agent: pick.Agent, Focus: pick.Focus, ChildSessionID: childID,
	})
	if err != nil {
		return util.ReportError(err)
	}
	b.runs[pick.SessionID] = &branchSummaryRun{
		runID: runID, agent: pick.Agent, state: branchsummaries.StateRunning,
		toolCallID: toolCallID, childID: childID,
	}
	if b.host.SessionID() != pick.SessionID {
		return nil
	}
	item := chat.NewBranchSummaryItem(b.com.Styles, anchor, toolCallID, pick.Agent, nil)
	b.live[childID] = item
	return b.host.ShowChatItem(item)
}

// dismiss discards sessionID's stored summary, or forgets a failed one.
func (b *branchSummaryFeature) dismiss(sessionID string) tea.Cmd {
	r := b.run(sessionID)
	if r == nil || r.state == branchsummaries.StateRunning {
		return nil
	}
	delete(b.runs, sessionID)
	b.host.Relayout()
	if r.summaryID == "" {
		b.host.RemoveChatItem(r.toolCallID)
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
	r := b.run(b.host.SessionID())
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

// Panel implements panelFeature: one line after the summary failed. A
// running summary shows as its chat item instead.
func (b *branchSummaryFeature) Panel() panelView {
	r := b.run(b.host.SessionID())
	if r == nil || r.state != branchsummaries.StateFailed {
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
	line := t.Pills.TodoLabel.Render("Branch summary") + sep + t.Pills.TodoProgress.Render(p.agent) + sep +
		t.Pills.QueueLabel.Render("failed: "+chat.FirstLine(p.err.Error()))
	hint := t.Pills.HelpKey.Render("ctrl+p") + " " + t.Pills.HelpText.Render("dismiss")
	available := width - lipgloss.Width(hint) - 2
	line = ansi.Truncate(line, max(available, 0), "…")
	return ansi.Truncate(line+"  "+hint, width, "…")
}
