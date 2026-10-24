package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

// runningSummaryWorkspace is the real workspace, except that it reports a
// branch summary running in the sessions it lists, and records starts,
// which run nothing, and cancels.
type runningSummaryWorkspace struct {
	branchSummaryWorkspace
	running   map[string]bool
	starts    []agent.BranchSummaryRequest
	cancelled []string
	fixture   *branchfixture.Fixture
}

func (w *runningSummaryWorkspace) AgentStartBranchSummary(req agent.BranchSummaryRequest) (string, error) {
	w.starts = append(w.starts, req)
	w.running[req.SessionID] = true
	return "run-1", nil
}

func (w *runningSummaryWorkspace) AgentBranchSummaryRunning(sessionID string) bool {
	return w.running[sessionID]
}

func (w *runningSummaryWorkspace) AgentCancelBranchSummary(sessionID string) {
	w.cancelled = append(w.cancelled, sessionID)
}

// summarizerBusyWorkspace reports the app busy, as it is while a
// summarizer runs in its child session, but not the root session.
type summarizerBusyWorkspace struct {
	workspace.Workspace
}

func (summarizerBusyWorkspace) AgentIsBusy() bool { return true }

// branchSummaryUI is a UI whose selected user message has a reply, so
// branching from it leaves something to summarize.
func branchSummaryUI(t *testing.T) (*UI, *runningSummaryWorkspace) {
	t.Helper()
	m, f, source := branchUI(t)
	reply, err := f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{
		ParentMessageID: source.ID, Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "answer"}},
	})
	require.NoError(t, err)
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, m.session.ID, reply.ID))
	m.session.LeafMessageID = reply.ID
	ws := &runningSummaryWorkspace{branchSummaryWorkspace: m.com.Workspace, running: map[string]bool{}, fixture: f}
	m.features.register(newBranchSummaryFeature(uiHost{m}, m.com, ws))
	return m, ws
}

func TestRewindOffersBranchSummary(t *testing.T) {
	m, _ := branchSummaryUI(t)

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	done, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok)
	_, cmd = m.Update(done)
	offer, ok := findMsg[branchSummaryOfferMsg](collectMsgs(cmd))
	require.True(t, ok, "a rewind past an assistant reply offers a summary")
	m.Update(offer)

	require.True(t, m.dialog.ContainsDialog(dialog.BranchSummaryPickerID))
}

func TestRewindWaitsForARunningBranchSummary(t *testing.T) {
	m, ws := branchSummaryUI(t)
	ws.running[m.session.ID] = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})

	msgs := collectMsgs(cmd)
	_, navigated := findMsg[navigateTreeDoneMsg](msgs)
	require.False(t, navigated)
	info, ok := findMsg[util.InfoMsg](msgs)
	require.True(t, ok)
	require.Contains(t, info.Msg, "branch summary")
}

func TestCancelKeysReachARunningBranchSummaryFirst(t *testing.T) {
	keys := map[string]tea.KeyPressMsg{
		"esc":    {Code: tea.KeyEscape},
		"ctrl+c": {Code: 'c', Mod: tea.ModCtrl},
	}
	for name, k := range keys {
		t.Run(name, func(t *testing.T) {
			m, ws := branchSummaryUI(t)
			ws.running[m.session.ID] = true
			m.com.Workspace = summarizerBusyWorkspace{m.com.Workspace}
			m.pendingBranch = &branchReturnSnapshot{sessionID: m.session.ID, leafID: m.session.LeafMessageID}
			m.focus = uiFocusEditor

			_, cmd := m.Update(k)
			msgs := collectMsgs(cmd)

			require.Equal(t, []string{m.session.ID}, ws.cancelled)
			_, navigated := findMsg[navigateTreeDoneMsg](msgs)
			require.False(t, navigated)
			require.False(t, m.dialog.ContainsDialog(dialog.QuitID))
			require.NotNil(t, m.pendingBranch, "a second esc still returns")
		})
	}
}

func TestMetadataEntriesFollowTheStoredLeaf(t *testing.T) {
	m, f, source := branchUI(t)
	summary, err := f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{
		ParentMessageID: source.ID, Role: message.User, MessageType: message.MessageTypeBranchSummary,
		Parts: []message.ContentPart{message.BranchSummaryContent{Summary: "s"}},
	})
	require.NoError(t, err)

	// The UI's cached leaf is still the source when the toggle is written;
	// the entry goes on the stored leaf regardless.
	require.NoError(t, f.Workspace.WriteMetadataEntry(f.Context, m.session.ID, message.CreateMessageParams{
		MessageType: message.MessageTypeMCPToggle,
		Parts:       []message.ContentPart{message.MCPToggleContent{ServerName: "linear", Enabled: true}},
	}))

	children, err := f.Messages.GetChildren(f.Context, summary.ID)
	require.NoError(t, err)
	require.Len(t, children, 1)
}

func TestEscOnTheSummaryPickerCancelsTheBranch(t *testing.T) {
	m, _ := branchSummaryUI(t)
	before := m.session.LeafMessageID

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	done, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok)
	_, cmd = m.Update(done)
	offer, ok := findMsg[branchSummaryOfferMsg](collectMsgs(cmd))
	require.True(t, ok)
	m.Update(offer)
	require.True(t, m.dialog.ContainsDialog(dialog.BranchSummaryPickerID))
	require.NotEqual(t, before, m.session.LeafMessageID)

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	back, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok, "esc returns to the pre-branch conversation")
	m.Update(back)

	require.False(t, m.dialog.ContainsDialog(dialog.BranchSummaryPickerID))
	require.Equal(t, before, m.session.LeafMessageID)
	require.Empty(t, m.textarea.Value(), "the prefilled message is put back as it was")
}

// startBranchSummary rewinds past the reply and picks the first
// summarizer, returning the request it started.
func startBranchSummary(t *testing.T, m *UI, ws *runningSummaryWorkspace) agent.BranchSummaryRequest {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	done, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok)
	_, cmd = m.Update(done)
	offer, ok := findMsg[branchSummaryOfferMsg](collectMsgs(cmd))
	require.True(t, ok)
	m.Update(offer)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Len(t, ws.starts, 1)
	return ws.starts[0]
}

func TestBranchSummarizerMessagesReachItsLiveItem(t *testing.T) {
	m, ws := branchSummaryUI(t)
	req := startBranchSummary(t, m, ws)
	_, toolCallID, ok := session.ParseAgentToolSessionID(req.ChildSessionID)
	require.True(t, ok)
	item, ok := m.chat.MessageItem(toolCallID).(*chat.BranchSummaryItem)
	require.True(t, ok, "the summarizer shows as it runs")

	m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "child-msg", SessionID: req.ChildSessionID, Role: message.Assistant,
		Parts: []message.ContentPart{message.ToolCall{ID: "grep-1", Name: "grep", Input: `{"pattern":"TODO"}`, Finished: true}},
	}})

	require.Len(t, item.NestedTools(), 1)
	require.Equal(t, "grep-1", item.NestedTools()[0].ID())
	require.Equal(t, req.ChildSessionID, item.AgentDrillIn())
	turns, tools := item.Stats()
	require.Equal(t, 1, turns)
	require.Equal(t, 1, tools)
}

func TestStoredBranchSummaryReplacesItsLiveItemWithItsTools(t *testing.T) {
	m, ws := branchSummaryUI(t)
	f := ws.fixture
	req := startBranchSummary(t, m, ws)
	_, toolCallID, _ := session.ParseAgentToolSessionID(req.ChildSessionID)
	_, err := f.Sessions.CreateTaskSession(f.Context, req.ChildSessionID, req.SessionID, "Branch summary")
	require.NoError(t, err)
	_, err = f.Messages.Create(f.Context, req.ChildSessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.ToolCall{ID: "grep-1", Name: "grep", Input: `{"pattern":"TODO"}`, Finished: true}},
	})
	require.NoError(t, err)
	live := m.chat.MessageItem(toolCallID)
	before := m.chat.Len()

	m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: message.Message{
		ID: "summary-1", SessionID: req.SessionID, Role: message.User, MessageType: message.MessageTypeBranchSummary,
		ParentMessageID: req.TargetID,
		Parts: []message.ContentPart{message.BranchSummaryContent{
			Summary: "## Outcome\nDone.", ChildSessionID: req.ChildSessionID, Agent: req.Agent,
		}},
	}})

	stored, ok := m.chat.MessageItem(toolCallID).(*chat.BranchSummaryItem)
	require.True(t, ok)
	require.NotSame(t, live, stored)
	require.Equal(t, before, m.chat.Len(), "the stored item takes the live one's place")
	require.Equal(t, chat.ToolStatusSuccess, stored.Status())
	require.Len(t, stored.NestedTools(), 1, "its tools load as a loaded session's would")
	require.Equal(t, req.ChildSessionID, stored.AgentDrillIn())

	m.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: message.Message{
		ID: "summary-1", SessionID: req.SessionID, Role: message.User, MessageType: message.MessageTypeBranchSummary,
		Parts: []message.ContentPart{message.BranchSummaryContent{ChildSessionID: req.ChildSessionID}},
	}})

	require.Nil(t, m.chat.MessageItem(toolCallID), "discarding removes it")
}
