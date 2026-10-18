package agent

import (
	"context"
	"errors"
	"iter"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

// billedToolModel streams one tool step that reports usage, then a final text
// step, so tests can see how a run accounts for the first step's cost.
type billedToolModel struct {
	scriptedModel
	steps atomic.Int32
}

func (m *billedToolModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	step := m.steps.Add(1) - 1
	return iter.Seq[fantasy.StreamPart](func(yield func(fantasy.StreamPart) bool) {
		var parts []fantasy.StreamPart
		if step == 0 {
			parts = []fantasy.StreamPart{
				{Type: fantasy.StreamPartTypeToolInputStart, ID: "call_0", ToolCallName: "work"},
				{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call_0"},
				{Type: fantasy.StreamPartTypeToolCall, ID: "call_0", ToolCallName: "work", ToolCallInput: "{}"},
				{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls, Usage: fantasy.Usage{InputTokens: 1_000_000}},
			}
		} else {
			parts = []fantasy.StreamPart{
				{Type: fantasy.StreamPartTypeTextStart, ID: "text"},
				{Type: fantasy.StreamPartTypeTextDelta, ID: "text", Delta: "done"},
				{Type: fantasy.StreamPartTypeTextEnd, ID: "text"},
				{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop},
			}
		}
		for _, p := range parts {
			if !yield(p) {
				return
			}
		}
	}), nil
}

func pricedAgent(env fakeEnv, sessions session.Service, model fantasy.LanguageModel, agentTools ...fantasy.AgentTool) SessionAgent {
	m := Model{
		Model:      model,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 10000, CostPer1MIn: 3},
	}
	return NewSessionAgent(SessionAgentOptions{
		LargeModel: m, SmallModel: m, SystemPrompt: "system", IsYolo: true,
		Sessions: sessions, Messages: env.messages, Tools: agentTools,
	})
}

func TestCancelledToolStepStillChargesItsReportedUsage(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Priced", t.TempDir())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	tool := fantasy.NewAgentTool("work", "Test tool.",
		func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			cancel()
			<-ctx.Done()
			return fantasy.ToolResponse{}, ctx.Err()
		})
	a := pricedAgent(env, env.sessions, &billedToolModel{}, tool)

	_, err = a.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.ErrorIs(t, err, context.Canceled)

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.InDelta(t, 3.0, got.Cost, 1e-9, "the provider already billed the step")
}

func TestFinishedStepIsChargedOnce(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Priced", t.TempDir())
	require.NoError(t, err)
	tool := stepTool("work", func(context.Context, int) fantasy.ToolResponse { return fantasy.NewTextResponse("ok") })
	a := pricedAgent(env, env.sessions, &billedToolModel{}, tool)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.NoError(t, err)

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.InDelta(t, 3.0, got.Cost, 1e-9)
}

// failingSaves fails every session save once armed.
type failingSaves struct {
	session.Service
	armed atomic.Bool
}

func (f *failingSaves) Save(ctx context.Context, s session.Session) (session.Session, error) {
	if f.armed.Load() {
		return session.Session{}, errors.New("disk full")
	}
	return f.Service.Save(ctx, s)
}

func TestStepFinishPersistenceFailureFailsTheRun(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Failing", t.TempDir())
	require.NoError(t, err)
	sessions := &failingSaves{Service: env.sessions}
	sessions.armed.Store(true)
	model := &scriptedModel{toolName: "work", toolSteps: 2}
	tool := stepTool("work", func(context.Context, int) fantasy.ToolResponse { return fantasy.NewTextResponse("ok") })
	a := pricedAgent(env, sessions, model, tool)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.ErrorIs(t, err, errLocalPersistence)
	require.ErrorContains(t, err, "disk full")
	require.Len(t, model.prompts, 1, "no further steps after the failed save")
}

func TestCompactionPersistenceFailureIsMarked(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Compacting", t.TempDir())
	require.NoError(t, err)
	sessions := &failingSaves{Service: env.sessions}
	a := pricedAgent(env, sessions, &scriptedModel{})
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.NoError(t, err)

	sessions.armed.Store(true)
	err = a.Summarize(t.Context(), sess.ID, nil)
	require.ErrorIs(t, err, errLocalPersistence)
}

// failingCostAdds fails cost additions once armed, after saves succeed.
type failingCostAdds struct {
	session.Service
	armed atomic.Bool
}

func (f *failingCostAdds) AddCost(ctx context.Context, id string, delta float64) error {
	if f.armed.Load() {
		return errors.New("disk full")
	}
	return f.Service.AddCost(ctx, id, delta)
}

func TestPartiallySavedStepIsNotCountedTwice(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Partial", t.TempDir())
	require.NoError(t, err)
	sessions := &failingCostAdds{Service: env.sessions}
	sessions.armed.Store(true)
	tool := stepTool("work", func(context.Context, int) fantasy.ToolResponse { return fantasy.NewTextResponse("ok") })
	a := pricedAgent(env, sessions, &billedToolModel{}, tool)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "start", NonInteractive: true})
	require.ErrorIs(t, err, errLocalPersistence)

	got, err := env.sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1_000_000), got.PromptTokens, "the step's tokens are saved once")
}
