package model

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

type composerSnapshot struct {
	text        string
	attachments []message.Attachment
	skills      []attachments.SkillAttachment
}

func (c composerSnapshot) isEmpty() bool {
	return c.text == "" && len(c.attachments) == 0 && len(c.skills) == 0
}

type historySnapshot struct {
	messages []composerSnapshot
	index    int
	draft    composerSnapshot
}

type branchReturnSnapshot struct {
	sessionID        string
	leafID           string
	originalDraft    composerSnapshot
	originalHistory  historySnapshot
	originalViewport branchViewport
}

func (m *UI) captureBranchSnapshot() *branchReturnSnapshot {
	history := m.promptHistory
	history.messages = append([]composerSnapshot(nil), history.messages...)
	for i := range history.messages {
		history.messages[i] = history.messages[i].clone()
	}
	history.draft = history.draft.clone()
	return &branchReturnSnapshot{
		sessionID:        m.session.ID,
		originalDraft:    m.captureComposer(),
		originalHistory:  history,
		originalViewport: m.chat.branchViewport(),
	}
}

func (m *UI) branchFromSelectedMessage() tea.Cmd {
	if !m.hasSession() || m.session.ParentSessionID != "" || m.isDrilledIn() {
		return util.ReportWarn("Branching is only available in the root chat.")
	}
	provider, ok := m.chat.SelectedItem().(chat.SourceMessageProvider)
	if !ok {
		return util.ReportWarn("Select a user or assistant message to branch from.")
	}
	src := provider.SourceMessage()
	if src.Role != message.User && src.Role != message.Assistant {
		return nil
	}
	return m.handleNavigateTree(dialog.ActionNavigateTree{
		MessageID: src.ID, ParentMessageID: src.ParentMessageID,
		Role: src.Role, Source: src,
	})
}

func (m *UI) beginBranchReturn(pending bool) tea.Cmd {
	if m.branchNavigation != nil || m.branchRestoring {
		return util.ReportWarn("Please wait for navigation to finish.")
	}
	snapshot := m.branchReturn
	if pending {
		snapshot = m.pendingBranch
	} else if m.textarea.Value() != "" || m.attachments.HasContent() {
		return util.ReportWarn("Clear the composer before returning to the pre-branch conversation.")
	}
	if snapshot == nil || !m.hasSession() || snapshot.sessionID != m.session.ID {
		return nil
	}
	m.branchRestoring = true
	m.branchRestoreSnapshot = snapshot
	return m.cancelThenNavigate(dialog.ActionNavigateTree{MessageID: snapshot.leafID})
}

func (m *UI) restoreBranchDraft(snapshot *branchReturnSnapshot) tea.Cmd {
	prevHeight := m.textarea.Height()
	m.restoreComposer(snapshot.originalDraft)
	m.promptHistory.messages = snapshot.originalHistory.messages
	m.promptHistory.index = snapshot.originalHistory.index
	m.promptHistory.draft = snapshot.originalHistory.draft
	m.focus = uiFocusEditor
	heightCmd := m.handleTextareaHeightChange(prevHeight)
	m.updateLayoutAndSize()
	m.chat.restoreBranchViewport(snapshot.originalViewport)
	return tea.Batch(m.textarea.Focus(), heightCmd)
}

func (m *UI) clearBranchState() {
	m.pendingBranch = nil
	m.branchReturn = nil
	m.branchNavigation = nil
	m.branchRestoreSnapshot = nil
	m.branchRestoring = false
}

type treeNavErrorMsg struct {
	err       error
	snapshot  *branchReturnSnapshot
	movedLeaf bool
}

func (m *UI) handleTreeNavError(msg treeNavErrorMsg) tea.Cmd {
	m.navigating = false
	if msg.movedLeaf && !m.branchRestoring && msg.snapshot != nil {
		m.pendingBranch = msg.snapshot
	}
	m.branchNavigation = nil
	m.branchRestoring = false
	m.branchRestoreSnapshot = nil
	return util.ReportError(msg.err)
}

func (m *UI) loadTreePoint(nav dialog.ActionNavigateTree, targetLeafID string) tea.Cmd {
	ws := m.com.Workspace
	sessionID := m.session.ID
	snapshot := m.branchNavigation
	restore := m.branchRestoreSnapshot
	return func() tea.Msg {
		ctx := context.Background()
		if snapshot != nil {
			source, err := ws.GetSession(ctx, sessionID)
			if err != nil {
				return treeNavErrorMsg{err: err}
			}
			saved := *snapshot
			saved.leafID = source.LeafMessageID
			snapshot = &saved
		}
		if err := ws.MoveLeaf(ctx, sessionID, targetLeafID); err != nil {
			return treeNavErrorMsg{err: err, snapshot: snapshot}
		}
		fail := func(err error) tea.Msg {
			return treeNavErrorMsg{err: err, snapshot: snapshot, movedLeaf: true}
		}
		sess, err := ws.GetSession(ctx, sessionID)
		if err != nil {
			return fail(err)
		}
		var msgs []message.Message
		if targetLeafID != "" {
			msgs, err = ws.GetBranchPath(ctx, targetLeafID)
			if err != nil {
				return fail(err)
			}
		}
		nested, err := readBranchNested(ctx, ws, msgs)
		if err != nil {
			return fail(err)
		}
		return navigateTreeDoneMsg{
			session: &sess, leafID: targetLeafID, messages: msgs, nested: nested,
			source: nav.Source, role: nav.Role, snapshot: snapshot, restore: restore,
		}
	}
}
