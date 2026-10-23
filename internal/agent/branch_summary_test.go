package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
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
	f.setSummarizerAgent(newMockAgent(reloadTestProvider, 1000, run))
}

// setSummarizerAgent caches a as the built-in summarizer, which may use
// every read-only tool.
func (f branchFixture) setSummarizerAgent(a SessionAgent) {
	f.c.agents.Set(summarizerCacheKey(BuiltinBranchSummarizer, readOnlySummarizerTools), a)
}

// addSummarizer registers a marked summarizer that may use allowed.
func (f branchFixture) addSummarizer(name string, allowed []string) {
	f.c.orchestratorMu.Lock()
	defer f.c.orchestratorMu.Unlock()
	md := f.c.agentMDs[BuiltinBranchSummarizer]
	md.Name = name
	f.c.agentMDs[name] = md
	agentCfg := f.c.agentConfigs[BuiltinBranchSummarizer]
	agentCfg.ID, agentCfg.Name, agentCfg.AllowedTools = name, name, allowed
	f.c.agentConfigs[name] = agentCfg
}

// recordProviderRequests points the test provider at a server that
// records each request body and refuses it.
func (f branchFixture) recordProviderRequests(t *testing.T) <-chan string {
	t.Helper()
	bodies := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)
		http.Error(w, `{"error":{"message":"refused by test"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	providerCfg, ok := f.c.cfg.Config().Providers.Get(reloadTestProvider)
	require.True(t, ok)
	providerCfg.BaseURL, providerCfg.APIKey = server.URL, "test-key"
	// Room for the summarizer's real prompt and tools.
	providerCfg.Models = slices.Clone(providerCfg.Models)
	providerCfg.Models[0].ContextWindow = 100_000
	f.c.cfg.Config().Providers.Set(reloadTestProvider, providerCfg)
	return bodies
}

// extendBranch adds n long messages to the end of the abandoned branch.
func (f *branchFixture) extendBranch(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		role := message.User
		if i%2 == 1 {
			role = message.Assistant
		}
		m, err := f.env.messages.Create(t.Context(), f.sessionID, message.CreateMessageParams{
			Role: role, ParentMessageID: f.a2,
			Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf("step-%02d ", i) + strings.Repeat("word ", 1_400)}},
		})
		require.NoError(t, err)
		f.a2 = m.ID
	}
	require.NoError(t, f.env.sessions.MoveLeaf(t.Context(), f.sessionID, f.a1))
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

	require.Contains(t, prompt, "<message role=\"user\">\ndo item 1\n</message>")
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

func TestSummarizeBranch_RunsInTheChildSessionTheCallerChose(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	var ranIn string
	f.setSummarizer(func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		ranIn = call.SessionID
		return agentResultWithText("## Outcome\nItem 1 done."), nil
	})
	req := f.request()
	req.ChildSessionID = session.AgentToolSessionID(f.a1, "call-1")

	got, err := f.c.SummarizeBranch(t.Context(), req)
	require.NoError(t, err)

	require.Equal(t, req.ChildSessionID, ranIn)
	content, ok := got.Parts[0].(message.BranchSummaryContent)
	require.True(t, ok)
	require.Equal(t, req.ChildSessionID, content.ChildSessionID)
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
	done := summarizeAsync(t.Context(), f)
	<-started

	leafAtSend := f.submitLeafProbe(t)
	close(release)

	got := <-done
	require.NoError(t, got.err)
	leaf := <-leafAtSend
	require.NoError(t, leaf.err)
	require.Equal(t, got.summary.ID, leaf.id)
}

type summarizeResult struct {
	summary message.Message
	err     error
}

func summarizeAsync(ctx context.Context, f branchFixture) <-chan summarizeResult {
	done := make(chan summarizeResult, 1)
	go func() {
		m, err := f.c.SummarizeBranch(ctx, f.request())
		done <- summarizeResult{m, err}
	}()
	return done
}

type leafResult struct {
	id  string
	err error
}

// submitLeafProbe queues a send that reports the session's leaf when it
// runs.
func (f branchFixture) submitLeafProbe(t *testing.T) <-chan leafResult {
	t.Helper()
	leafAtSend := make(chan leafResult, 1)
	_, err := f.c.admission.submit(t.Context(), f.sessionID, submission{prompt: "next", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		sess, err := f.env.sessions.Get(ctx, f.sessionID)
		leafAtSend <- leafResult{sess.LeafMessageID, err}
		return nil, nil
	}})
	require.NoError(t, err)
	return leafAtSend
}

func TestSummarizeBranch_CancelLetsAQueuedSendRunAtTheTarget(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	started := make(chan struct{})
	f.setSummarizer(func(ctx context.Context, _ SessionAgentCall) (*fantasy.AgentResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := summarizeAsync(ctx, f)
	<-started
	leafAtSend := f.submitLeafProbe(t)

	cancel()

	require.ErrorIs(t, (<-done).err, context.Canceled)
	leaf := <-leafAtSend
	require.NoError(t, leaf.err)
	require.Equal(t, f.a1, leaf.id)
}

func TestSummarizeBranch_RefusesWhenTheLeafMovedBeforeItStarts(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	called := false
	f.setSummarizer(func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		called = true
		return agentResultWithText("summary"), nil
	})
	require.NoError(t, f.env.sessions.MoveLeaf(t.Context(), f.sessionID, f.a2))

	_, err := f.c.SummarizeBranch(t.Context(), f.request())

	require.ErrorIs(t, err, ErrStaleBranchSummary)
	require.False(t, called, "nothing is sent")
	children, err := f.env.messages.GetChildren(t.Context(), f.a1)
	require.NoError(t, err)
	require.Len(t, children, 1)
}

func TestSummarizeBranch_RunsWithOnlyReadOnlyTools(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		allowed     []string
		wantTools   []string
		unwantTools []string
	}{
		"read-only subset": {allowed: []string{"view", "grep", "bash", "edit"}, wantTools: []string{"view", "grep"}, unwantTools: []string{"bash", "edit", "glob", "ls"}},
		"every tool":       {allowed: nil, wantTools: []string{"view", "grep", "glob", "ls"}, unwantTools: []string{"bash", "edit", "write", "fetch"}},
		"no read-only":     {allowed: []string{"bash", "edit"}, unwantTools: []string{"bash", "edit", "view"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newBranchFixture(t)
			bodies := f.recordProviderRequests(t)
			f.addSummarizer("marked", tc.allowed)
			req := f.request()
			req.Agent = "marked"

			_, err := f.c.SummarizeBranch(t.Context(), req)

			require.ErrorContains(t, err, "refused by test")
			body := <-bodies
			for _, tool := range tc.wantTools {
				require.Contains(t, body, `"name":"`+tool+`"`)
			}
			for _, tool := range tc.unwantTools {
				require.NotContains(t, body, `"name":"`+tool+`"`)
			}
			if tc.wantTools == nil {
				require.NotContains(t, body, `"tools"`)
			}
		})
	}
}

func TestSummarizeBranch_FitsTheBranchInWhatTheAgentLeaves(t *testing.T) {
	t.Parallel()

	// A 40,000-token window, less 1,000 for output and 5,000 for the tool
	// loop, leaves 34,000 tokens, less the agent's measured overhead.
	tests := map[string]struct {
		overhead     int
		wantOmitted  bool
		minPromptLen int
		maxPromptLen int
	}{
		"no overhead": {overhead: 0, maxPromptLen: 34_000 * 3},
		// Within one message of the 14,000 tokens left.
		"measured overhead": {overhead: 60_000, wantOmitted: true, minPromptLen: 14_000*3 - 8_000, maxPromptLen: 14_000 * 3},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newBranchFixture(t)
			f.extendBranch(t, 10)
			var prompt string
			summarizer := newMockAgent(reloadTestProvider, 1_000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				prompt = call.Prompt
				return agentResultWithText("summary"), nil
			})
			summarizer.model.CatwalkCfg.ContextWindow = 40_000
			summarizer.overhead = tc.overhead
			f.setSummarizerAgent(summarizer)

			_, err := f.c.SummarizeBranch(t.Context(), f.request())

			require.NoError(t, err)
			require.Contains(t, prompt, "step-09")
			require.Equal(t, tc.wantOmitted, strings.Contains(prompt, "earlier messages omitted"))
			require.LessOrEqual(t, len(prompt), tc.maxPromptLen)
			require.GreaterOrEqual(t, len(prompt), tc.minPromptLen)
		})
	}
}

func TestSummarizeBranch_StoredSummaryReachesTheConversationAfterIt(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("## Outcome\nItem 1 done."))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	returned, err := f.env.messages.Create(t.Context(), f.sessionID, message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "carry on"}}, ParentMessageID: summary.ID,
	})
	require.NoError(t, err)
	path, err := f.env.messages.GetBranchPath(t.Context(), returned.ID)
	require.NoError(t, err)

	got := message.FilterBranchPathForContext(path)

	stored := summary.Parts[0].(message.BranchSummaryContent).Summary
	require.Len(t, got, 4)
	require.Contains(t, got[2].Content().Text, stored)
	require.Equal(t, "carry on", got[3].Content().Text)
}

func TestDiscardBranchSummary_WaitsForASendInFlight(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)

	readLeaf, proceed := make(chan struct{}), make(chan struct{})
	sent := make(chan error, 1)
	// An idle session runs a send at once, so submit returns only after it.
	go func() {
		_, err := f.c.admission.submit(t.Context(), f.sessionID, submission{prompt: "next", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			sess, err := f.env.sessions.Get(ctx, f.sessionID)
			if err != nil {
				return nil, err
			}
			close(readLeaf)
			<-proceed
			_, err = f.env.messages.Create(ctx, f.sessionID, message.CreateMessageParams{
				Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "next"}}, ParentMessageID: sess.LeafMessageID,
			})
			return nil, err
		}})
		sent <- err
	}()
	select {
	case <-readLeaf:
	case err := <-sent:
		t.Fatalf("send ended before reading the leaf: %v", err)
	}

	discarded := make(chan error, 1)
	go func() { discarded <- f.c.DiscardBranchSummary(t.Context(), f.sessionID, summary.ID) }()
	select {
	case err := <-discarded:
		t.Fatalf("discard returned while a send was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(proceed)

	require.NoError(t, <-sent)
	require.ErrorIs(t, <-discarded, ErrBranchSummaryInUse)
	children, err := f.env.messages.GetChildren(t.Context(), f.a1)
	require.NoError(t, err)
	require.Contains(t, ids(children), summary.ID)
	after, err := f.env.messages.GetChildren(t.Context(), summary.ID)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, "next", after[0].Content().Text)
}

func ids(msgs []message.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func TestSummarizeBranch_StaleCompletionStoresNothing(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	var child string
	f.setSummarizer(func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		child = call.SessionID
		require.NoError(t, f.env.sessions.MoveLeaf(ctx, f.sessionID, f.a2))
		return agentResultWithText("summary"), nil
	})
	req := f.request()

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
	var child string
	f.setSummarizer(func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		child = call.SessionID
		cancel()
		return nil, ctx.Err()
	})
	req := f.request()

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
	for _, name := range []string{"a-summarizer", "b-unresolvable", "c-summarizer"} {
		md := f.c.agentMDs[BuiltinBranchSummarizer]
		md.Name = name
		f.c.agentMDs[name] = md
		f.c.agentConfigs[name] = f.c.agentConfigs[BuiltinBranchSummarizer]
	}
	unresolvable := f.c.agentConfigs["b-unresolvable"]
	unresolvable.Model = "missing/model"
	f.c.agentConfigs["b-unresolvable"] = unresolvable
	// An ordinary agent that sorts before every summarizer: listing must
	// skip it, not stop at it.
	f.c.agentMDs["0-helper"] = prompt.AgentMD{Name: "0-helper"}
	f.c.agentConfigs["0-helper"] = f.c.agentConfigs[BuiltinBranchSummarizer]
	f.c.orchestratorMu.Unlock()

	require.Equal(t, []string{BuiltinBranchSummarizer, "a-summarizer", "c-summarizer"}, f.c.BranchSummarizers())
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

func TestReserveSession_QueuesSendsAtOnce(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)

	release := f.c.ReserveSession(f.sessionID)
	ran := make(chan struct{})
	_, err := f.c.admission.submit(t.Context(), f.sessionID, submission{prompt: "next", run: func(context.Context) (*fantasy.AgentResult, error) {
		close(ran)
		return nil, nil
	}})
	require.NoError(t, err)
	select {
	case <-ran:
		t.Fatal("a send ran while the session was reserved")
	default:
	}

	release()
	<-ran
}

// waitForLeafUpdate waits for an update to sessionID that shows leaf.
func waitForLeafUpdate(t *testing.T, events <-chan pubsub.Event[session.Session], sessionID, leaf string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == pubsub.UpdatedEvent && ev.Payload.ID == sessionID && ev.Payload.LeafMessageID == leaf {
				return
			}
		case <-timeout:
			t.Fatalf("no session update with leaf %s", leaf)
		}
	}
}

func TestSummarizeBranch_PublishesTheNewLeaf(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	events := f.env.sessions.Subscribe(t.Context())

	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)

	waitForLeafUpdate(t, events, f.sessionID, summary.ID)
}

func TestDiscardBranchSummary_PublishesTheRestoredLeaf(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	events := f.env.sessions.Subscribe(t.Context())

	require.NoError(t, f.c.DiscardBranchSummary(t.Context(), f.sessionID, summary.ID))

	waitForLeafUpdate(t, events, f.sessionID, f.a1)
}

func TestSummarizeBranch_RefusesWhenTheAgentLeavesNoRoom(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	called := false
	summarizer := newMockAgent(reloadTestProvider, 1000, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		called = true
		return agentResultWithText("summary"), nil
	})
	summarizer.overhead = 1_000_000
	f.setSummarizerAgent(summarizer)

	_, err := f.c.SummarizeBranch(t.Context(), f.request())

	require.ErrorContains(t, err, "leave only")
	require.False(t, called, "nothing is sent")
}

func TestSummarizeBranch_RefusesAgentsThatAreNotSummarizers(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	req := f.request()
	req.Agent = "helper"

	_, err := f.c.SummarizeBranch(t.Context(), req)

	require.ErrorIs(t, err, ErrNotABranchSummarizer)
}

func TestBranchWorthSummarizing(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)

	worth, err := f.c.BranchWorthSummarizing(t.Context(), f.a2, f.a1)
	require.NoError(t, err)
	require.True(t, worth)

	worth, err = f.c.BranchWorthSummarizing(t.Context(), f.a1, f.a1)
	require.NoError(t, err)
	require.False(t, worth)
}

func TestSummarizeBranch_KeepsNoToolLoopRoomForAnAgentWithoutTools(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.addSummarizer("toolless", []string{"bash"})
	// A 5,000-token window less 1,000 for output leaves exactly the least
	// worth sending, so any room kept for a tool loop would refuse it.
	summarizer := newMockAgent(reloadTestProvider, 1_000, reply("summary"))
	summarizer.model.CatwalkCfg.ContextWindow = 5_000
	f.c.agents.Set(summarizerCacheKey("toolless", []string{}), summarizer)
	req := f.request()
	req.Agent = "toolless"

	_, err := f.c.SummarizeBranch(t.Context(), req)

	require.NoError(t, err)
}

func TestSummarizerBudget(t *testing.T) {
	t.Parallel()
	model := Model{CatwalkCfg: catwalk.Model{ID: "m", ContextWindow: 5_000, DefaultMaxTokens: 1_000}}

	budget, err := summarizerBudget(model, 0, false)
	require.NoError(t, err)
	require.Equal(t, minSummarizerInputTokens, budget)

	_, err = summarizerBudget(model, 1, false)
	require.ErrorContains(t, err, "leave only 3999 of the 5000-token window")
}

func TestDiscardBranchSummary_RefusesOtherMessages(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	f.setSummarizer(reply("summary"))
	summary, err := f.c.SummarizeBranch(t.Context(), f.request())
	require.NoError(t, err)
	other, err := f.env.sessions.Create(t.Context(), "Other", t.TempDir())
	require.NoError(t, err)

	tests := map[string]struct{ sessionID, messageID string }{
		"not a summary":     {f.sessionID, f.a2},
		"another's summary": {other.ID, summary.ID},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := f.c.DiscardBranchSummary(t.Context(), tc.sessionID, tc.messageID)

			require.ErrorContains(t, err, "is not a branch summary")
			_, err = f.env.messages.Get(t.Context(), tc.messageID)
			require.NoError(t, err)
		})
	}
}

func TestDelegatedMessages_LoadsOnlyDelegatingCalls(t *testing.T) {
	t.Parallel()
	f := newBranchFixture(t)
	for _, call := range []string{"task", "fetch", "other"} {
		child, err := f.env.sessions.CreateTaskSession(t.Context(), session.AgentToolSessionID(f.a2, call), f.sessionID, call)
		require.NoError(t, err)
		_, err = f.env.messages.Create(t.Context(), child.ID, message.CreateMessageParams{
			Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "from " + call}},
		})
		require.NoError(t, err)
	}

	tests := map[string]struct {
		call message.ToolCall
		want string
	}{
		"task":          {message.ToolCall{ID: "task", Name: TaskToolName}, "from task"},
		"agentic fetch": {message.ToolCall{ID: "fetch", Name: tools.AgenticFetchToolName}, "from fetch"},
		"other tool":    {message.ToolCall{ID: "other", Name: "mcp_other_run"}, ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := f.c.delegatedMessages(t.Context(), f.a2, tc.call)

			if tc.want == "" {
				require.Nil(t, got)
				return
			}
			require.Len(t, got, 1)
			require.Equal(t, tc.want, got[0].Content().Text)
		})
	}
}

// Failing to publish or delete only logs, so the warning is all there is
// to see. Not parallel: it captures the default logger.
func TestBranchSummaryHousekeepingWarnsOnlyOnFailure(t *testing.T) {
	f := newBranchFixture(t)
	child, err := f.env.sessions.CreateTaskSession(t.Context(), "child", f.sessionID, "child")
	require.NoError(t, err)
	logs := captureLogs(t)

	f.c.refreshSession(f.sessionID)
	f.c.deleteChildSession(child.ID)
	require.Empty(t, logs.String())
	_, err = f.env.sessions.Get(t.Context(), child.ID)
	require.Error(t, err, "the child session is deleted")

	f.c.refreshSession("missing")
	f.c.deleteChildSession("missing")
	require.Contains(t, logs.String(), "Failed to publish session after a branch summary change")
	require.Contains(t, logs.String(), "Failed to delete branch summary session")
}
