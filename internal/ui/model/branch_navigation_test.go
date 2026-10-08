package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/completions"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

func TestBranchKeyNavigatesImmediately(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	parent := seedUserSource(t, f, sess.ID, "parent")
	source := seedUserSource(t, f, sess.ID, "hello\n  world")
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, sess.ID, source.ID))
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.textarea.SetValue("original draft")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	results := collectMsgs(cmd)
	done, ok := findMsg[navigateTreeDoneMsg](results)
	require.True(t, ok, "B must navigate immediately, not preview")
	m.Update(done)
	require.Equal(t, parent.ID, m.session.LeafMessageID)
	require.Equal(t, "hello\n  world", m.textarea.Value())
	require.Equal(t, uiFocusEditor, m.focus)
	require.NotContains(t, m.renderEditorView(80), "later messages")
}

func branchUI(t *testing.T) (*UI, *branchfixture.Fixture, message.Message) {
	t.Helper()
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	return m, f, source
}

func completeNavigation(t *testing.T, m *UI, cmd tea.Cmd) navigateTreeDoneMsg {
	t.Helper()
	done, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok)
	m.Update(done)
	return done
}

func pressBranch(t *testing.T, m *UI) {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	completeNavigation(t, m, cmd)
}

func TestBranchAssistantKeepsComposer(t *testing.T) {
	m, f, source := branchUI(t)
	assistant, err := f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{
		ParentMessageID: source.ID, Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "answer"}},
	})
	require.NoError(t, err)
	seedUserSource(t, f, m.session.ID, "later")
	m.chat.SetMessages(chat.NewAssistantMessageItem(m.com.Styles, &assistant))
	m.chat.SelectLast()
	m.textarea.SetValue("keep this")
	file := message.Attachment{FileName: "draft.txt", Content: []byte("draft")}
	skill := attachments.SkillAttachment{Name: "draft", Instructions: "instructions"}
	m.attachments.Update(file)
	m.attachments.Update(skill)
	pressBranch(t, m)
	require.Equal(t, assistant.ID, m.session.LeafMessageID)
	require.Equal(t, "keep this", m.textarea.Value())
	require.Equal(t, []message.Attachment{file}, m.attachments.List())
	require.Equal(t, []attachments.SkillAttachment{skill}, m.attachments.SkillList())
	require.Equal(t, uiFocusEditor, m.focus)
}

func TestBranchEscapeRestoresFullDraft(t *testing.T) {
	m, f, source := branchUI(t)
	leaf := seedUserSource(t, f, m.session.ID, "exact source leaf")
	m.textarea.SetValue("draft\ntext")
	bytes := []byte("original bytes")
	m.attachments.Update(message.Attachment{FileName: "draft.txt", MimeType: "text/plain", Content: bytes})
	skill := attachments.SkillAttachment{Name: "skill", Instructions: "instructions"}
	m.attachments.Update(skill)
	m.promptHistory.messages = []composerSnapshot{{text: "one"}, {text: "two"}}
	m.promptHistory.index = 1
	m.promptHistory.draft = composerSnapshot{text: "history draft"}
	path, err := f.Workspace.GetBranchPath(f.Context, leaf.ID)
	require.NoError(t, err)
	m.width, m.height = 100, 40
	m.updateLayoutAndSize()
	m.setSessionMessages(path)
	m.chat.SetSelected(0)
	m.chat.SetFollow(false)
	viewport := m.chat.branchViewport()
	pressBranch(t, m)
	require.NotNil(t, m.pendingBranch)
	require.Empty(t, m.session.LeafMessageID)
	require.Equal(t, leaf.ID, m.pendingBranch.leafID)
	require.NotEqual(t, source.ID, m.pendingBranch.leafID)
	m.attachments.Update(message.Attachment{FileName: "replacement.txt", Content: []byte("replacement")})
	m.textarea.SetValue("edited")
	m.attachments.Reset()
	require.Contains(t, fmt.Sprint(m.ShortHelp()), "return to previous branch")
	require.Contains(t, fmt.Sprint(m.FullHelp()), "return to previous branch")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	completeNavigation(t, m, cmd)
	require.Nil(t, m.pendingBranch)
	require.Equal(t, leaf.ID, m.session.LeafMessageID)
	require.Equal(t, "draft\ntext", m.textarea.Value())
	require.Equal(t, []byte("original bytes"), m.attachments.List()[0].Content)
	require.Equal(t, []attachments.SkillAttachment{skill}, m.attachments.SkillList())
	require.Equal(t, []composerSnapshot{{text: "one"}, {text: "two"}}, m.promptHistory.messages)
	require.Equal(t, 1, m.promptHistory.index)
	require.Equal(t, composerSnapshot{text: "history draft"}, m.promptHistory.draft)
	require.Equal(t, viewport, m.chat.branchViewport())
	saved, err := f.Workspace.GetSession(f.Context, m.session.ID)
	require.NoError(t, err)
	require.Equal(t, leaf.ID, saved.LeafMessageID)
}

func TestBranchDialogEntryPoints(t *testing.T) {
	for _, id := range []string{dialog.TreeID, dialog.BranchID} {
		t.Run(id, func(t *testing.T) {
			m, _, source := branchUI(t)
			m.textarea.SetValue("saved draft")
			require.Nil(t, m.openDialog(id))
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			completeNavigation(t, m, cmd)
			require.NotNil(t, m.pendingBranch)
			require.Equal(t, source.ID, m.pendingBranch.leafID)
			require.Equal(t, "saved draft", m.pendingBranch.originalDraft.text)
		})
	}
}

type navigationWorkspace struct {
	workspace.Workspace
	busy     bool
	cancels  int
	reads    int
	moves    int
	failRead bool
	failMove bool
}

func (w *navigationWorkspace) AgentIsBusy() bool              { return w.busy }
func (w *navigationWorkspace) AgentIsSessionBusy(string) bool { return w.busy }
func (w *navigationWorkspace) AgentCancel(string)             { w.cancels++ }
func (w *navigationWorkspace) GetSession(ctx context.Context, id string) (session.Session, error) {
	w.reads++
	if w.failRead {
		return session.Session{}, errors.New("read failed")
	}
	return w.Workspace.GetSession(ctx, id)
}

func (w *navigationWorkspace) MoveLeaf(ctx context.Context, id, leaf string) error {
	w.moves++
	if w.failMove {
		return errors.New("move failed")
	}
	return w.Workspace.MoveLeaf(ctx, id, leaf)
}

func TestBranchBusyCancelsAndCapturesPersistedLeafAfterIdle(t *testing.T) {
	m, f, source := branchUI(t)
	ws := &navigationWorkspace{Workspace: f.Workspace, busy: true}
	m.com.Workspace = ws
	m.textarea.SetValue("choice draft")
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.NotNil(t, cmd)
	require.Equal(t, 1, ws.cancels)
	require.Zero(t, ws.reads)
	require.Zero(t, ws.moves)
	m.textarea.SetValue("later edit")
	_, cmd = m.Update(checkAgentIdleMsg{sessionID: m.session.ID})
	require.NotNil(t, cmd)
	require.Zero(t, ws.moves)
	leaf := seedUserSource(t, f, m.session.ID, "cancel persisted leaf")
	ws.busy = false
	_, cmd = m.Update(checkAgentIdleMsg{sessionID: m.session.ID, nav: dialog.ActionNavigateTree{MessageID: source.ID, Role: message.User}})
	completeNavigation(t, m, cmd)
	require.Equal(t, leaf.ID, m.pendingBranch.leafID)
	require.Equal(t, "choice draft", m.pendingBranch.originalDraft.text)
}

func TestBranchEscapePrecedence(t *testing.T) {
	for _, mode := range []string{"dialog", "completions", "slash", "attachments"} {
		t.Run(mode, func(t *testing.T) {
			m, _, _ := branchUI(t)
			pressBranch(t, m)
			switch mode {
			case "dialog":
				m.dialog.OpenDialog(dialog.NewQuit(m.com))
			case "completions":
				m.completions = completions.New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
				m.completions.SetItems([]completions.FileCompletionValue{{Path: "file"}}, nil)
				m.completionsOpen = true
			case "slash":
				m.slashAC = autocomplete.New(nil, 10)
				m.slashACOpen = true
			case "attachments":
				m.attachments.Update(message.Attachment{FileName: "file"})
				m.attachments.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
				require.True(t, m.attachments.IsDeleting())
			}
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			require.NotNil(t, m.pendingBranch)
			require.False(t, m.branchRestoring)
			for _, result := range collectMsgs(cmd) {
				_, navigated := result.(navigateTreeDoneMsg)
				require.False(t, navigated)
			}
		})
	}
}

func executeSend(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	result := cmd()
	if batch, ok := result.(tea.BatchMsg); ok {
		for _, child := range batch {
			executeSend(child)
		}
	}
}

func TestBranchDoubleEnterAndPaletteReturn(t *testing.T) {
	m, f, source := branchUI(t)
	m.textarea.SetValue("original draft")
	pressBranch(t, m)
	m.textarea.SetValue("branch prompt")
	_, first := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Nil(t, m.pendingBranch)
	require.NotNil(t, m.branchReturn)
	_, second := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	executeSend(first)
	executeSend(second)
	msgs, err := f.Workspace.GetAllSessionMessages(f.Context, m.session.ID)
	require.NoError(t, err)
	count := 0
	for _, msg := range msgs {
		if msg.Role == message.User && msg.Content().Text == "branch prompt" {
			count++
		}
	}
	require.Equal(t, 1, count)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	require.True(t, m.dialog.ContainsDialog(dialog.CommandsID))
	m.Update(tea.KeyPressMsg{Code: 'R', Text: "Return to pre-branch conversation"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	completeNavigation(t, m, cmd)
	require.Nil(t, m.branchReturn)
	require.Equal(t, source.ID, m.session.LeafMessageID)
	require.Equal(t, "original draft", m.textarea.Value())
}

func TestBranchReturnConflictsAndFailures(t *testing.T) {
	m, f, _ := branchUI(t)
	m.textarea.SetValue("saved")
	pressBranch(t, m)
	m.branchReturn, m.pendingBranch = m.pendingBranch, nil
	ws := &navigationWorkspace{Workspace: f.Workspace}
	m.com.Workspace = ws
	for _, dirty := range []string{"text", "files", "skills"} {
		m.textarea.Reset()
		m.attachments.Reset()
		switch dirty {
		case "text":
			m.textarea.SetValue("conflict")
		case "files":
			m.attachments.Update(message.Attachment{FileName: "file"})
		case "skills":
			m.attachments.Update(attachments.SkillAttachment{Name: "skill"})
		}
		info := m.beginBranchReturn(false)().(util.InfoMsg)
		require.Equal(t, "Clear the composer before returning to the pre-branch conversation.", info.Msg)
		require.Zero(t, ws.reads)
		require.Zero(t, ws.moves)
	}
	m.textarea.Reset()
	m.attachments.Reset()
	for _, fail := range []string{"move", "read"} {
		ws.failMove = fail == "move"
		ws.failRead = fail == "read"
		cmd := m.beginBranchReturn(false)
		failure := cmd().(navigateTreeDoneMsg)
		m.Update(failure)
		require.NotNil(t, m.branchReturn)
	}
	ws.failMove, ws.failRead = false, false
	ws.busy = true
	require.NotNil(t, m.beginBranchReturn(false))
	require.Equal(t, 1, ws.cancels)
	ws.busy = false
	_, cmd := m.Update(checkAgentIdleMsg{sessionID: m.session.ID, nav: dialog.ActionNavigateTree{MessageID: m.branchReturn.leafID}})
	completeNavigation(t, m, cmd)
	require.Nil(t, m.branchReturn)
}

func TestBranchProtectsSavedDraft(t *testing.T) {
	m, _, source := branchUI(t)
	m.textarea.SetValue("saved")
	pressBranch(t, m)
	m.branchReturn, m.pendingBranch = m.pendingBranch, nil
	info := m.handleNavigateTree(dialog.ActionNavigateTree{MessageID: source.ID})().(util.InfoMsg)
	require.Contains(t, info.Msg, "Return to pre-branch conversation")
	require.Nil(t, m.branchNavigation)
	m.branchReturn.originalDraft = composerSnapshot{}
	completeNavigation(t, m, m.handleNavigateTree(dialog.ActionNavigateTree{MessageID: source.ID}))
	require.NotNil(t, m.pendingBranch)
}

func TestBranchSlashCommandIsNotSent(t *testing.T) {
	m, _, _ := branchUI(t)
	pressBranch(t, m)
	m.textarea.SetValue("/tree")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	executeSend(cmd)
	require.NotNil(t, m.pendingBranch)
	require.Nil(t, m.branchReturn)
	require.Empty(t, m.textarea.Value())
	require.True(t, m.dialog.ContainsDialog(dialog.TreeID))
}

func TestBranchLowercaseBPages(t *testing.T) {
	m, _, source := branchUI(t)
	var items []chat.MessageItem
	for i := range 30 {
		msg := source
		msg.ID = fmt.Sprint(i)
		items = append(items, chat.NewUserMessageItem(m.com.Styles, &msg, m.attachments.Renderer()))
	}
	m.chat.SetMessages(items...)
	m.chat.SetSize(80, 10)
	m.chat.ScrollToBottom()
	before := m.chat.list.Offset()
	require.True(t, key.Matches(tea.KeyPressMsg{Code: 'b', Text: "b"}, m.keyMap.Chat.PageUp))
	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	require.Less(t, m.chat.list.Offset(), before)
	require.Nil(t, m.pendingBranch)
	require.Equal(t, uiFocusMain, m.focus)
}

func TestBranchNavigationAfterPendingUsesNormalTreeFlow(t *testing.T) {
	m, _, source := branchUI(t)
	pressBranch(t, m)
	m.textarea.SetValue("pending draft")
	require.Nil(t, m.openDialog(dialog.TreeID))
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	completeNavigation(t, m, cmd)
	require.Equal(t, source.Content().Text, m.textarea.Value())
	require.Equal(t, "pending draft", m.pendingBranch.originalDraft.text)
}

func TestBranchEscapeFailureRetainsSnapshot(t *testing.T) {
	m, f, _ := branchUI(t)
	m.textarea.SetValue("saved")
	pressBranch(t, m)
	snapshot := m.pendingBranch
	ws := &navigationWorkspace{Workspace: f.Workspace, failMove: true}
	m.com.Workspace = ws
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Zero(t, ws.moves)
	failure, ok := findMsg[navigateTreeDoneMsg](collectMsgs(cmd))
	require.True(t, ok)
	m.Update(failure)
	require.Same(t, snapshot, m.pendingBranch)
	ws.failMove = false
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	completeNavigation(t, m, cmd)
	require.Nil(t, m.pendingBranch)
	require.Equal(t, "saved", m.textarea.Value())
}

func TestBranchEscapeBusyDoesNotRestore(t *testing.T) {
	m, f, _ := branchUI(t)
	pressBranch(t, m)
	ws := &navigationWorkspace{Workspace: f.Workspace, busy: true}
	m.com.Workspace = ws
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.NotNil(t, m.pendingBranch)
	require.False(t, m.branchRestoring)
	require.Zero(t, ws.moves)
}
