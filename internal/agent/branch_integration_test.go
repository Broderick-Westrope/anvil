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

func TestBranchPersistsSelectedAncestry(t *testing.T) {
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
			target, wantParent := later.ID, parent
			if targetKind == "root" {
				target, wantParent = root.ID, ""
			}
			if targetKind == "assistant" {
				target, wantParent = assistant.ID, assistant.ID
			}
			var accepted message.Message
			count := 0
			_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "replacement", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: target, ExpectedSourceLeafID: later.ID}, OnUserMessageCreated: func(m message.Message) {
				count++
				accepted = m
				persisted, e := f.Messages.Get(context.Background(), m.ID)
				require.NoError(t, e)
				require.Equal(t, m.ID, persisted.ID)
				current, e := f.Sessions.Get(context.Background(), s.ID)
				require.NoError(t, e)
				require.Equal(t, m.ID, current.LeafMessageID)
				require.True(t, f.Coordinator.IsSessionBusy(s.ID))
			}})
			require.NoError(t, err)
			f.Coordinator.WaitBackgroundJobs()
			require.Equal(t, 1, count)
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

func TestBranchRollbackAndCancellation(t *testing.T) {
	for _, mode := range []string{"insert", "leaf", "before cancellation", "accepted cancellation", "provider failure"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "source"}}})
			require.NoError(t, err)
			switch mode {
			case "insert":
				_, err = f.Conn.Exec("CREATE TRIGGER fail_branch BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'insert rejected'); END")
			case "leaf":
				_, err = f.Conn.Exec("CREATE TRIGGER fail_branch BEFORE UPDATE OF leaf_message_id ON sessions BEGIN SELECT RAISE(ABORT, 'leaf rejected'); END")
			case "provider failure":
				f.Provider.Enqueue(branchfixture.Response{Status: http.StatusBadRequest})
			}
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(f.Context)
			defer cancel()
			if mode == "before cancellation" {
				cancel()
			}
			count := 0
			_, err = f.Coordinator.RunFromMessage(ctx, s.ID, "new", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(message.Message) {
				count++
				if mode == "accepted cancellation" {
					cancel()
				}
			}})
			require.Error(t, err)
			current, e := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, e)
			messages, e := f.Messages.List(f.Context, s.ID)
			require.NoError(t, e)
			if mode == "accepted cancellation" || mode == "provider failure" {
				require.Equal(t, 1, count)
				require.NotEqual(t, source.ID, current.LeafMessageID)
				users, e := f.Messages.ListUserMessages(f.Context, s.ID)
				require.NoError(t, e)
				require.Len(t, users, 2)
			} else {
				require.Zero(t, count)
				require.Len(t, messages, 1)
				require.Equal(t, source.ID, current.LeafMessageID)
				require.Empty(t, f.Provider.Requests())
			}
		})
	}
}

func TestBranchAuthRetryRetainsAcceptedUser(t *testing.T) {
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
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Text: "retried"})
			var accepted message.Message
			count := 0
			_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "unique request", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(m message.Message) { accepted = m; count++ }})
			require.NoError(t, err)
			f.Coordinator.WaitBackgroundJobs()
			require.Equal(t, 1, count)
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
			for _, request := range f.Provider.Requests() {
				require.NotContains(t, request.Body, "EXCLUDED")
			}
		})
	}
}

func TestBranchCompactionContinuationRetry(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
	require.NoError(t, err)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95},
		branchfixture.Response{Text: "summary of accepted request", InputTokens: 10},
		branchfixture.Response{Status: http.StatusUnauthorized},
		branchfixture.Response{Text: "finished", InputTokens: 10},
	)
	count := 0
	var accepted message.Message
	_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "ORIGINAL_REQUEST", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(m message.Message) { count++; accepted = m }})
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
	require.Equal(t, 1, count)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Equal(t, accepted.ID, path[0].ID)
	require.Len(t, path, 5)
	require.Equal(t, message.MessageTypeCompaction, path[3].MessageType)
	require.Equal(t, path[3].ID, path[4].ParentMessageID)
	users, err := f.Messages.ListUserMessages(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, users, 2)
	requests := f.Provider.Requests()
	require.GreaterOrEqual(t, len(requests), 4)
	require.Equal(t, requests[2].Body, requests[3].Body)
	for _, request := range requests {
		require.NotContains(t, request.Body, "EXCLUDED")
	}
}

func TestBranchQueueHandoffReturnsBeforeQueuedRun(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	branchStarted, branchRelease := make(chan struct{}), make(chan struct{})
	queuedStarted, queuedRelease := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{Text: "branch", Started: branchStarted, Release: branchRelease}, branchfixture.Response{Text: "queued", Started: queuedStarted, Release: queuedRelease})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
		done <- err
	}()
	select {
	case <-branchStarted:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "another branch", agent.BranchRunOptions{})
	require.ErrorIs(t, err, agent.ErrSessionBusy)
	require.ErrorIs(t, f.Coordinator.Summarize(f.Context, s.ID), agent.ErrSessionBusy)
	bytes := []byte("frozen bytes")
	_, err = f.Coordinator.Run(f.Context, s.ID, "queued", message.Attachment{MimeType: "text/plain", Content: bytes})
	require.NoError(t, err)
	bytes[0] = 'X'
	require.Equal(t, []string{"queued"}, f.Coordinator.QueuedPromptsList(s.ID))
	close(branchRelease)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	select {
	case <-queuedStarted:
	case <-f.Context.Done():
		t.Fatal(f.Context.Err())
	}
	require.True(t, f.Coordinator.IsSessionBusy(s.ID))
	close(queuedRelease)
	f.Coordinator.WaitBackgroundJobs()
	require.False(t, f.Coordinator.IsSessionBusy(s.ID))
	require.Contains(t, f.Provider.Requests()[1].Body, "frozen bytes")
	require.NotContains(t, f.Provider.Requests()[1].Body, "Xrozen")
}

func TestBranchSummaryCancellationRestoresLeaf(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	started := make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Started: started, Release: make(chan struct{})})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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

func TestBranchRejectsInvalidTargetsWithoutMutation(t *testing.T) {
	for _, mode := range []string{"missing", "cross session", "off path", "stale", "subsession", "metadata", "tool", "unfinished", "canceled", "error", "tool assistant", "unsafe raw", "unsafe filtered", "remote image", "unsupported binary", "image model", "empty"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			if mode == "subsession" {
				s, err = f.Sessions.CreateTaskSession(f.Context, "child", s.ID, "child")
				require.NoError(t, err)
			}
			params := message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "source"}}}
			want := agent.ErrBranchInvalidTarget
			switch mode {
			case "metadata":
				params.MessageType = message.MessageTypeLabel
			case "tool":
				params.Role = message.Tool
			case "unfinished":
				params.Role = message.Assistant
			case "canceled", "error":
				params.Role = message.Assistant
				reason := message.FinishReasonCanceled
				if mode == "error" {
					reason = message.FinishReasonError
				}
				params.Parts = append(params.Parts, message.Finish{Reason: reason})
			case "tool assistant":
				params.Role = message.Assistant
				params.Parts = append(params.Parts, message.Finish{Reason: message.FinishReasonEndTurn}, message.ToolCall{ID: "call", Name: "todos", Finished: true})
			case "unsafe raw", "unsafe filtered":
				call, e := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "call", Name: "todos", Finished: true}}})
				require.NoError(t, e)
				params.ParentMessageID = call.ID
				if mode == "unsafe filtered" {
					result, e := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.Tool, ParentMessageID: call.ID, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call", Name: "todos"}}})
					require.NoError(t, e)
					compact, e := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.Assistant, MessageType: message.MessageTypeCompaction, ParentMessageID: result.ID, Parts: []message.ContentPart{message.CompactionContent{Summary: "partial", FirstKeptEntryID: result.ID}}})
					require.NoError(t, e)
					params.ParentMessageID = compact.ID
				}
				want = agent.ErrBranchUnsafePrefix
			case "remote image":
				params.Parts = append(params.Parts, message.ImageURLContent{URL: "https://must-not-fetch.invalid/image.png"})
				want = agent.ErrBranchUnsupportedAttachment
			}
			source, err := f.Messages.Create(f.Context, s.ID, params)
			require.NoError(t, err)
			origin := agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}
			prompt := "branch"
			var attachments []message.Attachment
			switch mode {
			case "missing":
				origin.TargetMessageID = "missing"
			case "stale":
				origin.ExpectedSourceLeafID = "stale"
				want = agent.ErrBranchStaleSource
			case "cross session":
				other, e := f.Workspace.CreateSession(f.Context, "other")
				require.NoError(t, e)
				m, e := f.Messages.Create(f.Context, other.ID, message.CreateMessageParams{Role: message.User})
				require.NoError(t, e)
				origin.TargetMessageID = m.ID
			case "off path":
				m, e := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
				require.NoError(t, e)
				origin.TargetMessageID = m.ID
				require.NoError(t, f.Sessions.MoveLeaf(f.Context, s.ID, source.ID))
			case "unsupported binary":
				attachments = []message.Attachment{{MimeType: "application/pdf", Content: []byte("pdf")}}
				want = agent.ErrBranchUnsupportedAttachment
			case "image model":
				provider, _ := f.Config.Config().Providers.Get("anthropic")
				provider.Models[0].SupportsImages = false
				f.Config.Config().Providers.Set("anthropic", provider)
				attachments = []message.Attachment{{MimeType: "image/png", Content: []byte("image")}}
				want = agent.ErrBranchUnsupportedAttachment
			case "empty":
				prompt = ""
				want = agent.ErrEmptyPrompt
			}
			before, err := f.Messages.List(f.Context, s.ID)
			require.NoError(t, err)
			count := 0
			_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, prompt, agent.BranchRunOptions{Origin: origin, OnUserMessageCreated: func(message.Message) { count++ }}, attachments...)
			require.ErrorIs(t, err, want)
			require.Zero(t, count)
			after, e := f.Messages.List(f.Context, s.ID)
			require.NoError(t, e)
			require.Equal(t, before, after)
			current, e := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, e)
			require.Equal(t, source.ID, current.LeafMessageID)
			require.Empty(t, f.Provider.Requests())
		})
	}
}

func TestBranchOAuthOuterRetry(t *testing.T) {
	for _, mode := range []string{"success", "rejected", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			f := branchfixture.New(t)
			provider, _ := f.Config.Config().Providers.Get("anthropic")
			provider.APIKey = "Bearer stale-token"
			provider.OAuthToken = &oauth.Token{AccessToken: "stale-token", RefreshToken: "stale-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
			if mode == "continuation" {
				provider.Models[0].ContextWindow = 100
			}
			f.Config.Config().Providers.Set("anthropic", provider)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
			require.NoError(t, err)
			if mode == "continuation" {
				f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Text: "summary"})
			}
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "outer-token"})
			if mode == "rejected" {
				f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized, RefreshToken: "last-token"}, branchfixture.Response{Status: http.StatusUnauthorized})
			} else {
				f.Provider.Enqueue(branchfixture.Response{Text: "success"})
			}
			count := 0
			var accepted message.Message
			_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "UNIQUE_REQUEST", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(m message.Message) { accepted = m; count++ }})
			if mode == "rejected" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, count)
			current, e := f.Sessions.Get(f.Context, s.ID)
			require.NoError(t, e)
			path, e := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
			require.NoError(t, e)
			require.Equal(t, accepted.ID, path[0].ID)
			if mode == "continuation" {
				require.Equal(t, message.MessageTypeCompaction, path[len(path)-2].MessageType)
			} else {
				require.Equal(t, accepted.ID, path[len(path)-1].ParentMessageID)
			}
			requests := f.Provider.Requests()
			offset := 0
			if mode == "continuation" {
				offset = 2
			}
			require.Equal(t, "Bearer stale-token", requests[0].Authorization)
			require.Equal(t, "Bearer fresh-token", requests[offset+1].Authorization)
			for _, request := range requests {
				require.NotContains(t, request.Body, "EXCLUDED")
				require.LessOrEqual(t, strings.Count(request.Body, "UNIQUE_REQUEST"), 1)
			}
		})
	}
}

func TestBranchPreparationCancellation(t *testing.T) {
	f := branchfixture.New(t)
	mcp.ResetInitForTest()
	t.Cleanup(mcp.ResetInitForTest)
	mcp.ArmInit()
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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
	require.Equal(t, source.ID, current.LeafMessageID)
}

func TestBranchQueueCapturesModelSelection(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	require.NoError(t, f.Sessions.Rename(f.Context, s.ID, "custom", true))
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	started, release := make(chan struct{}), make(chan struct{})
	f.Provider.Enqueue(branchfixture.Response{Text: "branch", Started: started, Release: release})
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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
	for _, mode := range []string{"ordinary", "branch auto-summary"} {
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
			if mode == "branch auto-summary" {
				f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95})
				wantRequests, wantMessages = 2, 5
			}
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusBadRequest})
			if mode == "ordinary" {
				err = f.Coordinator.Summarize(f.Context, s.ID)
			} else {
				_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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

func TestBranchSummaryAuthRetry(t *testing.T) {
	f := branchfixture.New(t)
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`, InputTokens: 95}, branchfixture.Response{Status: http.StatusUnauthorized}, branchfixture.Response{Text: "summary"}, branchfixture.Response{Text: "continued"})
	count := 0
	_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(message.Message) { count++ }})
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
	require.Equal(t, 1, count)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Len(t, path, 5)
	require.Equal(t, message.MessageTypeCompaction, path[3].MessageType)
	all, err := f.Messages.List(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, all, 6)
}

func TestBranchUnsafeRetryPreservesToolEffects(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	f.Provider.Enqueue(branchfixture.Response{ToolName: "todos", ToolInput: `{"todos":[]}`}, branchfixture.Response{Status: http.StatusUnauthorized})
	count := 0
	_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(message.Message) { count++ }})
	require.ErrorIs(t, err, agent.ErrBranchUnsafePrefix)
	require.Equal(t, 1, count)
	require.Len(t, f.Provider.Requests(), 2)
	current, err := f.Sessions.Get(f.Context, s.ID)
	require.NoError(t, err)
	path, err := f.Messages.GetBranchPath(f.Context, current.LeafMessageID)
	require.NoError(t, err)
	require.Len(t, path, 4)
	require.Equal(t, message.Tool, path[2].Role)
}

func TestBranchRetryCleanupFailureStopsProvider(t *testing.T) {
	for _, operation := range []string{"UPDATE OF leaf_message_id ON sessions", "DELETE ON messages"} {
		t.Run(operation, func(t *testing.T) {
			f := branchfixture.New(t)
			s, err := f.Workspace.CreateSession(f.Context, "source")
			require.NoError(t, err)
			source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
			require.NoError(t, err)
			started, release := make(chan struct{}), make(chan struct{})
			f.Provider.Enqueue(branchfixture.Response{Status: http.StatusUnauthorized, Started: started, Release: release})
			done := make(chan error, 1)
			go func() {
				_, err := f.Coordinator.RunFromMessage(f.Context, s.ID, "branch", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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

func TestBranchTitleFallbackUsesSelectedPath(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "EXCLUDED"}}})
	require.NoError(t, err)
	f.Provider.Enqueue(branchfixture.Response{Text: "branch response"}, branchfixture.Response{Status: http.StatusBadRequest}, branchfixture.Response{Text: "title fallback"})
	_, err = f.Coordinator.RunFromMessage(f.Context, s.ID, "replacement", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}})
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

func TestBranchRechecksSourceImmediatelyBeforeInsertion(t *testing.T) {
	f := branchfixture.New(t)
	mcp.SetStateForTest("local", mcp.StateDeferred)
	t.Cleanup(func() { mcp.DeleteStateForTest("local") })
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	metadata, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.System, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{message.MCPToggleContent{ServerName: "local", Enabled: true}}})
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: metadata.ID})
	require.NoError(t, err)
	direct := agent.NewSessionAgent(agent.SessionAgentOptions{Sessions: f.Sessions, Messages: f.Messages, LargeModel: f.Coordinator.Model(), SmallModel: f.Coordinator.Model()})
	direct.SetConnectFn(func(ctx context.Context, name string) (int, error) {
		require.True(t, direct.IsSessionBusy(s.ID))
		require.Equal(t, "local", name)
		return 0, f.Sessions.MoveLeaf(ctx, s.ID, metadata.ID)
	})
	count := 0
	_, err = direct.Run(f.Context, agent.SessionAgentCall{SessionID: s.ID, Prompt: "branch", BranchOrigin: &agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(message.Message) { count++ }})
	require.ErrorIs(t, err, agent.ErrBranchStaleSource)
	require.Zero(t, count)
	require.Empty(t, f.Provider.Requests())
	all, err := f.Messages.List(f.Context, s.ID)
	require.NoError(t, err)
	require.Len(t, all, 2)
}
