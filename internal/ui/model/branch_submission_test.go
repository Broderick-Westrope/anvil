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
	"github.com/stretchr/testify/require"
)

// batchCmds extracts the underlying []tea.Cmd from a message produced by
// tea.Batch. tea.BatchMsg is exported; this driver never needs to peer
// inside tea.Sequence because nothing under test here uses it directly
// (setSessionMessages's internal tea.Sequence is executed opaquely,
// which is fine: its own messages don't need inspecting by these tests).
func batchCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	b, ok := msg.(tea.BatchMsg)
	return []tea.Cmd(b), ok
}

// collectMsgs runs cmd and, if it is a tea.Batch, runs each sub-command
// once (non-recursively) and collects every resulting non-nil message.
// This is sufficient for the finite, one-level-deep command batches
// under test; it deliberately does not follow the perpetual
// reconciliation poll loop, which these tests drive explicitly instead.
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

// newIntegrationUI builds a *UI wired to a real fixture AppWorkspace, with
// a real Chat containing the given source message as the sole (selected)
// item, ready to drive tryStartBranchPreview/trySubmitBranch end to end.
func newIntegrationUI(t *testing.T, f *branchfixture.Fixture, sessionID string, source *message.Message) *UI {
	t.Helper()
	com := common.DefaultCommon(f.Workspace)
	sess, err := f.Workspace.GetSession(f.Context, sessionID)
	require.NoError(t, err)

	m := &UI{
		com:     com,
		keyMap:  DefaultKeyMap(),
		chat:    NewChat(com),
		focus:   uiFocusMain,
		session: &sess,
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

// driveAcceptedBranch submits ui's active preview, then drives the
// resulting run through acceptance and both reconciliation reads
// (immediate-on-accept and mandatory-post-finish), asserting each stage
// completes deterministically. It returns the run's accepted user ID.
func driveAcceptedBranch(t *testing.T, m *UI) string {
	t.Helper()

	submitCmd := m.trySubmitBranch()
	require.NotNil(t, submitCmd)
	require.True(t, m.branchActive())
	require.True(t, m.branchLoading)

	// tea.Batch(runCmd, waitCmd): runCmd performs the whole
	// fixture-served turn synchronously, filling the outcome channel
	// with accepted then finished before waitCmd ever reads it.
	msgs := collectMsgs(submitCmd)
	accepted, ok := findMsg[branchOutcomeMsg](msgs)
	require.True(t, ok, "accepted outcome must be observed")
	require.Equal(t, branchOutcomeAccepted, accepted.outcome.kind)

	acceptedMsgs := collectMsgs(m.handleBranchOutcome(accepted))
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
	// source is the session's first (root) message, so branching from a
	// user target retains the target's parent — here, none — rather
	// than appending under source itself.
	require.Equal(t, source.ParentMessageID, acceptedUser.ParentMessageID, "a first-root user replacement keeps the same (empty) parent as the target")
	require.Equal(t, "edited continuation prompt", acceptedUser.Content().Text)

	// The transcript now shows only the new branch (accepted user +
	// assistant reply), excluding the old sibling source, not a mix of
	// stale UI state and the new turn.
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

	// The composer must be idle/empty before a return is allowed.
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

	// Leave the composer dirty: return must refuse before any IO or
	// MoveLeaf, retaining the snapshot.
	m.textarea.SetValue("still typing something else")
	cmd := m.beginBranchReturn()
	require.NotNil(t, cmd)
	_ = cmd() // a warning message, not a branchReturnResultMsg
	require.NotNil(t, m.branchReturn, "conflict must retain the snapshot")

	sessAfter, err := f.Workspace.GetSession(f.Context, sess.ID)
	require.NoError(t, err)
	require.NotEqual(t, source.ID, sessAfter.LeafMessageID, "no MoveLeaf on conflict")
}
