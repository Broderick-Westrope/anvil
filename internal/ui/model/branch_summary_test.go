package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app/branchsummaries"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// branchSummaryTestWorkspace serves a conversation u1, a1 with a tangent
// u2, a2 off a1, and records what the feature asks of the service.
type branchSummaryTestWorkspace struct {
	cfg       *config.Config
	paths     map[string][]message.Message
	agents    []string
	starts    []agent.BranchSummaryRequest
	startErr  error
	running   map[string]bool
	cancelled []string
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
		agents:  []string{agent.BuiltinBranchSummarizer, "pr-summarizer"},
		running: map[string]bool{},
	}
}

// AgentBranchWorthSummarizing reports a rewind worthwhile when the
// abandoned part of the fixture's paths holds an assistant message.
func (w *branchSummaryTestWorkspace) AgentBranchWorthSummarizing(_ context.Context, from, target string) (bool, error) {
	shared := len(w.paths[target])
	for _, m := range w.paths[from][shared:] {
		if m.Role == message.Assistant {
			return true, nil
		}
	}
	return false, nil
}

func (w *branchSummaryTestWorkspace) AgentStartBranchSummary(req agent.BranchSummaryRequest) (string, error) {
	if w.startErr != nil {
		return "", w.startErr
	}
	w.starts = append(w.starts, req)
	w.running[req.SessionID] = true
	return "run-1", nil
}

func (w *branchSummaryTestWorkspace) AgentCancelBranchSummary(sessionID string) {
	w.cancelled = append(w.cancelled, sessionID)
}

func (w *branchSummaryTestWorkspace) AgentBranchSummaryRunning(sessionID string) bool {
	return w.running[sessionID]
}

func (w *branchSummaryTestWorkspace) AgentDiscardBranchSummary(_ context.Context, _, summaryID string) error {
	w.discarded = append(w.discarded, summaryID)
	return nil
}

func (w *branchSummaryTestWorkspace) AgentBranchSummarizers() []string { return w.agents }

func (*branchSummaryTestWorkspace) AgentIsSessionBusy(string) bool { return false }

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

// run runs cmd and feeds the feature its own messages until none are
// left, returning the status messages.
func (b *branchSummaryFixture) run(cmd tea.Cmd) []util.InfoMsg {
	var infos []util.InfoMsg
	queue := cmdMsgs(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		if info, ok := msg.(util.InfoMsg); ok {
			infos = append(infos, info)
			continue
		}
		if next, ok := b.f.HandleMsg(msg); ok {
			queue = append(queue, cmdMsgs(next)...)
		}
	}
	return infos
}

func (b *branchSummaryFixture) rewind(from, target string) {
	b.run(b.f.Observe(navigateTreeDoneMsg{
		session:  &session.Session{ID: "sess-1"},
		leafID:   target,
		snapshot: &branchReturnSnapshot{sessionID: "sess-1", leafID: from},
	}))
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

// event delivers a service event for sess-1's run-1.
func (b *branchSummaryFixture) event(state branchsummaries.State, summaryID string, err error) []util.InfoMsg {
	if state != branchsummaries.StateRunning {
		b.ws.running["sess-1"] = false
	}
	cmd, ok := b.f.HandleMsg(pubsub.Event[branchsummaries.Event]{Type: pubsub.UpdatedEvent, Payload: branchsummaries.Event{
		SessionID: "sess-1", RunID: "run-1", Agent: "pr-summarizer", State: state, SummaryID: summaryID, Err: err,
	}})
	if !ok {
		panic("event not handled")
	}
	return b.run(cmd)
}

func (b *branchSummaryFixture) runPicked() {
	b.pick("pr-summarizer", "")
	b.event(branchsummaries.StateRunning, "", nil)
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
}

func TestBranchSummary_StaleOfferIsDropped(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	offer := b.f.Observe(navigateTreeDoneMsg{
		session: &session.Session{ID: "sess-1"}, leafID: "a1",
		snapshot: &branchReturnSnapshot{sessionID: "sess-1", leafID: "a2"},
	})

	b.f.Observe(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "u3", SessionID: "sess-1", Role: message.User, ParentMessageID: "a1",
	}})
	b.run(offer)

	require.False(t, b.host.overlay.HasDialogs(), "a send after the rewind makes its offer stale")
}

func TestBranchSummary_PromptSetting(t *testing.T) {
	t.Parallel()

	t.Run("never", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.ws.cfg.Options.BranchSummary = &config.BranchSummaryOptions{Prompt: config.BranchSummaryNever}

		b.rewind("a2", "a1")

		require.False(t, b.host.overlay.HasDialogs())
		require.Empty(t, b.ws.starts)
	})
	t.Run("always", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.ws.cfg.Options.BranchSummary = &config.BranchSummaryOptions{Prompt: config.BranchSummaryAlways}

		b.rewind("a2", "a1")

		require.False(t, b.host.overlay.HasDialogs())
		require.Len(t, b.ws.starts, 1)
		require.Equal(t, agent.BuiltinBranchSummarizer, b.ws.starts[0].Agent)
	})
}

func TestBranchSummary_PickStartsTheSummarizer(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)

	_, ok := b.f.HandleAction(dialog.ActionBranchSummaryPick{
		SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer", Focus: "the PR",
	})

	require.True(t, ok)
	require.Equal(t, []agent.BranchSummaryRequest{{
		SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer", Focus: "the PR",
	}}, b.ws.starts, "started synchronously, so the session is reserved before any send")
	require.True(t, b.f.Busy("sess-1"))
}

func TestBranchSummary_StartError(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.ws.startErr = branchsummaries.ErrAlreadyRunning

	cmd, _ := b.f.HandleAction(dialog.ActionBranchSummaryPick{SessionID: "sess-1", FromLeafID: "a2", TargetID: "a1", Agent: "pr-summarizer"})

	infos := b.run(cmd)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeError, infos[0].Type)
}

func TestBranchSummary_RunningShowsAPanelUntilStored(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	panel := b.f.Panel()
	require.NotNil(t, panel)
	require.Contains(t, ansi.Strip(panel.Render(120)), "pr-summarizer")

	b.event(branchsummaries.StateStored, "summary-1", nil)
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

	require.Empty(t, b.ws.starts)
}

func TestBranchSummary_RunsAreKeptPerSession(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	b.host.session = &session.Session{ID: "sess-2"}
	require.Nil(t, b.f.Panel())
	require.Empty(t, b.f.PaletteEntries())

	b.host.session = &session.Session{ID: "sess-1"}
	require.NotNil(t, b.f.Panel())
}

func TestBranchSummary_PaletteCancelsARunningSummary(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	cmd, ok := b.f.HandleAction(entries[0].Action())
	require.True(t, ok)
	b.run(cmd)

	require.Equal(t, []string{"sess-1"}, b.ws.cancelled)
	infos := b.event(branchsummaries.StateCancelled, "", nil)
	require.Len(t, infos, 1)
	require.Nil(t, b.f.Panel())
}

func TestBranchSummary_FailureShowsUntilDismissed(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	b.event(branchsummaries.StateFailed, "", errors.New("provider exploded"))

	panel := b.f.Panel()
	require.NotNil(t, panel)
	require.Contains(t, ansi.Strip(panel.Render(120)), "provider exploded")
	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	cmd, ok := b.f.HandleAction(entries[0].Action())
	require.True(t, ok)
	b.run(cmd)
	require.Nil(t, b.f.Panel())
	require.Empty(t, b.ws.discarded)
}

func TestBranchSummary_StaleEndIsQuiet(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	infos := b.event(branchsummaries.StateStale, "", nil)

	require.Empty(t, infos)
	require.Nil(t, b.f.Panel())
	require.Empty(t, b.f.PaletteEntries())
}

func TestBranchSummary_DiscardBeforeSend(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()
	b.event(branchsummaries.StateStored, "summary-1", nil)

	entries := b.f.PaletteEntries()
	require.Len(t, entries, 1)
	cmd, ok := b.f.HandleAction(entries[0].Action())
	require.True(t, ok)
	b.run(cmd)

	require.Equal(t, []string{"summary-1"}, b.ws.discarded)
	require.Empty(t, b.f.PaletteEntries())
}

func TestBranchSummary_SendRetiresDiscard(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()
	b.event(branchsummaries.StateStored, "summary-1", nil)

	b.f.Observe(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "u3", SessionID: "sess-1", Role: message.User, ParentMessageID: "summary-1",
	}})

	require.Empty(t, b.f.PaletteEntries())
}

func TestBranchSummary_ReturnToPreBranch(t *testing.T) {
	t.Parallel()

	returnMsg := navigateTreeDoneMsg{
		session: &session.Session{ID: "sess-1"},
		leafID:  "a2",
		restore: &branchReturnSnapshot{sessionID: "sess-1", leafID: "a2"},
	}
	t.Run("discards a stored summary", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.runPicked()
		b.event(branchsummaries.StateStored, "summary-1", nil)

		b.run(b.f.Observe(returnMsg))

		require.Equal(t, []string{"summary-1"}, b.ws.discarded)
	})
	t.Run("cancels a running summary", func(t *testing.T) {
		t.Parallel()
		b := newBranchSummaryFixture(t)
		b.runPicked()

		b.run(b.f.Observe(returnMsg))

		require.Equal(t, []string{"sess-1"}, b.ws.cancelled)
	})
}

func TestBranchSummary_SendSeenBeforeStoredRetiresDiscard(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	b.f.Observe(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "u3", SessionID: "sess-1", Role: message.User, ParentMessageID: "summary-1",
	}})
	b.event(branchsummaries.StateStored, "summary-1", nil)

	require.Empty(t, b.f.PaletteEntries())
}

func TestBranchSummary_TheSummaryItselfDoesNotRetireDiscard(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	b.runPicked()

	b.f.Observe(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "summary-1", SessionID: "sess-1", Role: message.User, MessageType: message.MessageTypeBranchSummary, ParentMessageID: "a1",
	}})
	b.event(branchsummaries.StateStored, "summary-1", nil)

	require.Len(t, b.f.PaletteEntries(), 1)
}

func TestBranchSummary_HoldsMCPTogglesWhileRunning(t *testing.T) {
	t.Parallel()
	b := newBranchSummaryFixture(t)
	toggle := dialog.ActionToggleLazyMCP{ServerName: "linear", Enabled: true}

	_, claimed := b.f.HandleAction(toggle)
	require.False(t, claimed, "toggles pass through when no summary runs")

	b.runPicked()
	cmd, claimed := b.f.HandleAction(toggle)
	require.True(t, claimed, "a toggle would move the leaf off the summary's target")
	infos := b.run(cmd)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
}

func TestBranchSummary_HoldsModelAndReasoningChangesWhileRunning(t *testing.T) {
	t.Parallel()
	actions := map[string]dialog.Action{
		"model":     dialog.ActionSelectModel{},
		"reasoning": dialog.ActionSelectReasoningEffort{Effort: "high"},
	}
	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b := newBranchSummaryFixture(t)
			b.ws.running["sess-1"] = true

			_, claimed := b.f.HandleAction(action)

			require.True(t, claimed, "the change writes metadata under the summary's target")
		})
	}
}
