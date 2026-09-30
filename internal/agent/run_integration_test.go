package agent_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/stretchr/testify/require"
)

func TestRunPersistsSelectedAncestry(t *testing.T) {
	for _, targetKind := range []string{"root", "later user", "assistant", "metadata parent"} {
		t.Run(targetKind, func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			root, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "SOURCE_ROOT"}}})
			require.NoError(t, err)
			assistant, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: root.ID, Parts: []message.ContentPart{message.TextContent{Text: "selected answer"}, message.Finish{Reason: message.FinishReasonEndTurn}}})
			require.NoError(t, err)
			parent := assistant.ID
			if targetKind == "metadata parent" {
				meta, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.System, ParentMessageID: parent, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{message.MCPToggleContent{}}})
				require.NoError(t, err)
				parent = meta.ID
			}
			later, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: parent, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED_CONTINUATION"}}})
			require.NoError(t, err)
			oldPath, err := f.Messages.GetBranchPath(f.Context, later.ID)
			require.NoError(t, err)
			wantParent := parent
			if targetKind == "root" {
				wantParent = ""
			}
			if targetKind == "assistant" {
				wantParent = assistant.ID
			}
			require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, wantParent))
			_, err = f.Coordinator.Run(f.Context, s.ID, "replacement")
			require.NoError(t, err)
			f.Coordinator.WaitBackgroundJobs()
			accepted := acceptedUser(t, f, s.ID, "replacement")
			require.Equal(t, wantParent, accepted.ParentMessageID)
			current, err := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, err)
			path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
			require.NoError(t, err)
			require.Equal(t, accepted.ID, path[len(path)-1].ParentMessageID)
			require.Equal(t, accepted.ID, path[len(path)-2].ID)
			unchanged, err := f.Messages.GetBranchPath(f.Context, later.ID)
			require.NoError(t, err)
			require.Equal(t, oldPath, unchanged)
			for _, request := range f.Provider.Requests() {
				require.NotContains(t, request.Body, "EXCLUDED_CONTINUATION")
				if targetKind == "root" {
					require.NotContains(t, request.Body, "SOURCE_ROOT")
				}
			}
		})
	}
}

func TestRunAuthRetryRetainsAcceptedUser(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "root", true: "metadata"}[metadata], func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			parent := ""
			if metadata {
				m, e := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.System, MessageType: message.MessageTypeLabel})
				require.NoError(t, e)
				parent = m.ID
			}
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: parent, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
			require.NoError(t, err)
			require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Text: "retried"})
			_, err = f.Coordinator.Run(f.Context, s.ID, "unique request")
			require.NoError(t, err)
			f.Coordinator.WaitBackgroundJobs()
			accepted := acceptedUser(t, f, s.ID, "unique request")
			current, err := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, err)
			path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
			require.NoError(t, err)
			require.Equal(t, accepted.ID, path[len(path)-1].ParentMessageID)
			require.Equal(t, parent, accepted.ParentMessageID)
			all, err := f.Messages.List(f.Context, s.ID)
			require.NoError(t, err)
			want := 3
			if metadata {
				want++
			}
			require.Len(t, all, want)
			requests := f.Provider.Requests()
			require.GreaterOrEqual(t, len(requests), 2)
			require.Equal(t, requests[0].Body, requests[1].Body)
			require.Equal(t, 1, strings.Count(requests[1].Body, "unique request"))
			for _, request := range requests {
				require.NotContains(t, request.Body, "EXCLUDED")
			}
		})
	}
}

func TestAdmissionQueueHandoffOwnsQueuedRun(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	branchStarted, branchRelease := make(chan struct{}), make(chan struct{})
	queuedStarted, queuedRelease := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{Text: "branch", Started: branchStarted, Release: branchRelease}, branchfixture.Response{Text: "queued", Started: queuedStarted, Release: queuedRelease})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.Run(f.Context, s.ID, "branch")
		done <- err
	}()
	select {
	case <-branchStarted:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	require.ErrorIs(t, f.Coordinator.Summarize(f.Context, s.ID), agent.ErrSessionBusy)
	bytes := []byte("frozen bytes")
	_, err = f.Coordinator.Run(f.Context, s.ID, "queued", message.Attachment{MimeType: "text/plain", Content: bytes})
	require.NoError(t, err)
	bytes[0] = 'X'
	require.Equal(t, []string{"queued"}, f.Coordinator.QueuedPromptsList(s.ID))
	close(branchRelease)
	select {
	case <-queuedStarted:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	require.True(t, f.Coordinator.IsSessionBusy(s.ID))
	close(queuedRelease)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	f.Coordinator.WaitBackgroundJobs()
	require.False(t, f.Coordinator.IsSessionBusy(s.ID))
	require.Contains(t, f.Provider.Requests()[1].Body, "frozen bytes")
	require.NotContains(t, f.Provider.Requests()[1].Body, "Xrozen")
}

func TestRunSummaryCancellationRestoresLeaf(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	started := make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Started: started, Release: make(chan struct{})})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.Run(f.Context, s.ID, "branch")
		done <- err
	}()
	select {
	case <-started:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	require.True(t, f.Coordinator.IsSessionBusy(s.ID))
	f.Coordinator.Cancel(s.ID)
	select {
	case err = <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Len(t, path, 3)
	require.Equal(t, message.Tool, path[2].Role)
	all, err := f.Messages.List(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, all, 4)
	require.False(t, f.Coordinator.IsSessionBusy(s.ID))
}

func TestRunOAuthOuterRetry(t *testing.T) {
	for _, mode := range []string{"success", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			provider, _ := f.Config.Config().Providers.Get("anthropic")
			provider.APIKey = "Bearer stale-token"
			provider.OAuthToken = &oauth.Token{AccessToken: "stale-token", RefreshToken: "stale-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			f.Config.Config().Providers.Set("anthropic", provider)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
			require.NoError(t, err)
			require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "outer-token"})
			if mode == "rejected" {
				f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "last-token"}, branchfixture.Response{Status: http.StatusUnauthorized})
			} else {
				f.Provider.Enqueue(branchfixture.Response{Text: "success"})
			}
			_, err = f.Coordinator.Run(f.Context, s.ID, "UNIQUE_REQUEST")
			if mode == "rejected" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			accepted := acceptedUser(t, f, s.ID, "UNIQUE_REQUEST")
			current, e := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, e)
			path, e := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
			require.NoError(t, e)
			require.Equal(t, accepted.ID, path[0].ID)
			require.Equal(t, accepted.ID, path[len(path)-1].ParentMessageID)
			requests := f.Provider.Requests()
			require.GreaterOrEqual(t, len(requests), 3)
			require.Equal(t, "Bearer stale-token", requests[0].Authorization)
			require.Equal(t, "Bearer fresh-token", requests[1].Authorization)
			for _, request := range requests {
				require.NotContains(t, request.Body, "EXCLUDED")
				require.Equal(t, 1, strings.Count(request.Body, "UNIQUE_REQUEST"))
			}
		})
	}
}

func TestRunPreparationCancellation(t *testing.T) {
	f := branchfixture.New(t)
	mcp.ResetInitForTest()
	t.Cleanup(mcp.ResetInitForTest)
	mcp.ArmInit()
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.Run(f.Context, s.ID, "branch")
		done <- err
	}()
	require.Eventually(t, func() bool { return f.Coordinator.IsSessionBusy(s.ID) }, time.Second, time.Millisecond)
	require.ErrorIs(t, f.Coordinator.Summarize(f.Context, s.ID), agent.ErrSessionBusy)
	_, err = f.Coordinator.Run(f.Context, s.ID, "queued")
	require.NoError(t, err)
	require.Equal(t, []string{"queued"}, f.Coordinator.QueuedPromptsList(s.ID))
	f.Coordinator.Cancel(s.ID)
	select {
	case err = <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	require.Empty(t, f.Coordinator.QueuedPromptsList(s.ID))
	require.Empty(t, f.Provider.Requests())
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	require.Equal(t, source.ParentMessageID, current.LeafMessageID)
}

func TestRunQueueCapturesModelSelection(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	started, release := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{Text: "branch", Started: started, Release: release})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.Run(f.Context, s.ID, "branch")
		done <- err
	}()
	select {
	case <-started:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	_, err = f.Coordinator.Run(f.Context, s.ID, "queued")
	require.NoError(t, err)
	f.Config.Config().Models[config.SelectedModelTypeLarge] = config.SelectedModel{Provider: "missing", Model: "missing"}
	close(release)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	f.Coordinator.WaitBackgroundJobs()
	require.Len(t, f.Provider.Requests(), 2)
}

func TestSummaryProviderErrorPersists(t *testing.T) {
	for _, mode := range []string{"ordinary", "auto-summary"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			provider, _ := f.Config.Config().Providers.Get("anthropic")
			provider.Models[0].ContextWindow = 100
			f.Config.Config().Providers.Set("anthropic", provider)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "original request"}}})
			require.NoError(t, err)
			wantRequests, wantMessages := 1, 2
			if mode == "auto-summary" {
				require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
				f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95})
				wantRequests, wantMessages = 2, 5
			}
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusBadRequest})
			if mode == "ordinary" {
				err = f.Coordinator.Summarize(f.Context, s.ID)
			} else {
				_, err = f.Coordinator.Run(f.Context, s.ID, "branch")
			}
			require.ErrorContains(t, err, "scripted provider failure")
			f.Coordinator.WaitBackgroundJobs()
			require.Len(t, f.Provider.Requests(), wantRequests)
			require.False(t, f.Coordinator.IsSessionBusy(s.ID))
			current, err := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, err)
			persisted := message.NewService(f.Queries, message.WithConn(f.Conn))
			all, err := persisted.List(f.Context, s.ID)
			require.NoError(t, err)
			require.Len(t, all, wantMessages)
			leaf, err := persisted.Get(f.Context, current.LeafMessageID)
			require.NoError(t, err)
			require.Equal(t, message.MessageTypeCompaction, leaf.MessageType)
			require.Equal(t, message.FinishReasonError, leaf.FinishReason())
			require.Equal(t, "Summarization Error", leaf.FinishPart().Message)
			require.Contains(t, leaf.FinishPart().Details, "scripted provider failure")
			parent, err := persisted.Get(f.Context, leaf.ParentMessageID)
			require.NoError(t, err)
			if mode == "ordinary" {
				require.Equal(t, source.ID, parent.ID)
			} else {
				require.Equal(t, message.Tool, parent.Role)
				require.Len(t, parent.ToolResults(), 1)
			}
		})
	}
}

func TestSummaryAuthRetryRestoresParent(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "original request"}}})
	require.NoError(t, err)
	f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Text: "summary"})
	require.NoError(t, f.Coordinator.Summarize(f.Context, s.ID))
	require.Len(t, f.Provider.Requests(), 2)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	all, err := f.Messages.List(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, all, 2)
	leaf, err := f.Messages.Get(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Equal(t, source.ID, leaf.ParentMessageID)
	require.Equal(t, message.MessageTypeCompaction, leaf.MessageType)
	require.Equal(t, message.FinishReasonEndTurn, leaf.FinishReason())
}

func TestRunSummaryAuthRetry(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Text: "summary"}, branchfixture.Response{Text: "continued"})
	_, err = f.Coordinator.Run(f.Context, s.ID, "branch")
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Len(t, path, 6)
	require.Equal(t, message.MessageTypeCompaction, path[3].MessageType)
	all, err := f.Messages.List(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, all, 7)
}

func TestRunUnsafeRetryPreservesToolEffects(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`}, branchfixture.Response{Status: http.StatusUnauthorized})
	_, err = f.Coordinator.Run(f.Context, s.ID, "branch")
	require.ErrorContains(t, err, "retry would replay meaningful output")
	require.Len(t, f.Provider.Requests(), 2)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Len(t, path, 4)
	require.Equal(t, message.Tool, path[2].Role)
}

func TestRunRetryCleanupFailureStopsProvider(t *testing.T) {
	for _, operation := range []string{"UPDATE OF leaf_message_id ON sessions", "DELETE ON messages"} {
		t.Run(operation, func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
			require.NoError(t, err)
			require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
			started, release := make(chan struct{}), make(chan struct{})
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized, Started: started, Release: release})
			done := make(chan error, 1)
			go func() {
				_, err := f.Coordinator.Run(f.Context, s.ID, "branch")
				done <- err
			}()
			select {
			case <-started:
			case <-f.Context.Done():
				t.Fatal(f.Context.Err())
			}
			_, err = f.Conn.Exec("CREATE TRIGGER reject_cleanup BEFORE " + operation + " BEGIN SELECT RAISE(ABORT, 'cleanup rejected'); END")
			require.NoError(t, err)
			close(release)
			select {
			case err = <-done:
				require.ErrorContains(t, err, "cleanup rejected")
			case <-f.Context.Done():
				t.Fatal(f.Context.Err())
			}
			require.Len(t, f.Provider.Requests(), 1)
			current, err := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, err)
			_, err = f.Messages.Get(f.Context, current.LeafMessageID)
			require.NoError(t, err)
			all, err := f.Messages.List(f.Context, s.ID)
			require.NoError(t, err)
			require.Len(t, all, 3)
		})
	}
}

func TestRunTitleFallbackUsesSelectedPath(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
	require.NoError(t, err)
	require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ParentMessageID))
	f.Provider.Enqueue(branchfixture.Response{Text: "branch response"}, branchfixture.Response{Status: http.StatusBadRequest}, branchfixture.Response{Text: "title fallback"})
	_, err = f.Coordinator.Run(f.Context, s.ID, "replacement")
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
	requests := f.Provider.Requests()
	require.Len(t, requests, 3)
	for _, request := range requests {
		require.NotContains(t, request.Body, "EXCLUDED")
	}
	require.Contains(t, requests[2].Body, "branch response")
}

func TestAdmissionOrdinaryCompactionKeepsFIFO(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	started, release := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95, Started: started, Release: release}, branchfixture.Response{Text: "summary"}, branchfixture.Response{Text: "queued answer"}, branchfixture.Response{Text: "continuation"})
	done := make(chan error, 1)
	go func() { _, err := f.Coordinator.Run(f.Context, s.ID, "first"); done <- err }()
	select {
	case <-started:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	_, err = f.Coordinator.Run(f.Context, s.ID, "second")
	require.NoError(t, err)
	close(release)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	users, err := f.Messages.ListUserMessages(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, users, 3)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	var prompts []string
	for _, m := range path {
		if m.Role == message.User {
			prompts = append(prompts, m.Content().Text)
		}
	}
	require.Equal(t, "first", prompts[0])
	require.Equal(t, "second", prompts[1])
	require.Contains(t, prompts[2], "initial user request was: `first`")
}

func acceptedUser(t *testing.T, f *branchfixture.Fixture, sessionID, prompt string) message.Message {
	t.Helper()
	users, err := f.Messages.ListUserMessages(f.Context, sessionID)
	require.NoError(t, err)
	var matches []message.Message
	for _, user := range users {
		if user.Content().Text == prompt {
			matches = append(matches, user)
		}
	}
	require.Len(t, matches, 1)
	return matches[0]
}
