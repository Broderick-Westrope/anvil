package model

import (
	"context"
	"errors"
	"log/slog"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/branchsummary"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// branchSummaryWorkspace is what the branch summary feature asks of the
// workspace.
type branchSummaryWorkspace interface {
	GetBranchPath(ctx context.Context, leafMessageID string) ([]message.Message, error)
	AgentSummarizeBranch(ctx context.Context, req agent.BranchSummaryRequest) (message.Message, error)
	AgentDiscardBranchSummary(ctx context.Context, sessionID, summaryID string) error
	AgentBranchSummarizers() []string
	Config() *config.Config
}

// branchSummaryOp is the summary of one rewind: running, stored and not
// yet followed by a message, or failed.
type branchSummaryOp struct {
	sessionID string
	agent     string
	childID   string
	cancel    context.CancelFunc
	// summaryID is set once the summary is stored.
	summaryID string
	err       error
}

func (o *branchSummaryOp) running() bool { return o.summaryID == "" && o.err == nil }

// branchSummaryOfferMsg says a rewind left something worth summarizing.
type branchSummaryOfferMsg struct{ offer dialog.BranchSummaryOffer }

// branchSummaryDoneMsg is how a summary run ended.
type branchSummaryDoneMsg struct {
	childID string
	summary message.Message
	err     error
}

// actionBranchSummaryDismiss discards a stored summary, or forgets a
// failed one, from the palette.
type actionBranchSummaryDismiss struct{}

// actionBranchSummaryCancel cancels a running summary from the palette.
type actionBranchSummaryCancel struct{}

// branchSummaryFeature offers to summarize the branch a rewind left
// behind, runs the chosen summarizer in the background, and lets the user
// cancel it, or discard what it wrote before sending.
type branchSummaryFeature struct {
	host featureHost
	com  *common.Common
	ws   branchSummaryWorkspace

	// lastAgent is the summarizer picked last, offered first next time.
	lastAgent string
	op        *branchSummaryOp
}

func newBranchSummaryFeature(host featureHost, com *common.Common, ws branchSummaryWorkspace) *branchSummaryFeature {
	return &branchSummaryFeature{host: host, com: com, ws: ws}
}

// Busy reports whether a summary is generating for sessionID.
func (b *branchSummaryFeature) Busy(sessionID string) bool {
	return b.op != nil && b.op.sessionID == sessionID && b.op.running()
}

// Cancel stops the running summary, if any. The rewind stays.
func (b *branchSummaryFeature) Cancel() tea.Cmd {
	if b.op == nil || !b.op.running() {
		return nil
	}
	b.op.cancel()
	return util.ReportInfo("Cancelling branch summary...")
}

// Observe implements feature. It offers a summary after a rewind, and
// drops the summary when the user returns to the branch it summarized.
func (b *branchSummaryFeature) Observe(msg tea.Msg) tea.Cmd {
	done, ok := msg.(navigateTreeDoneMsg)
	if !ok || done.err != nil || done.session == nil {
		return nil
	}
	if done.restore != nil {
		return b.dropForSession(done.session.ID)
	}
	if done.snapshot == nil || done.snapshot.leafID == "" || done.snapshot.leafID == done.leafID {
		return nil
	}
	if b.ws.Config().Options.BranchSummaryPrompt() == config.BranchSummaryNever {
		return nil
	}
	offer := dialog.BranchSummaryOffer{
		SessionID: done.session.ID, FromLeafID: done.snapshot.leafID, TargetID: done.leafID,
	}
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

// dropForSession cancels or discards the summary of sessionID's last
// rewind.
func (b *branchSummaryFeature) dropForSession(sessionID string) tea.Cmd {
	if b.op == nil || b.op.sessionID != sessionID {
		return nil
	}
	if b.op.running() {
		return b.Cancel()
	}
	return b.dismiss()
}

// HandleMsg implements feature.
func (b *branchSummaryFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case branchSummaryOfferMsg:
		return b.offer(msg.offer), true
	case branchSummaryDoneMsg:
		return b.finish(msg), true
	}
	return nil, false
}

func (b *branchSummaryFeature) offer(offer dialog.BranchSummaryOffer) tea.Cmd {
	if b.host.SessionID() != offer.SessionID {
		return nil
	}
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

// HandleAction implements feature.
func (b *branchSummaryFeature) HandleAction(action dialog.Action) (tea.Cmd, bool) {
	switch action := action.(type) {
	case dialog.ActionBranchSummaryPick:
		b.host.Dialogs().CloseDialog(dialog.BranchSummaryPickerID)
		if action.Agent == "" {
			return nil, true
		}
		return b.start(action), true
	case actionBranchSummaryDismiss:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.dismiss(), true
	case actionBranchSummaryCancel:
		b.host.Dialogs().CloseDialog(dialog.CommandsID)
		return b.Cancel(), true
	}
	return nil, false
}

func (b *branchSummaryFeature) start(pick dialog.ActionBranchSummaryPick) tea.Cmd {
	b.lastAgent = pick.Agent
	ctx, cancel := context.WithCancel(context.Background())
	op := &branchSummaryOp{sessionID: pick.SessionID, agent: pick.Agent, childID: uuid.NewString(), cancel: cancel}
	b.op = op
	b.host.Relayout()
	req := agent.BranchSummaryRequest{
		SessionID: pick.SessionID, FromLeafID: pick.FromLeafID, TargetID: pick.TargetID,
		Agent: pick.Agent, Focus: pick.Focus, ChildSessionID: op.childID,
	}
	return func() tea.Msg {
		defer cancel()
		summary, err := b.ws.AgentSummarizeBranch(ctx, req)
		return branchSummaryDoneMsg{childID: op.childID, summary: summary, err: err}
	}
}

func (b *branchSummaryFeature) finish(msg branchSummaryDoneMsg) tea.Cmd {
	if b.op == nil || b.op.childID != msg.childID {
		return nil
	}
	defer b.host.Relayout()
	switch {
	case msg.err == nil:
		b.op.summaryID = msg.summary.ID
		return nil
	case errors.Is(msg.err, context.Canceled):
		b.op = nil
		return util.ReportInfo("Branch summary cancelled.")
	case errors.Is(msg.err, agent.ErrStaleBranchSummary), errors.Is(msg.err, agent.ErrNothingToSummarize):
		b.op = nil
		return nil
	default:
		b.op.err = msg.err
		return nil
	}
}

// dismiss discards the stored summary, or forgets a failed one.
func (b *branchSummaryFeature) dismiss() tea.Cmd {
	op := b.op
	b.op = nil
	b.host.Relayout()
	if op == nil || op.summaryID == "" {
		return nil
	}
	return func() tea.Msg {
		err := b.ws.AgentDiscardBranchSummary(context.Background(), op.sessionID, op.summaryID)
		if errors.Is(err, agent.ErrBranchSummaryInUse) {
			return util.NewWarnMsg("The branch summary is already part of the conversation.")
		}
		if err != nil {
			return util.NewErrorMsg(err)
		}
		return util.NewInfoMsg("Branch summary discarded.")
	}
}

// HandleKey implements feature.
func (b *branchSummaryFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool) { return nil, false }

// PaletteEntries implements paletteFeature.
func (b *branchSummaryFeature) PaletteEntries() []*dialog.CommandItem {
	if b.op == nil || b.op.sessionID != b.host.SessionID() {
		return nil
	}
	if b.op.running() {
		return []*dialog.CommandItem{
			dialog.NewCommandItem(b.com.Styles, "branch_summary_cancel", "Cancel Branch Summary", "", actionBranchSummaryCancel{}),
		}
	}
	title := "Discard Branch Summary"
	if b.op.err != nil {
		title = "Dismiss Branch Summary Error"
	}
	return []*dialog.CommandItem{
		dialog.NewCommandItem(b.com.Styles, "branch_summary_dismiss", title, "", actionBranchSummaryDismiss{}),
	}
}

// Panel implements panelFeature: one line while the summary runs or after
// it failed.
func (b *branchSummaryFeature) Panel() panelView {
	if b.op == nil || b.op.sessionID != b.host.SessionID() || b.op.summaryID != "" {
		return nil
	}
	return branchSummaryPanel{styles: b.com.Styles, agent: b.op.agent, err: b.op.err}
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
