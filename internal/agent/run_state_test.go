package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestAdmissionDirectSessionAgentSharesOwner(t *testing.T) {
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	_, err = a.admission.submit(t.Context(), s.ID, submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		_, err := a.Run(t.Context(), SessionAgentCall{SessionID: s.ID, Prompt: "ordinary"})
		require.NoError(t, err)
		require.ErrorIs(t, a.Summarize(t.Context(), s.ID, nil), ErrSessionBusy)
		require.Equal(t, []string{"ordinary"}, a.QueuedPromptsList(s.ID))
		a.ClearQueue(s.ID)
		require.True(t, a.IsSessionBusy(s.ID))
		a.Cancel(s.ID)
		return nil, ctx.Err()
	}})
	require.ErrorIs(t, err, context.Canceled)
	messages, err := env.messages.List(t.Context(), s.ID)
	require.NoError(t, err)
	require.Empty(t, messages)
}

func TestAdmissionDelegationDoesNotReuseParentOwner(t *testing.T) {
	env := testEnv(t)
	coord := newTestCoordinator(t, env, "local", config.ProviderConfig{ID: "local"})
	coord.admission = newAdmission(t.Context())
	parent, err := env.sessions.Create(t.Context(), "parent", env.workingDir)
	require.NoError(t, err)
	child := newMockAgent("local", 100, func(ctx context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		return coord.admission.submit(ctx, call.SessionID, submission{run: func(context.Context) (*fantasy.AgentResult, error) {
			return agentResultWithText("child completed"), nil
		}})
	})
	_, err = coord.admission.submit(t.Context(), parent.ID, submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		response, err := coord.runSubAgent(ctx, subAgentParams{Agent: child, SessionID: parent.ID, AgentMessageID: "parent-message", ToolCallID: "task", Prompt: "child"})
		require.NoError(t, err)
		require.False(t, response.IsError, response.Content)
		require.True(t, coord.admission.busy(parent.ID))
		return nil, nil
	}})
	require.NoError(t, err)
}

func TestRetryRejectsReasoningMetadata(t *testing.T) {
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	user, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	assistant, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: user.ID, Parts: []message.ContentPart{message.ReasoningContent{Signature: "signed reasoning"}, message.Finish{Reason: message.FinishReasonError}}})
	require.NoError(t, err)
	err = a.restoreAttempt(t.Context(), s.ID, &runState{acceptedUserID: user.ID, assistantIDs: []string{assistant.ID}})
	require.ErrorContains(t, err, "retry would replay meaningful output")
	current, err := env.sessions.Get(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, assistant.ID, current.LeafMessageID)
}
