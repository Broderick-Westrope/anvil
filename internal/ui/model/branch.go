package model

import (
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

type branchViewport struct {
	selectedID    string
	selectedIndex int
	offset        int
	follow        bool
}

func (m *Chat) branchViewport() branchViewport {
	s := branchViewport{selectedIndex: m.list.Selected(), offset: m.list.Offset(), follow: m.follow}
	if item, ok := m.SelectedItem().(chat.MessageItem); ok {
		s.selectedID = item.ID()
	}
	return s
}

func (m *Chat) restoreBranchViewport(s branchViewport) {
	index := s.selectedIndex
	if i, ok := m.idInxMap[s.selectedID]; ok {
		index = i
	}
	m.SetSelected(index)
	m.list.ScrollToTop()
	m.list.ScrollBy(s.offset)
	m.follow = s.follow
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

func (m *UI) clearBranchState() {
	m.pendingBranch = nil
	m.branchReturn = nil
	m.branchNavigation = nil
	m.branchRestoreSnapshot = nil
	m.branchRestoring = false
}
