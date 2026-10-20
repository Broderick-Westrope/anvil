package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// branchSummaryTestWorkspace serves a conversation u1, a1 with a tangent
// u2, a2 off a1, and records summary requests.
type branchSummaryTestWorkspace struct {
	cfg       *config.Config
	paths     map[string][]message.Message
	agents    []string
	requests  []agent.BranchSummaryRequest
	ctxs      []context.Context
	result    message.Message
	err       error
	discarded []string
}

func newBranchSummaryTestWorkspace() *branchSummaryTestWorkspace {
	u1 := message.Message{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "plan"}}}
	a1 := message.Message{ID: "a1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "plan made"}}}
	u2 := message.Message{ID: "u2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "do 1"}}}
	a2 := message.Message{ID: "a2", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "did 1"}}}
	return &branchSummaryTestWorkspace{
		cfg: &config.Config{Options: &config.Options{}},
		paths: map[string][]message.Message{
			"a1": {u1, a1},
			"u2": {u1, a1, u2},
			"a2": {u1, a1, u2, a2},
		},
		agents: []string{agent.BuiltinBranchSummarizer, "pr-summarizer"},
		result: message.Message{ID: "summary-1", MessageType: message.MessageTypeBranchSummary},
	}
}

func (w *branchSummaryTestWorkspace) GetBranchPath(_ context.Context, leaf string) ([]message.Message, error) {
	return w.paths[leaf], nil
}

func (w *branchSummaryTestWorkspace) AgentSummarizeBranch(ctx context.Context, req agent.BranchSummaryRequest) (message.Message, error) {
	w.requests = append(w.requests, req)
	w.ctxs = append(w.ctxs, ctx)
	return w.result, w.err
}

func (w *branchSummaryTestWorkspace) AgentDiscardBranchSummary(_ context.Context, _, summaryID string) error {
	w.discarded = append(w.discarded, summaryID)
	return nil
}

func (w *branchSummaryTestWorkspace) AgentBranchSummarizers() []string { return w.agents }

func (w *branchSummaryTestWorkspace) Config() *config.Config { return w.cfg }

type branchSummaryFixture struct {
	f    *branchSummaryFeature
	host *fakeHost
	ws   *branchSummaryTestWorkspace
}

func newBranchSummaryFixture(t *testing.T) *branchSummaryFixture {
	t.Helper()
	ws := newBranchSummaryTestWorkspace()
	host := &fakeHost{session: &session.Session{ID: "sess-1"}, overlay: dialog.NewOverlay(), keyMap: DefaultKeyMap()}
	return &branchSummaryFixture{f: newBranchSummaryFeature(host, common.DefaultCommon(nil), ws), host: host, ws: ws}
}

// rewind reports a finished rewind from one leaf to another and feeds the
// feature its own follow-up messages until none are left.
func (b *branchSummaryFixture) rewind(from, target string) {
	b.run(b.f.Observe(navigateTreeDoneMsg{
		session:  &session.Session{ID: "sess-1"},
		leafID:   target,
		snapshot: &branchReturnSnapshot{sessionID: "sess-1", leafID: from},
	}))
}

// run runs cmd and feeds the feature its own messages until none are
// left.
func (b *branchSummaryFixture) run(cmd tea.Cmd) {
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		if next, ok := b.f.HandleMsg(msg); ok {
			queue = append(queue, cmdMsgs(next)...)
		}
	}
}

func (b *branchSummaryFixture) pick(agentName, focus string) {
	cmd, ok := b.f.HandleAction(dialog.ActionBranchSummaryPick{
		SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: agentName, Focus: focus,
	})
	if !ok {
		panic("pick not handled")
	}
	b.run(cmd)
}

func TestBranchSummary_OffersPickerAfterRewind(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)

	b.rewind("a2", "a1")

	require.True(t, b.host.overlay.ContainsDialog(dialog.BranchSummaryPickerID))
}

func TestBranchSummary_NoOfferWithoutAnAssistantReply(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)

	b.rewind("u2", "a1")

	require.False(t, b.host.overlay.HasDialogs())
	require.Empty(t, b.ws.requests)
}

func TestBranchSummary_PromptSetting(t *testing.T) {
	t.Parallel()

	t.Run("never", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.ws.cfg.Options.BranchSummary = &config.BranchSummaryOptions{Prompt: config.BranchSummaryNever}

		b.rewind("a2", "a1")

		require.False(t, b.host.overlay.HasDialogs())
		require.Empty(t, b.ws.requests)
	})
	t.Run("always", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.ws.cfg.Options.BranchSummary = &config.BranchSummaryOptions{Prompt: config.BranchSummaryAlways}

		b.rewind("a2", "a1")

		require.False(t, b.host.overlay.HasDialogs())
		require.Len(t, b.ws.requests, 1)
		require.Equal(t, agent.BuiltinBranchSummarizer, b.ws.requests[0].Agent)
	})
}

func TestBranchSummary_PickRunsTheSummarizer(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)

	cmd, ok := b.f.HandleAction(dialog.ActionBranchSummaryPick{
		SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer", Focus: "the PR",
	})
	require.True(t, ok)
	require.True(t, b.f.Busy("sess-1"))
	panel := b.f.Panel()
	require.NotNil(t, panel)
	require.Contains(t, ansi.Strip(panel.Render(120)), "pr-summarizer")

	b.run(cmd)

	require.Len(t, b.ws.requests, 1)
	req := b.ws.requests[0]
	require.Equal(t, "sess-1", req.SessionID)
	require.Equal(t, "a2", req.FromLeafID)
	require.Equal(t, "a1", req.TargetID)
	require.Equal(t, "pr-summarizer", req.Agent)
	require.Equal(t, "the PR", req.Focus)
	require.NotEmpty(t, req.ChildSessionID)
	require.False(t, b.f.Busy("sess-1"))
	require.Nil(t, b.f.Panel())
}

func TestBranchSummary_RemembersTheLastSummarizer(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.pick("pr-summarizer", "")

	b.rewind("a2", "a1")

	d, ok := b.host.overlay.Dialog(dialog.BranchSummaryPickerID).(*dialog.BranchSummaryPicker)
	require.True(t, ok)
	require.Equal(t, "pr-summarizer", d.Offer().DefaultAgent)
}

func TestBranchSummary_NoSummaryPicked(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)

	b.pick("", "")

	require.Empty(t, b.ws.requests)
	require.False(t, b.f.Busy("sess-1"))
}

func TestBranchSummary_Cancel(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	cmd, _ := b.f.HandleAction(dialog.ActionBranchSummaryPick{SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer"})

	b.run(b.f.Cancel())
	b.ws.err = context.Canceled
	b.run(cmd)

	require.ErrorIs(t, b.ws.ctxs[0].Err(), context.Canceled)
	require.False(t, b.f.Busy("sess-1"))
	require.Nil(t, b.f.Panel())
}

func TestBranchSummary_FailureShowsUntilDismissed(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.ws.err = errors.New("provider exploded")

	b.pick("pr-summarizer", "")

	panel := b.f.Panel()
	require.NotNil(t, panel)
	require.Contains(t, ansi.Strip(panel.Render(120)), "provider exploded")
	require.False(t, b.f.Busy("sess-1"))

	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	cmd, ok := b.f.HandleAction(entries[0].Action())
	require.True(t, ok)
	b.run(cmd)
	require.Nil(t, b.f.Panel())
}

func TestBranchSummary_StaleCompletionIsQuiet(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.ws.err = agent.ErrStaleBranchSummary

	b.pick("pr-summarizer", "")

	require.Nil(t, b.f.Panel())
}

func TestBranchSummary_DiscardBeforeSend(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.pick("pr-summarizer", "")

	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	cmd, ok := b.f.HandleAction(entries[0].Action())
	require.True(t, ok)
	b.run(cmd)

	require.Equal(t, []string{"summary-1"}, b.ws.discarded)
	require.Empty(t, b.f.PaletteEntries())
}

func TestBranchSummary_ReturnToPreBranchDiscards(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.pick("pr-summarizer", "")

	b.run(b.f.Observe(navigateTreeDoneMsg{
		session: &session.Session{ID: "sess-1"},
		leafID:  "a2",
		restore: &branchReturnSnapshot{sessionID: "sess-1", leafID: "a2"},
	}))

	require.Equal(t, []string{"summary-1"}, b.ws.discarded)
}

func TestBranchSummary_PaletteCancelsARunningSummary(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	_, _ = b.f.HandleAction(dialog.ActionBranchSummaryPick{SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer"})
	cancelled := false
	b.f.op.cancel = func() { cancelled = true }

	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	_, ok := b.f.HandleAction(entries[0].Action())

	require.True(t, ok)
	require.True(t, cancelled)
}
