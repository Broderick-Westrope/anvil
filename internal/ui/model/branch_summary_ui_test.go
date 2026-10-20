package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// branchSummaryUI is a UI whose selected user message has a reply, so
// branching from it leaves something to summarize.
func branchSummaryUI(t *testing.T) *UI {
	t.Helper()
	m, f, source := branchUI(t)
	reply, err := f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{
		ParentMessageID: source.ID, Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "answer"}},
	})
	require.NoError(t, err)
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, m.session.ID, reply.ID))
	m.session.LeafMessageID = reply.ID
	m.branchSummary = newBranchSummaryFeature(uiHost{m}, m.com, m.com.Workspace)
	m.features.register(m.branchSummary)
	return m
}

func TestRewindOffersBranchSummary(t *testing.T) {
	m := branchSummaryUI(t)

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
	m := branchSummaryUI(t)
	m.branchSummary.op = &branchSummaryOp{sessionID: m.session.ID, cancel: func() {}}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})

	msgs := collectMsgs(cmd)
	_, navigated := findMsg[navigateTreeDoneMsg](msgs)
	require.False(t, navigated)
	info, ok := findMsg[util.InfoMsg](msgs)
	require.True(t, ok)
	require.Contains(t, info.Msg, "branch summary")
}

func TestEscCancelsARunningBranchSummaryBeforeReturning(t *testing.T) {
	m := branchSummaryUI(t)
	cancelled := false
	m.branchSummary.op = &branchSummaryOp{sessionID: m.session.ID, cancel: func() { cancelled = true }}
	m.pendingBranch = &branchReturnSnapshot{sessionID: m.session.ID, leafID: m.session.LeafMessageID}
	m.focus = uiFocusEditor

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	require.True(t, cancelled)
	_, navigated := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.False(t, navigated)
	require.NotNil(t, m.pendingBranch, "a second esc still returns")
}
