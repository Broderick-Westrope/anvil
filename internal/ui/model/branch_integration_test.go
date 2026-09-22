package model

import (
	"crypto/sha256"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func finishInlineBranchKeys(t *testing.T, m *UI, submit tea.Cmd) branchOutcomeMsg {
	t.Helper()
	results := collectMsgs(submit)
	accepted, ok := findMsg[branchOutcomeMsg](results)
	require.True(t, ok)
	require.Equal(t, branchOutcomeAccepted, accepted.outcome.kind)
	_, cmd := m.Update(accepted)
	results = collectBranchAccepted(cmd)
	initial, ok := findMsg[branchReadResultMsg](results)
	require.True(t, ok)
	require.NoError(t, initial.err)
	m.Update(initial)
	require.False(t, m.branchLoading)
	require.NotNil(t, m.chat.MessageItem(accepted.outcome.userID))
	finished, ok := findMsg[branchOutcomeMsg](results)
	require.True(t, ok)
	require.Equal(t, branchOutcomeFinished, finished.outcome.kind)
	_, cmd = m.Update(finished)
	final, ok := findMsg[branchReadResultMsg](collectMsgs(cmd))
	require.True(t, ok)
	require.NoError(t, final.err)
	m.Update(final)
	require.False(t, m.branchActive())
	require.Empty(t, m.branchRun.outcome)
	return finished
}

func TestInlineBranchKeysRestoreCompleteSource(t *testing.T) {
	for _, role := range []message.MessageRole{message.User, message.Assistant} {
		t.Run(string(role), func(t *testing.T) {
			f := branchfixture.New(t)
			sess, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			sentinel := filepath.Join(f.Config.WorkingDir(), "sentinel.png")
			diskBytes := []byte("workspace contents must not be rolled back")
			require.NoError(t, os.WriteFile(sentinel, diskBytes, 0o600))
			diskHash := sha256.Sum256(diskBytes)
			raw := "<skill_content name=\"stored\">\nPreserve <raw> & text.\n</skill_content>\n\nOriginal request"
			storedBytes := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
			root, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{
				message.TextContent{Text: raw}, message.BinaryContent{Path: sentinel, MIMEType: "image/png", Data: storedBytes},
			}})
			require.NoError(t, err)
			call, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: root.ID, Parts: []message.ContentPart{
				message.ToolCall{ID: "historical-tool", Name: "todos", Input: `{"todos":[]}`, Finished: true}, message.Finish{Reason: message.FinishReasonToolUse},
			}})
			require.NoError(t, err)
			result, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.Tool, ParentMessageID: call.ID, Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "historical-tool", Name: "todos", Content: "historical result"},
			}})
			require.NoError(t, err)
			compact, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: result.ID, MessageType: message.MessageTypeCompaction, Parts: []message.ContentPart{
				message.CompactionContent{Summary: "retained summary", FirstKeptEntryID: root.ID},
			}})
			require.NoError(t, err)
			assistant, err := f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: compact.ID, Parts: []message.ContentPart{
				message.TextContent{Text: "completed answer"}, message.Finish{Reason: message.FinishReasonEndTurn},
			}})
			require.NoError(t, err)
			for _, text := range []string{"EXCLUDED_SIBLING", "EXCLUDED_CURRENT"} {
				_, err = f.Messages.Create(f.Context, sess.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: assistant.ID, Parts: []message.ContentPart{message.TextContent{Text: text}}})
				require.NoError(t, err)
			}
			sess, err = f.Workspace.GetSession(f.Context, sess.ID)
			require.NoError(t, err)
			require.NoError(t, f.Workspace.WriteMetadataEntry(f.Context, sess.ID, message.CreateMessageParams{ParentMessageID: sess.LeafMessageID, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{
				message.MCPToggleContent{ServerName: "excluded-server", Enabled: true},
			}}))
			m := newIntegrationUI(t, f, sess.ID, &root)
			t.Cleanup(m.clearBranchState)
			before, err := f.Messages.List(f.Context, sess.ID)
			require.NoError(t, err)
			sourceLeaf := m.session.LeafMessageID
			sourcePath, err := f.Workspace.GetBranchPath(f.Context, sourceLeaf)
			require.NoError(t, err)
			m.installBranchSnapshot(sourcePath, nil, nil)
			m.width, m.height = 90, 28
			m.updateLayoutAndSize()
			target := root
			if role == message.Assistant {
				target = assistant
			}
			for i := range m.chat.Len() {
				if m.chat.ItemAt(i).(chat.MessageItem).ID() == target.ID {
					m.chat.SetSelected(i)
				}
			}
			m.chat.SetFollow(false)
			originalViewport := m.chat.branchViewport()
			m.textarea.SetValue("unsent original draft")
			draftFile := message.Attachment{FilePath: sentinel, FileName: "sentinel.png", MimeType: "image/png", Content: []byte{9, 8, 0, 7}}
			draftSkill := attachments.SkillAttachment{Name: "draft-only", Instructions: "Do not send this on the branch"}
			m.attachments.Update(draftFile)
			m.attachments.Update(draftSkill)
			m.promptHistory.messages = []string{"older prompt", "newer prompt"}
			m.promptHistory.index = 1
			m.promptHistory.draft = "history draft"
			originalHistory := m.promptHistory

			for _, commit := range []bool{false, true} {
				m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
				require.NotNil(t, m.branchPreview)
				require.Empty(t, m.attachments.SkillList())
				if role == message.User {
					require.Equal(t, raw, m.textarea.Value())
					require.Len(t, m.attachments.List(), 1)
					require.Equal(t, storedBytes, m.attachments.List()[0].Content)
				} else {
					require.Empty(t, m.textarea.Value())
					require.Empty(t, m.attachments.List())
				}
				current, err := f.Workspace.GetSession(f.Context, sess.ID)
				require.NoError(t, err)
				require.Equal(t, sourceLeaf, current.LeafMessageID)
				unchanged, err := f.Messages.List(f.Context, sess.ID)
				require.NoError(t, err)
				require.Equal(t, before, unchanged)
				if !commit {
					m.textarea.SetValue("discarded branch edit")
					m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				} else {
					prompt := "/tree"
					if role == message.User {
						prompt += "\n\n" + strings.ReplaceAll(raw, "Original request", "Edited request")
					}
					m.textarea.SetValue(prompt)
					_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					require.NotNil(t, submit)
					require.True(t, m.branchLoading)
					finished := finishInlineBranchKeys(t, m, submit)
					require.NoError(t, finished.outcome.err)
					f.Coordinator.WaitBackgroundJobs()
					acceptedID := m.branchRun.acceptedUserID
					accepted, err := f.Messages.Get(f.Context, acceptedID)
					require.NoError(t, err)
					require.Equal(t, prompt, accepted.Content().Text)
					parent := ""
					if role == message.Assistant {
						parent = assistant.ID
					} else {
						require.Equal(t, storedBytes, accepted.BinaryContent()[0].Data)
						require.Equal(t, 1, strings.Count(accepted.Content().Text, "<skill_content"))
					}
					require.Equal(t, parent, accepted.ParentMessageID)
					branchLeaf := m.session.LeafMessageID
					branchPath, err := f.Workspace.GetBranchPath(f.Context, branchLeaf)
					require.NoError(t, err)
					require.Equal(t, acceptedID, branchPath[len(branchPath)-1].ParentMessageID)
					require.Equal(t, message.FinishReasonEndTurn, branchPath[len(branchPath)-1].FinishReason())
					for _, old := range before {
						persisted, err := f.Messages.Get(f.Context, old.ID)
						require.NoError(t, err)
						require.Equal(t, old, persisted)
					}
					users, err := f.Messages.ListUserMessages(f.Context, sess.ID)
					require.NoError(t, err)
					require.Len(t, users, 4)
					require.NotEmpty(t, f.Provider.Requests())
					for _, request := range f.Provider.Requests() {
						for _, excluded := range []string{"EXCLUDED_SIBLING", "EXCLUDED_CURRENT", "excluded-server", "draft-only", "unsent original draft"} {
							require.NotContains(t, request.Body, excluded)
						}
					}
					m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
					require.True(t, m.dialog.ContainsDialog(dialog.CommandsID))
					m.Update(tea.KeyPressMsg{Code: 'R', Text: "Return to pre-branch conversation"})
					_, returning := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					require.NotNil(t, returning)
					m.Update(returning())
					require.Nil(t, m.branchReturn)
					restoredBranch, err := f.Workspace.GetBranchPath(f.Context, branchLeaf)
					require.NoError(t, err)
					require.Equal(t, branchPath, restoredBranch)
				}
				require.Nil(t, m.branchPreview)
				require.Equal(t, "unsent original draft", m.textarea.Value())
				require.Equal(t, []message.Attachment{draftFile}, m.attachments.List())
				require.Equal(t, []attachments.SkillAttachment{draftSkill}, m.attachments.SkillList())
				require.Equal(t, originalHistory, m.promptHistory)
				require.Equal(t, uiFocusMain, m.focus)
				require.Equal(t, originalViewport, m.chat.branchViewport())
				current, err = f.Workspace.GetSession(f.Context, sess.ID)
				require.NoError(t, err)
				require.Equal(t, sourceLeaf, current.LeafMessageID)
				restored, err := f.Workspace.GetBranchPath(f.Context, sourceLeaf)
				require.NoError(t, err)
				require.Equal(t, sourcePath, restored)
				contents, err := os.ReadFile(sentinel)
				require.NoError(t, err)
				require.Equal(t, diskHash, sha256.Sum256(contents))
			}
		})
	}
}

func TestInlineBranchKeysShowPersistedProgressBeforeFinish(t *testing.T) {
	f := branchfixture.New(t)
	sess, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, sess.ID, "custom", true))
	source := seedUserSource(t, f, sess.ID, "EXCLUDED_ORIGINAL")
	m := newIntegrationUI(t, f, sess.ID, &source)
	t.Cleanup(m.clearBranchState)
	started, release := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`},
		branchfixture.Response{Text: "final response", Started: started, Release: release},
	)
	m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
	m.textarea.SetValue("show branch progress")
	_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	batch := submit().(tea.BatchMsg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		batch[0]()
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	})
	select {
	case <-started:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	accepted := batch[1]().(branchOutcomeMsg)
	require.Equal(t, branchOutcomeAccepted, accepted.outcome.kind)
	_, acceptance := m.Update(accepted)
	commands := acceptance().(tea.BatchMsg)
	initial := commands[0]().(branchReadResultMsg)
	require.NoError(t, initial.err)
	m.Update(initial)
	require.False(t, m.branchLoading)
	require.True(t, m.branchActive())
	require.Nil(t, m.chat.MessageItem(source.ID))
	require.NotNil(t, m.chat.MessageItem(accepted.outcome.userID))
	var toolID string
	for _, msg := range initial.messages {
		if calls := msg.ToolCalls(); len(calls) > 0 {
			toolID = calls[0].ID
		}
	}
	require.NotEmpty(t, toolID)
	require.NotNil(t, m.chat.MessageItem(toolID))
	close(release)
	<-done
	finished := commands[1]().(branchOutcomeMsg)
	require.Equal(t, branchOutcomeFinished, finished.outcome.kind)
	require.NoError(t, finished.outcome.err)
	_, final := m.Update(finished)
	m.Update(final())
	require.False(t, m.branchActive())
	visible := m.chat.MessageItem(m.session.LeafMessageID).(chat.SourceMessageProvider).SourceMessage()
	require.Equal(t, "final response", visible.Content().Text)
	require.Equal(t, message.FinishReasonEndTurn, visible.FinishReason())
}

func TestInlineBranchKeysReconcileOAuthAndCompaction(t *testing.T) {
	for _, mode := range []string{"retry", "compaction", "provider-error"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			provider, _ := f.Config.Config().Providers.Get("anthropic")
			provider.APIKey = "Bearer stale-token"
			provider.OAuthToken = &oauth.Token{AccessToken: "stale-token", RefreshToken: "stale-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if mode == "compaction" {
				provider.Models[0].ContextWindow = 100
				f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Text: "summary"})
			}
			f.Config.Config().Providers.Set("anthropic", provider)
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "outer-token"})
			if mode == "provider-error" {
				f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "last-token"}, branchfixture.Response{Status: http.StatusUnauthorized})
			} else {
				f.Provider.Enqueue(branchfixture.Response{Text: "branch completed"})
			}
			sess, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			require.NoError(t, f.Sessions.Rename(f.Context, sess.ID, "custom", true))
			source := seedUserSource(t, f, sess.ID, "EXCLUDED_ORIGINAL")
			m := newIntegrationUI(t, f, sess.ID, &source)
			t.Cleanup(m.clearBranchState)
			m.Update(tea.KeyPressMsg{Code: 'B', Text: "B"})
			m.textarea.SetValue("UNIQUE_BRANCH_REQUEST")
			_, submit := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			finished := finishInlineBranchKeys(t, m, submit)
			if mode == "provider-error" {
				require.Error(t, finished.outcome.err)
			} else {
				require.NoError(t, finished.outcome.err)
			}
			require.Empty(t, m.textarea.Value())
			require.NotNil(t, m.branchReturn)
			require.Nil(t, m.chat.MessageItem(source.ID))
			path, err := f.Workspace.GetBranchPath(f.Context, m.session.LeafMessageID)
			require.NoError(t, err)
			require.Equal(t, m.branchRun.acceptedUserID, path[0].ID)
			require.Empty(t, path[0].ParentMessageID)
			if mode == "compaction" {
				require.Equal(t, message.MessageTypeCompaction, path[len(path)-2].MessageType)
			}
			users, err := f.Messages.ListUserMessages(f.Context, sess.ID)
			require.NoError(t, err)
			require.Len(t, users, 2)
			requests := f.Provider.Requests()
			require.GreaterOrEqual(t, len(requests), 3)
			require.Equal(t, "Bearer stale-token", requests[0].Authorization)
			for _, request := range requests {
				require.NotContains(t, request.Body, "EXCLUDED_ORIGINAL")
				require.LessOrEqual(t, strings.Count(request.Body, "UNIQUE_BRANCH_REQUEST"), 1)
			}
		})
	}
}
