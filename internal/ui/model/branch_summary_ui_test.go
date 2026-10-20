package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

// runningSummaryWorkspace is the real workspace, except that it reports a
// branch summary running in the sessions it lists and records cancels.
type runningSummaryWorkspace struct {
	branchSummaryWorkspace
	running   map[string]bool
	cancelled []string
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
	ws := &runningSummaryWorkspace{branchSummaryWorkspace: m.com.Workspace, running: map[string]bool{}}
	m.branchSummary = newBranchSummaryFeature(uiHost{m}, m.com, ws)
	m.features.register(m.branchSummary)
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
