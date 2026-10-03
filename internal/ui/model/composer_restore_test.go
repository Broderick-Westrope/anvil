package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
)

func TestComposerFromMessage(t *testing.T) {
	t.Parallel()
	markdown := "# Heading\n\n- list\n    code  \n\nlast line"
	one := skills.FormatContentXML("one", "\n  instructions\n")
	two := skills.FormatContentXML("two", "second")
	firstSkill := attachments.SkillAttachment{Name: "one", Instructions: "\n  instructions\n", Source: "user"}
	secondSkill := attachments.SkillAttachment{Name: "two", Instructions: "second"}
	for _, tt := range []struct {
		name string
		text string
		want composerSnapshot
	}{
		{"markdown", markdown, composerSnapshot{text: markdown}},
		{"command", commands.FormatExpansionXML("/review fix typo", "expanded"), composerSnapshot{text: "/review fix typo"}},
		{"command skills", one + "\n\n" + two + "\n\n" + commands.FormatExpansionXML("/review fix typo", "expanded"), composerSnapshot{text: "/review fix typo"}},
		{"one skill", one + "\n\n" + markdown, composerSnapshot{text: markdown, skills: []attachments.SkillAttachment{firstSkill}}},
		{"multiple skills", one + "\n\n" + two + "\n\n" + markdown, composerSnapshot{text: markdown, skills: []attachments.SkillAttachment{firstSkill, secondSkill}}},
		{"skill only", one + "\n\n", composerSnapshot{skills: []attachments.SkillAttachment{firstSkill}}},
		{"skill without separator", one, composerSnapshot{skills: []attachments.SkillAttachment{firstSkill}}},
		{"preserve separator remainder", one + "\n\n\n  indented\n\n", composerSnapshot{text: "\n  indented\n\n", skills: []attachments.SkillAttachment{firstSkill}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := composerFromMessage(message.Message{Parts: []message.ContentPart{message.TextContent{Text: tt.text}}}, func(name string) *skills.Skill {
				if name == "one" {
					return &skills.Skill{Name: name, Instructions: "new instructions", Source: "user"}
				}
				return nil
			}, nil)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestComposerFromMessageCopiesAttachments(t *testing.T) {
	t.Parallel()
	file := []byte("file")
	image := []byte("image")
	msg := message.Message{Parts: []message.ContentPart{
		message.BinaryContent{Path: "/tmp/file.txt", MIMEType: "text/plain", Data: file},
		message.BinaryContent{Path: "/tmp/image.png", MIMEType: "image/png", Data: image},
		message.ImageURLContent{URL: "https://example.com/image.png"},
	}}
	got := composerFromMessage(msg, nil, nil)
	require.Equal(t, composerSnapshot{attachments: []message.Attachment{
		{FilePath: "/tmp/file.txt", FileName: "file.txt", MimeType: "text/plain", Content: []byte("file")},
		{FilePath: "/tmp/image.png", FileName: "image.png", MimeType: "image/png", Content: []byte("image")},
	}}, got)
	file[0], image[0] = 'X', 'X'
	require.Equal(t, []byte("file"), got.attachments[0].Content)
	require.Equal(t, []byte("image"), got.attachments[1].Content)
}

func composerRestoreUI(t *testing.T, text string) (*UI, *branchfixture.Fixture) {
	t.Helper()
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "restore")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: skills.FormatContentXML("review", "stored instructions") + "\n\n" + text},
			message.BinaryContent{Path: "/tmp/source.txt", MIMEType: "text/plain", Data: []byte("source bytes")},
		},
	})
	require.NoError(t, err)
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, sess.ID, source.ID))
	m := newIntegrationUI(t, f, sess.ID, &source)
	m.textarea.SetValue("draft\n  text")
	m.attachments.Update(message.Attachment{FilePath: "/tmp/draft.png", FileName: "draft.png", MimeType: "image/png", Content: []byte("draft bytes")})
	m.attachments.Update(attachments.SkillAttachment{Name: "draft", Instructions: "draft instructions", Source: "user"})
	return m, f
}

func requireRestoredComposer(t *testing.T, m *UI, text string) {
	t.Helper()
	require.Equal(t, text, m.textarea.Value())
	require.Equal(t, []message.Attachment{{FilePath: "/tmp/source.txt", FileName: "source.txt", MimeType: "text/plain", Content: []byte("source bytes")}}, m.attachments.List())
	require.Equal(t, []attachments.SkillAttachment{{Name: "review", Instructions: "stored instructions"}}, m.attachments.SkillList())
}

func requireOriginalComposer(t *testing.T, m *UI) {
	t.Helper()
	require.Equal(t, "draft\n  text", m.textarea.Value())
	require.Equal(t, []message.Attachment{{FilePath: "/tmp/draft.png", FileName: "draft.png", MimeType: "image/png", Content: []byte("draft bytes")}}, m.attachments.List())
	require.Equal(t, []attachments.SkillAttachment{{Name: "draft", Instructions: "draft instructions", Source: "user"}}, m.attachments.SkillList())
}

func TestBranchRestoresComposerState(t *testing.T) {
	for _, entry := range []string{"shift+b", dialog.BranchID, dialog.TreeID} {
		t.Run(entry, func(t *testing.T) {
			text := "# Heading\n\n- item\n    indented **markdown**\n\nlast line"
			m, _ := composerRestoreUI(t, text)
			if entry == "shift+b" {
				pressBranch(t, m)
			} else {
				require.Nil(t, m.openDialog(entry))
				_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				completeNavigation(t, m, cmd)
			}
			requireRestoredComposer(t, m, text)
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			completeNavigation(t, m, cmd)
			requireOriginalComposer(t, m)
		})
	}
}

func TestHistoryRestoresComposerState(t *testing.T) {
	for _, exit := range []rune{tea.KeyDown, tea.KeyEscape} {
		t.Run(tea.KeyPressMsg{Code: exit}.String(), func(t *testing.T) {
			text := "# Heading\n\n    code\n\nlast line"
			m, _ := composerRestoreUI(t, text)
			m.focus = uiFocusEditor
			m.Update(m.loadPromptHistory()())
			m.textarea.MoveToBegin()
			m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			requireRestoredComposer(t, m, text)
			m.textarea.MoveToEnd()
			m.Update(tea.KeyPressMsg{Code: exit})
			requireOriginalComposer(t, m)
		})
	}
}

func TestCommandComposerRestoration(t *testing.T) {
	for _, entry := range []string{"shift+b", dialog.BranchID, dialog.TreeID, "history"} {
		t.Run(entry, func(t *testing.T) {
			m, _ := composerRestoreUI(t, commands.FormatExpansionXML("/review fix typo", "expanded review instructions"))
			switch entry {
			case "shift+b":
				pressBranch(t, m)
			case "history":
				m.focus = uiFocusEditor
				m.Update(m.loadPromptHistory()())
				m.textarea.MoveToBegin()
				m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			default:
				require.Nil(t, m.openDialog(entry))
				_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				completeNavigation(t, m, cmd)
			}
			require.Equal(t, "/review fix typo", m.textarea.Value())
			require.Empty(t, m.attachments.SkillList())
		})
	}
}

func TestHistoryPreservesOrderAndEmptyFiltering(t *testing.T) {
	m, f := composerRestoreUI(t, "text")
	for _, text := range []string{"", "same", "same", "  "} {
		seedUserSource(t, f, m.session.ID, text)
	}
	stored, err := f.Workspace.ListUserMessages(f.Context, m.session.ID)
	require.NoError(t, err)
	var expected []string
	for _, msg := range stored {
		if msg.Content().Text != "" {
			_, text := skills.ParseContentXML(msg.Content().Text)
			expected = append(expected, text)
		}
	}
	m.Update(m.loadPromptHistory()())
	require.Len(t, m.promptHistory.messages, len(expected))
	m.focus = uiFocusEditor
	for _, text := range expected {
		m.textarea.MoveToBegin()
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		require.Equal(t, text, m.textarea.Value())
	}
	for i := len(expected) - 2; i >= 0; i-- {
		m.textarea.MoveToEnd()
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		require.Equal(t, expected[i], m.textarea.Value())
	}
	m.textarea.MoveToEnd()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	requireOriginalComposer(t, m)
}

func TestHistoryComposerSnapshotsAreIndependent(t *testing.T) {
	m, _ := composerRestoreUI(t, "text")
	m.focus = uiFocusEditor
	m.Update(m.loadPromptHistory()())
	m.textarea.MoveToBegin()
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.attachments.Reset()
	m.attachments.Update(message.Attachment{FileName: "replacement.txt", Content: []byte("replacement")})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	requireOriginalComposer(t, m)
	m.textarea.MoveToBegin()
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	requireRestoredComposer(t, m, "text")
}

func TestHistoryAttachmentOnlyMessage(t *testing.T) {
	m, f := composerRestoreUI(t, "")
	_, err := f.Messages.Create(f.Context, m.session.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.BinaryContent{Path: "/tmp/only.png", MIMEType: "image/png", Data: []byte("image")}},
	})
	require.NoError(t, err)
	m.Update(m.loadPromptHistory()())
	require.Len(t, m.promptHistory.messages, 2)
	m.focus = uiFocusEditor
	for range 2 {
		m.textarea.MoveToBegin()
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
		if m.textarea.Value() == "" && len(m.attachments.SkillList()) == 0 {
			require.Equal(t, []message.Attachment{{FilePath: "/tmp/only.png", FileName: "only.png", MimeType: "image/png", Content: []byte("image")}}, m.attachments.List())
			return
		}
	}
	t.Fatal("attachment-only history entry was not restored")
}
