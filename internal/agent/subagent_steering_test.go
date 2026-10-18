package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

const steerText = `<injected_message source="human">use the v2 API</injected_message>`

func steeredAgentEnv(t *testing.T) (fakeEnv, string, *subagentLease) {
	t.Helper()
	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "Steered", t.TempDir())
	require.NoError(t, err)
	var leases subagentLeases
	lease, err := leases.acquire(sess.ID, "msg$$call", func(error) {})
	require.NoError(t, err)
	return env, sess.ID, lease
}

func countWith(prompt []fantasy.Message, text string) int {
	n := 0
	for _, msg := range prompt {
		if msg.Role == fantasy.MessageRoleUser && strings.Contains(textOf(msg), text) {
			n++
		}
	}
	return n
}

func TestSteerReachesTheNextStepAndStaysInLaterOnes(t *testing.T) {
	t.Parallel()
	env, sessionID, lease := steeredAgentEnv(t)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 0 {
			require.NoError(t, lease.steer("use the v2 API", false))
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 3}
	a := jobEventsAgent(env, env.messages, model, nil, tool)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := a.Run(ctx, SessionAgentCall{SessionID: sessionID, Prompt: "start", NonInteractive: true, steering: lease})
	require.NoError(t, err)

	require.Len(t, model.prompts, 4)
	require.Zero(t, countWith(model.prompts[0], steerText))
	for step := 1; step < 4; step++ {
		require.Equal(t, 1, countWith(model.prompts[step], steerText), "step %d", step)
	}
	require.Empty(t, lease.takePending())

	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var injections []message.InjectionContent
	for _, msg := range msgs {
		if msg.MessageType == message.MessageTypeSubagentInjection {
			injections = append(injections, msg.Injections()...)
		}
	}
	require.Equal(t, []message.InjectionContent{{
		Source: message.InjectionSourceHuman, Kind: message.InjectionKindSteer, Text: "use the v2 API", CallID: "msg$$call",
	}}, injections)
}

func TestSteerKeepsTheEarlierRequestPrefixByteIdentical(t *testing.T) {
	t.Parallel()
	env, sessionID, lease := steeredAgentEnv(t)

	tool := stepTool("work", func(_ context.Context, call int) fantasy.ToolResponse {
		if call == 1 {
			require.NoError(t, lease.steer("use the v2 API", false))
		}
		return fantasy.NewTextResponse("ok")
	})
	model := &scriptedModel{toolName: "work", toolSteps: 3}
	a := jobEventsAgent(env, env.messages, model, nil, tool)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "start", NonInteractive: true, steering: lease})
	require.NoError(t, err)

	require.Len(t, model.prompts, 4)
	for step := 1; step < len(model.prompts); step++ {
		prev, next := model.prompts[step-1], model.prompts[step]
		require.Greater(t, len(next), len(prev))
		for i := range prev {
			require.Equal(t, prev[i].Role, next[i].Role, "step %d message %d", step, i)
			require.Equal(t, textOf(prev[i]), textOf(next[i]), "step %d message %d", step, i)
		}
	}
}

func TestRunWithInjectionsPersistsThemAsTheTurnsMessage(t *testing.T) {
	t.Parallel()
	env, sessionID, _ := steeredAgentEnv(t)
	model := &scriptedModel{}
	a := jobEventsAgent(env, env.messages, model, nil)

	injections := []message.InjectionContent{{
		Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, Text: "now add tests", CallID: "msg$$call-2",
	}}
	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Injections: injections, NonInteractive: true})
	require.NoError(t, err)

	require.Len(t, model.prompts, 1)
	last := model.prompts[0][len(model.prompts[0])-1]
	require.Equal(t, message.RenderInjections(injections), textOf(last))

	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	require.Equal(t, message.MessageTypeSubagentInjection, msgs[0].MessageType)
	require.Equal(t, injections, msgs[0].Injections())
}

func TestInterruptGivesInFlightToolsAnInterruptedResult(t *testing.T) {
	t.Parallel()
	env, sessionID, lease := steeredAgentEnv(t)

	tool := fantasy.NewAgentTool("work", "Test tool.",
		func(ctx context.Context, _ struct{}, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
			require.NoError(t, lease.steer("stop", true))
			<-ctx.Done()
			return fantasy.ToolResponse{}, ctx.Err()
		})
	model := &scriptedModel{toolName: "work", toolSteps: 1}
	a := jobEventsAgent(env, env.messages, model, nil, tool)

	attemptCtx := lease.beginAttempt(t.Context())
	_, err := a.Run(attemptCtx, SessionAgentCall{SessionID: sessionID, Prompt: "start", NonInteractive: true, steering: lease})
	require.ErrorIs(t, err, context.Canceled)

	msgs, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var results []string
	for _, msg := range msgs {
		for _, tr := range msg.ToolResults() {
			results = append(results, tr.Content)
		}
	}
	require.Equal(t, []string{"Interrupted by user"}, results)
}
