package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

func requireKind(doc json.RawMessage) error {
	var v struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(doc, &v); err != nil {
		return err
	}
	if v.Kind == "" {
		return errors.New("kind is required")
	}
	return nil
}

var kindSchema = json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string"}},"required":["kind"]}`)

func newParent(t *testing.T, f *branchfixture.Fixture) string {
	t.Helper()
	s, err := f.Workspace.CreateSession(f.Context, "parent")
	require.NoError(t, err)
	return s.ID
}

func TestWorkflowAgentReturnsSubmittedResult(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "submit_result", ToolInput: `{"kind":"clean"}`},
		branchfixture.Response{Text: "submitted"},
	)
	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "review", Schema: kindSchema, Validate: requireKind,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"clean"}`, string(out.Output))
	require.Contains(t, f.Provider.Requests()[0].Body, `"submit_result"`)
}

func TestWorkflowAgentRejectsInvalidSubmission(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "submit_result", ToolInput: `{"other":1}`},
		branchfixture.Response{Text: "gave up"},
		branchfixture.Response{ToolName: "submit_result", ToolInput: `{"kind":"changes"}`},
		branchfixture.Response{Text: "submitted"},
	)
	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "review", Schema: kindSchema, Validate: requireKind,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"kind":"changes"}`, string(out.Output))
	requests := f.Provider.Requests()
	require.True(t, strings.Contains(requests[1].Body, `"is_error":true`), "invalid submission must go back as a tool error")
	require.Len(t, requests, 4)
}

func TestWorkflowAgentNudgesOnceThenReturnsNoOutput(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(
		branchfixture.Response{Text: "looks clean to me"},
		branchfixture.Response{Text: "still prose"},
	)
	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "review", Schema: kindSchema, Validate: requireKind,
	})
	require.NoError(t, err)
	require.Nil(t, out.Output)
	require.Equal(t, "still prose", out.Text)
	require.Len(t, f.Provider.Requests(), 2)
}

func TestWorkflowAgentRestrictsTools(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})
	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "look", Tools: []string{"view", "grep"},
	})
	require.NoError(t, err)
	require.Equal(t, "done", out.Text)
	body := f.Provider.Requests()[0].Body
	require.Contains(t, body, `"name":"view"`)
	require.Contains(t, body, `"name":"grep"`)
	require.False(t, strings.Contains(body, `"name":"bash"`), "bash must not be offered")
}

func TestWorkflowAgentFailsClosedOnUnknownTool(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	_, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "look", Tools: []string{"view", "telepathy"},
	})
	require.ErrorContains(t, err, "telepathy")
	require.Empty(t, f.Provider.Requests())
}

func TestWorkflowAgentRejectsUnknownAgentAndModel(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	_, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{ParentSessionID: parent, Agent: "ghost", Prompt: "x"})
	require.ErrorContains(t, err, "ghost")
	_, err = f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{ParentSessionID: parent, Agent: "reviewer", Prompt: "x", Model: "nope/nope"})
	require.ErrorContains(t, err, "nope/nope")
	require.Empty(t, f.Provider.Requests())
}

func TestWorkflowAgentUsesTheGivenSessionID(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})
	out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
		ParentSessionID: parent, Agent: "reviewer", Prompt: "look", SessionID: "child-123",
	})
	require.NoError(t, err)
	require.Equal(t, "child-123", out.SessionID)
	child, err := f.Workspace.GetSession(f.Context, "child-123")
	require.NoError(t, err)
	require.Equal(t, parent, child.ParentSessionID)
}

func TestWorkflowAgentGeneratesASessionIDByDefault(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	parent := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"}, branchfixture.Response{Text: "done"})
	call := agent.WorkflowAgentCall{ParentSessionID: parent, Agent: "reviewer", Prompt: "look"}
	first, err := f.Coordinator.RunWorkflowAgent(f.Context, call)
	require.NoError(t, err)
	second, err := f.Coordinator.RunWorkflowAgent(f.Context, call)
	require.NoError(t, err)
	require.NotEmpty(t, first.SessionID)
	require.NotEqual(t, first.SessionID, second.SessionID)
}

func TestWorkflowAgentCancelStopsContinuationAfterCompaction(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))
	provider, _ := f.Config.Config().Providers.Get("anthropic")
	provider.Models[0].ContextWindow = 100
	f.Config.Config().Providers.Set("anthropic", provider)
	parent := newParent(t, f)
	continued := make(chan struct{})
	f.Provider.Enqueue(
		branchfixture.Response{ToolName: "grep", ToolInput: `{"pattern":"x"}`, InputTokens: 95},
		branchfixture.Response{Text: "summary"},
		branchfixture.Response{Started: continued, Release: make(chan struct{})},
	)
	ctx, cancel := context.WithCancel(f.Context)
	defer cancel()
	sessionID := "workflow-agent-compaction"
	done := make(chan error, 1)
	go func() {
		_, err := f.Coordinator.RunWorkflowAgent(ctx, agent.WorkflowAgentCall{
			ParentSessionID: parent, Agent: "reviewer", Prompt: "review", SessionID: sessionID,
		})
		done <- err
	}()
	select {
	case <-continued:
	case <-f.Context.Done():
		t.Fatal("continuation never started")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("RunWorkflowAgent kept waiting after cancel")
	}
	require.Eventually(t, func() bool { return !f.Coordinator.IsSessionBusy(sessionID) }, 5*time.Second, 10*time.Millisecond)
}
