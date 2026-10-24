package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

type resumeFixture struct {
	c      *coordinator
	env    fakeEnv
	parent session.Session
	ctx    context.Context
	task   fantasy.AgentTool
	calls  chan SessionAgentCall
	agent  *mockSessionAgent
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)
	f := &resumeFixture{c: c, env: env, parent: parent, calls: make(chan SessionAgentCall, 16)}
	f.agent = newMockAgent(reloadTestProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		f.calls <- call
		return agentResultWithText("ok"), nil
	})
	f.agent.model.ModelCfg.Model = reloadTestModel
	c.agents.Set("helper|2", f.agent)
	f.ctx = context.WithValue(t.Context(), tools.SessionIDContextKey, parent.ID)
	f.ctx = context.WithValue(f.ctx, tools.MessageIDContextKey, "msg-1")
	f.task, err = c.taskTool(f.ctx, "orchestrator", 3)
	require.NoError(t, err)
	return f
}

func (f *resumeFixture) run(t *testing.T, callID string, params TaskParams) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := f.task.Run(f.ctx, fantasy.ToolCall{ID: callID, Name: TaskToolName, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func handleOf(t *testing.T, content string) string {
	t.Helper()
	_, handle, ok := strings.Cut(content, "subagent_id: ")
	require.True(t, ok, content)
	return handle
}

func TestTaskResumeContinuesTheSameChildAsAnOrchestratorInjection(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)

	first := f.run(t, "call-1", TaskParams{Prompt: "investigate", SubagentType: "helper"})
	handle := handleOf(t, first.Content)
	firstCall := <-f.calls

	second := f.run(t, "call-2", TaskParams{Prompt: "now fix it", Resume: handle, Description: "fix"})
	require.False(t, second.IsError, second.Content)
	resumed := <-f.calls

	require.Equal(t, firstCall.SessionID, resumed.SessionID)
	require.Equal(t, []message.InjectionContent{{
		Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, Text: "now fix it", CallID: "msg-1$$call-2",
	}}, resumed.Injections)
	require.Equal(t, handle, handleOf(t, second.Content))

	link, err := f.env.sessions.GetSubagentCall(t.Context(), "msg-1$$call-2")
	require.NoError(t, err)
	require.Equal(t, firstCall.SessionID, link.SessionID)
	require.Equal(t, session.SubagentCallResume, link.Kind)
}

func TestTaskResumeAcceptsMatchingTypeAndModel(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	handle := handleOf(t, f.run(t, "call-1", TaskParams{Prompt: "p", SubagentType: "helper"}).Content)
	<-f.calls

	resp := f.run(t, "call-2", TaskParams{
		Prompt: "more", Resume: handle, SubagentType: "helper", Model: reloadTestProvider + "/" + reloadTestModel,
	})
	require.False(t, resp.IsError, resp.Content)
}

func TestTaskResumeRejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		params  func(handle string) TaskParams
		prepare func(t *testing.T, f *resumeFixture, handle string)
		want    string
	}{
		"unknown handle": {
			params: func(string) TaskParams { return TaskParams{Prompt: "p", Resume: "helper-ffff"} },
			want:   `unknown subagent_id "helper-ffff"`,
		},
		"handle of another session's subagent": {
			params: func(handle string) TaskParams { return TaskParams{Prompt: "p", Resume: handle} },
			prepare: func(t *testing.T, f *resumeFixture, _ string) {
				other, err := f.env.sessions.Create(t.Context(), "Other", t.TempDir())
				require.NoError(t, err)
				f.ctx = context.WithValue(f.ctx, tools.SessionIDContextKey, other.ID)
			},
			want: "unknown subagent_id",
		},
		"child without stored identity": {
			params: func(string) TaskParams { return TaskParams{Prompt: "p", Resume: "old-msg$$old-call"} },
			prepare: func(t *testing.T, f *resumeFixture, _ string) {
				_, err := f.env.sessions.CreateTaskSession(t.Context(), "old-msg$$old-call", f.parent.ID, "old")
				require.NoError(t, err)
			},
			want: "was created before subagents could be resumed",
		},
		"mismatched type": {
			params: func(handle string) TaskParams {
				return TaskParams{Prompt: "p", Resume: handle, SubagentType: "explorer"}
			},
			want: `is a "helper" subagent, not "explorer"`,
		},
		"mismatched model": {
			params: func(handle string) TaskParams {
				return TaskParams{Prompt: "p", Resume: handle, Model: "other/model"}
			},
			want: "omit model to resume it",
		},
		"agent no longer configured": {
			params: func(handle string) TaskParams { return TaskParams{Prompt: "p", Resume: handle} },
			prepare: func(t *testing.T, f *resumeFixture, _ string) {
				f.c.orchestratorMu.Lock()
				delete(f.c.agentConfigs, "helper")
				f.c.orchestratorMu.Unlock()
			},
			want: `agent "helper" is no longer configured`,
		},
		"model no longer available": {
			params: func(handle string) TaskParams { return TaskParams{Prompt: "p", Resume: handle} },
			prepare: func(t *testing.T, f *resumeFixture, _ string) {
				f.c.cfg.Config().Providers.Del(reloadTestProvider)
			},
			want: "is no longer available",
		},
		"running subagent": {
			params: func(handle string) TaskParams { return TaskParams{Prompt: "p", Resume: handle} },
			prepare: func(t *testing.T, f *resumeFixture, handle string) {
				sub, err := f.env.sessions.GetSubagentByHandle(t.Context(), f.parent.ID, handle)
				require.NoError(t, err)
				_, err = f.c.leases.acquire(sub.SessionID, "msg-9$$call-9", func(error) {})
				require.NoError(t, err)
			},
			want: "returns.\n\nsubagent_id: helper-",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newResumeFixture(t)
			handle := handleOf(t, f.run(t, "call-1", TaskParams{Prompt: "p", SubagentType: "helper"}).Content)
			<-f.calls
			if tc.prepare != nil {
				tc.prepare(t, f, handle)
			}
			resp := f.run(t, "call-2", tc.params(handle))
			require.True(t, resp.IsError, resp.Content)
			require.Contains(t, resp.Content, tc.want)
		})
	}
}

func TestParallelResumesOfOneHandleHaveOneWinner(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	handle := handleOf(t, f.run(t, "call-1", TaskParams{Prompt: "p", SubagentType: "helper"}).Content)
	<-f.calls

	release := make(chan struct{})
	f.agent.runFunc = func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
		<-release
		return agentResultWithText("ok"), nil
	}
	results := make(chan fantasy.ToolResponse, 2)
	for _, id := range []string{"call-ra", "call-rb"} {
		go func() { results <- f.run(t, id, TaskParams{Prompt: "p", Resume: handle}) }()
	}
	// The loser returns at once while the winner waits on release.
	loser := <-results
	close(release)
	winner := <-results
	require.True(t, loser.IsError)
	require.Contains(t, loser.Content, "is running")
	require.False(t, winner.IsError, winner.Content)
}

func TestTaskResumeOfAnotherSessionsChildIsUnknown(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	other, err := f.env.sessions.Create(t.Context(), "Other", t.TempDir())
	require.NoError(t, err)
	_, err = f.env.sessions.CreateTaskSession(t.Context(), "x$$y", other.ID, "foreign")
	require.NoError(t, err)

	resp := f.run(t, "call-1", TaskParams{Prompt: "p", Resume: "x$$y"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "unknown subagent_id")
}

func TestTaskResumeKeepsAStoredNonDefaultModel(t *testing.T) {
	t.Parallel()
	f := newResumeFixture(t)
	prov, _ := f.c.cfg.Config().Providers.Get(reloadTestProvider)
	prov.Models = append(prov.Models, catwalk.Model{ID: "model-2", ContextWindow: 10000, DefaultMaxTokens: 1000})
	f.c.cfg.Config().Providers.Set(reloadTestProvider, prov)
	override := reloadTestProvider + "/model-2"
	overrideCalls := make(chan SessionAgentCall, 4)
	other := newMockAgent(reloadTestProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		overrideCalls <- call
		return agentResultWithText("ok"), nil
	})
	other.model.ModelCfg.Model = "model-2"
	f.c.agents.Set("helper|2|"+override, other)

	handle := handleOf(t, f.run(t, "call-1", TaskParams{Prompt: "p", SubagentType: "helper", Model: override}).Content)
	<-overrideCalls

	resp := f.run(t, "call-2", TaskParams{Prompt: "more", Resume: handle})
	require.False(t, resp.IsError, resp.Content)
	select {
	case <-overrideCalls:
	default:
		t.Fatal("the resume didn't run on the subagent's stored model")
	}
	require.Empty(t, f.calls, "the default-model agent never ran")
}
