package model

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func batchCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	b, ok := msg.(tea.BatchMsg)
	return []tea.Cmd(b), ok
}

func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	cmds, ok := batchCmds(msg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range cmds {
		if c == nil {
			continue
		}
		if m := c(); m != nil {
			out = append(out, m)
		}
	}
	return out
}

func findMsg[T any](msgs []tea.Msg) (T, bool) {
	var zero T
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	return zero, false
}

func newIntegrationUI(t *testing.T, f *branchfixture.Fixture, sessionID string, source *message.Message) *UI {
	t.Helper()
	com := common.DefaultCommon(f.Workspace)
	sess, err := f.Workspace.GetSession(f.Context, sessionID)
	require.NoError(t, err)

	m := &UI{
		state:           uiChat,
		dialog:          dialog.NewOverlay(),
		enabledLazyMCPs: make(map[string]bool),
		com:             com,
		keyMap:          DefaultKeyMap(),
		chat:            NewChat(com),
		focus:           uiFocusMain,
		session:         &sess,
	}
	m.textarea = textarea.New()
	m.attachments = attachments.New(
		attachments.NewRenderer(
			com.Styles.Attachments.Normal,
			com.Styles.Attachments.Deleting,
			com.Styles.Attachments.Image,
			com.Styles.Attachments.Text,
			com.Styles.Attachments.Skill,
			com.Styles.Attachments.Remove,
		),
		attachments.Keymap{
			DeleteMode: m.keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  m.keyMap.Editor.DeleteAllAttachments,
			Escape:     m.keyMap.Editor.Escape,
		},
	)
	m.chat.Focus()
	item := chat.NewUserMessageItem(com.Styles, source, m.attachments.Renderer())
	m.chat.SetMessages(item)
	m.chat.SelectLast()
	m.status = NewStatus(com, m)
	return m
}

func seedUserSource(t *testing.T, f *branchfixture.Fixture, sessionID, text string) message.Message {
	t.Helper()
	created, err := f.Messages.Create(f.Context, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	})
	require.NoError(t, err)
	return created
}

func driveAcceptedBranch(t *testing.T, m *UI) string {
	t.Helper()

	submitCmd := m.trySubmitBranch()
	require.NotNil(t, submitCmd)
	require.True(t, m.branchActive())
	require.True(t, m.branchLoading)

	msgs := collectMsgs(submitCmd)
	accepted, ok := findMsg[branchOutcomeMsg](msgs)
	require.True(t, ok, "accepted outcome must be observed")
	require.Equal(t, branchOutcomeAccepted, accepted.outcome.kind)

	acceptedMsgs := collectBranchAccepted(m.handleBranchOutcome(accepted))
	readResult, ok := findMsg[branchReadResultMsg](acceptedMsgs)
	require.True(t, ok, "acceptance must trigger an immediate read")
	require.NoError(t, readResult.err)
	require.Nil(t, m.handleBranchReadResult(readResult))
	require.False(t, m.branchLoading, "loading clears once the first snapshot installs")

	finished, ok := findMsg[branchOutcomeMsg](acceptedMsgs)
	require.True(t, ok, "finished outcome must also be observed")
	require.Equal(t, branchOutcomeFinished, finished.outcome.kind)
	require.NoError(t, finished.outcome.err)

	finishedMsgs := collectMsgs(m.handleBranchOutcome(finished))
	finalRead, ok := findMsg[branchReadResultMsg](finishedMsgs)
	require.True(t, ok, "finish must force a mandatory final read")
	require.NoError(t, finalRead.err)
	require.Nil(t, m.handleBranchReadResult(finalRead))

	require.False(t, m.branchActive(), "branch is reconciled after the mandatory post-finish read")
	require.NotNil(t, m.branchRun, "the run stays tracked for the trailing watchdog")
	require.True(t, m.branchRun.reconciledAfterFinish)

	return accepted.outcome.userID
}

func TestBranchSubmissionIntegration_AcceptedStreamAndFinish(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "hello there")

	m := newIntegrationUI(t, f, sess.ID, &source)
	m.tryStartBranchPreview()
	require.NotNil(t, m.branchPreview)

	m.textarea.SetValue("edited continuation prompt")

	acceptedID := driveAcceptedBranch(t, m)
	require.NotNil(t, m.branchReturn, "an accepted branch saves a recoverable return snapshot")

	acceptedUser, err := f.Messages.Get(f.Context, acceptedID)
	require.NoError(t, err)
	require.Equal(t, source.ParentMessageID, acceptedUser.ParentMessageID, "a first-root user replacement keeps the same (empty) parent as the target")
	require.Equal(t, "edited continuation prompt", acceptedUser.Content().Text)

	ids := make(map[string]bool)
	for i := range m.chat.Len() {
		ids[m.chat.ItemAt(i).(interface{ ID() string }).ID()] = true
	}
	require.False(t, ids[source.ID], "the replaced sibling source must not appear in the new branch path")
	require.True(t, ids[acceptedID])
}

func TestBranchReturnIntegration_RestoresExactSourceLeafAndDraft(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "hello there")

	m := newIntegrationUI(t, f, sess.ID, &source)
	m.textarea.SetValue("saved original draft")
	m.tryStartBranchPreview()
	m.textarea.SetValue("branch prompt")

	driveAcceptedBranch(t, m)
	require.NotNil(t, m.branchReturn)

	m.textarea.Reset()
	returnCmd := m.beginBranchReturn()
	require.NotNil(t, returnCmd)

	returnMsgs := collectMsgs(returnCmd)
	result, ok := findMsg[branchReturnResultMsg](returnMsgs)
	require.True(t, ok)
	require.NoError(t, result.err)
	m.handleBranchReturnResult(result)

	require.Nil(t, m.branchReturn, "the snapshot is consumed on success")
	require.Equal(t, "saved original draft", m.textarea.Value())

	sessAfter, err := f.Workspace.GetSession(f.Context, sess.ID)
	require.NoError(t, err)
	require.Equal(t, source.ID, sessAfter.LeafMessageID, "return navigates to the exact saved source leaf")
}

func TestBranchSubmissionIntegration_ReturnRejectedWithDirtyComposer(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "hello there")

	m := newIntegrationUI(t, f, sess.ID, &source)
	m.tryStartBranchPreview()
	m.textarea.SetValue("branch prompt")
	driveAcceptedBranch(t, m)
	require.NotNil(t, m.branchReturn)

	m.textarea.SetValue("still typing something else")
	cmd := m.beginBranchReturn()
	require.NotNil(t, cmd)
	_ = cmd()
	require.NotNil(t, m.branchReturn, "conflict must retain the snapshot")

	sessAfter, err := f.Workspace.GetSession(f.Context, sess.ID)
	require.NoError(t, err)
	require.NotEqual(t, source.ID, sessAfter.LeafMessageID, "no MoveLeaf on conflict")
}

func TestBranchKeysAfterReconciliation(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	require.NotNil(t, m.branchPreview)
	driveAcceptedBranch(t, m)
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.Equal(t, "x", m.textarea.Value())
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.NotNil(t, cmd)
	require.Empty(t, m.textarea.Value())
	executeBranchSend(t, m, cmd)
	users, err := f.Messages.ListUserMessages(f.Context, sess.ID)
	require.NoError(t, err)
	require.Len(t, users, 3)
}

func executeBranchSend(t *testing.T, m *UI, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, child := range batch {
			executeBranchSend(t, m, child)
		}
	} else if msg != nil {
		m.Update(msg)
	}
}

func TestBranchQuitPreservesPreview(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	m.textarea.SetValue("edited branch")
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	require.Equal(t, "edited branch", m.textarea.Value())
	require.NotNil(t, m.branchPreview)
	require.False(t, m.dialog.ContainsDialog(dialog.QuitID))
}

func TestBranchSnapshotPreservesViewportAndIdentity(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.tryStartBranchPreview()
	driveAcceptedBranch(t, m)
	m.chat.SetSize(40, 2)
	m.chat.ScrollToTop()
	m.chat.SetSelected(0)
	before := m.chat.ItemAt(0)
	selected := m.chat.SelectedItem()
	offset := m.chat.list.Offset()
	m.drillStack = append(m.drillStack, drillInEntry{chat: NewChat(m.com), label: "tool"})
	result := m.scheduleBranchRead(m.branchRun)().(branchReadResultMsg)
	m.Update(result)
	require.Same(t, before, m.chat.ItemAt(0))
	require.Same(t, selected, m.chat.SelectedItem())
	require.Equal(t, offset, m.chat.list.Offset())
	require.False(t, m.chat.Follow())
	require.Len(t, m.drillStack, 1)
}

func TestBranchReadBeforeFinishCannotReconcile(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "root")
	require.NoError(t, err)
	source := seedUserSource(t, f, sess.ID, "source")
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.tryStartBranchPreview()
	outcomes := collectMsgs(m.trySubmitBranch())
	accepted, ok := findMsg[branchOutcomeMsg](outcomes)
	require.True(t, ok)
	results := collectBranchAccepted(m.handleBranchOutcome(accepted))
	initial, ok := findMsg[branchReadResultMsg](results)
	require.True(t, ok)
	finished, ok := findMsg[branchOutcomeMsg](results)
	require.True(t, ok)
	m.Update(finished)
	_, next := m.Update(initial)
	require.True(t, m.branchActive())
	require.False(t, m.branchRun.reconciledAfterFinish)
	require.NotNil(t, next)
	m.Update(next())
	require.False(t, m.branchActive())
}

func collectBranchAccepted(cmd tea.Cmd) []tea.Msg {
	batch := cmd().(tea.BatchMsg)
	var msgs []tea.Msg
	for _, cmd := range batch[:len(batch)-1] {
		msgs = append(msgs, cmd())
	}
	return msgs
}
