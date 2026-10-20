package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// branchFixture is a session whose conversation went u1, a1, then a
// tangent u2, a2 that the user has rewound from back to a1.
type branchFixture struct {
	c         *coordinator
	env       fakeEnv
	sessionID string
	a1, a2    string
}

func newBranchFixture(t *testing.T) branchFixture {
	t.Helper()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	sess, err := env.sessions.Create(t.Context(), "Root", t.TempDir())
	require.NoError(t, err)

	create := func(parent string, role message.MessageRole, parts ...message.ContentPart) string {
		m, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
			Role: role, Parts: parts, ParentMessageID: parent,
		})
		require.NoError(t, err)
		return m.ID
	}
	u1 := create("", message.User, message.TextContent{Text: "plan the work"})
	a1 := create(u1, message.Assistant, message.TextContent{Text: "Plan: 1, 2, 3."})
	u2 := create(a1, message.User, message.TextContent{Text: "do item 1"})
	a2 := create(u2, message.Assistant,
		message.TextContent{Text: "Done item 1."},
		message.ToolCall{ID: "commit", Name: "bash", Input: `{"command":"git commit -m item1"}`, Finished: true},
	)
	tool := create(a2, message.Tool, message.ToolResult{ToolCallID: "commit", Name: "bash", Content: "[main 1a2b3c4] item1"})
	require.NoError(t, env.sessions.MoveLeaf(t.Context(), sess.ID, a1))
	return branchFixture{c: c, env: env, sessionID: sess.ID, a1: a1, a2: tool}
}

func (f branchFixture) setSummarizer(run func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)) {
	f.c.agents.Set(BuiltinBranchSummarizer+"|2", newMockAgent(reloadTestProvider, 1000, run))
}

func (f branchFixture) request() BranchSummaryRequest {
	return BranchSummaryRequest{SessionID: f.sessionID, FromLeafID: f.a2, TargetID: f.a1, Agent: BuiltinBranchSummarizer}
}

func reply(text string) func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
	return func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		return agentResultWithText(text), nil
	}
}

func TestSummarizeBranch_StoresSummaryUnderTarget(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	var prompt string
	f.setSummarizer(func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		prompt = call.Prompt
		return agentResultWithText("## Outcome\nItem 1 done."), nil
	})

	got, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)

	require.Contains(t, prompt, "[User]: do item 1")
	require.NotContains(t, prompt, "plan the work")
	require.Equal(t, f.a1, got.ParentMessageID)
	require.Equal(t, message.MessageTypeBranchSummary, got.MessageType)
	content, ok := got.Parts[0].(message.BranchSummaryContent)
	require.True(t, ok)
	require.Contains(t, content.Summary, "Item 1 done.")
	require.Equal(t, f.a2, content.FromID)
	require.Equal(t, BuiltinBranchSummarizer, content.Agent)
	require.NotEmpty(t, content.ChildSessionID)
	require.Equal(t, []string{"1a2b3c4"}, content.Ledger.Events[0].Refs)

	sess, err := f.env.sessions.Get(t.Context(), f.sessionID)
	require.NoError(t, err)
	require.Equal(t, got.ID, sess.LeafMessageID)
}

func TestSummarizeBranch_SendDuringSummaryRunsAfterIt(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	release := make(chan struct{})
	started := make(chan struct{})
	f.setSummarizer(func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		close(started)
		<-release
		return agentResultWithText("summary"), nil
	})
	done := make(chan message.Message, 1)
	go func() {
		m, err := f.c.SummarizeBranch(t.Context(), f.request())
		require.NoError(t, err)
		done <- m
	}()
	<-started

	leafAtSend := make(chan string, 1)
	_, err := f.c.admission.submit(t.Context(), f.sessionID, submission{prompt: "next", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		sess, err := f.env.sessions.Get(ctx, f.sessionID)
		require.NoError(t, err)
		leafAtSend <- sess.LeafMessageID
		return nil, nil
	}})
	require.NoError(t, err)
	close(release)

	summary := <-done
	require.Equal(t, summary.ID, <-leafAtSend)
}

func TestSummarizeBranch_StaleCompletionStoresNothing(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
		require.NoError(t, f.env.sessions.MoveLeaf(ctx, f.sessionID, f.a2))
		return agentResultWithText("summary"), nil
	})
	child := "summary-child"
	req := f.request()
	req.ChildSessionID = child

	_, err := f.c.SummarizeBranch(t.Context(), req)

	require.ErrorIs(t, err, ErrStaleBranchSummary)
	children, err := f.env.messages.GetChildren(t.Context(), f.a1)
	require.NoError(t, err)
	require.Len(t, children, 1)
	_, err = f.env.sessions.Get(t.Context(), child)
	require.Error(t, err)
}

func TestSummarizeBranch_CancelStoresNothing(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	f.setSummarizer(func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
		cancel()
		return nil, ctx.Err()
	})
	child := "summary-child"
	req := f.request()
	req.ChildSessionID = child

	_, err := f.c.SummarizeBranch(ctx, req)

	require.ErrorIs(t, err, context.Canceled)
	sess, err := f.env.sessions.Get(t.Context(), f.sessionID)
	require.NoError(t, err)
	require.Equal(t, f.a1, sess.LeafMessageID)
	_, err = f.env.sessions.Get(t.Context(), child)
	require.Error(t, err)
	require.Zero(t, f.c.admission.held[f.sessionID])
}

func TestSummarizeBranch_NothingToSummarize(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	req := f.request()
	req.FromLeafID = f.a1

	_, err := f.c.SummarizeBranch(t.Context(), req)

	require.ErrorIs(t, err, ErrNothingToSummarize)
}

func TestDiscardBranchSummary(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	child := summary.Parts[0].(message.BranchSummaryContent).ChildSessionID

	require.NoError(t, f.c.DiscardBranchSummary(t.Context(), f.sessionID, summary.ID))

	sess, err := f.env.sessions.Get(t.Context(), f.sessionID)
	require.NoError(t, err)
	require.Equal(t, f.a1, sess.LeafMessageID)
	_, err = f.env.messages.Get(t.Context(), summary.ID)
	require.Error(t, err)
	_, err = f.env.sessions.Get(t.Context(), child)
	require.Error(t, err)
}

func TestDiscardBranchSummary_RefusedOnceUsed(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	_, err = f.env.messages.Create(t.Context(), f.sessionID, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "next"}}, ParentMessageID: summary.ID,
	})
	require.NoError(t, err)

	err = f.c.DiscardBranchSummary(t.Context(), f.sessionID, summary.ID)

	require.ErrorIs(t, err, ErrBranchSummaryInUse)
	_, err = f.env.messages.Get(t.Context(), summary.ID)
	require.NoError(t, err)
}

func TestBranchSummarizers_ListsMarkedAgentsBuiltinFirst(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.c.orchestratorMu.Lock()
	md := f.c.agentMDs[BuiltinBranchSummarizer]
	md.Name = "a-summarizer"
	f.c.agentMDs["a-summarizer"] = md
	f.c.agentConfigs["a-summarizer"] = f.c.agentConfigs[BuiltinBranchSummarizer]
	f.c.orchestratorMu.Unlock()

	require.Equal(t, []string{BuiltinBranchSummarizer, "a-summarizer"}, f.c.BranchSummarizers())
}

func TestDiscardBranchSummary_AfterReturningToTheOldBranch(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	require.NoError(t, f.env.sessions.MoveLeaf(t.Context(), f.sessionID, f.a2))

	require.NoError(t, f.c.DiscardBranchSummary(t.Context(), f.sessionID, summary.ID))

	sess, err := f.env.sessions.Get(t.Context(), f.sessionID)
	require.NoError(t, err)
	require.Equal(t, f.a2, sess.LeafMessageID)
	_, err = f.env.messages.Get(t.Context(), summary.ID)
	require.Error(t, err)
}
