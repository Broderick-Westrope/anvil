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
	sess, err := f.Workspace.GetSession(f.Context, sessionID)
	require.NoError(t, err)
	created, err := f.Messages.Create(f.Context, sessionID, message.CreateMessageParams{
		ParentMessageID: sess.LeafMessageID,
		Role:            message.User,
		Parts:           []message.ContentPart{message.TextContent{Text: text}},
	})
	require.NoError(t, err)
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, sessionID, created.ID))
	return created
}
