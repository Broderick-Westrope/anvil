package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestBranchPrefixValidation(t *testing.T) {
	t.Parallel()

	call := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "call", Name: "local", Finished: true}}}
	result := message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call", Name: "local"}}}
	user := message.Message{Role: message.User}
	for _, test := range []struct {
		name  string
		path  []message.Message
		valid bool
	}{
		{"empty", nil, true}, {"complete", []message.Message{call, result, user}, true},
		{"orphan", []message.Message{result}, false}, {"open", []message.Message{call}, false},
		{"reversed", []message.Message{result, call}, false}, {"duplicate call", []message.Message{call, result, call, result}, false},
		{"duplicate result", []message.Message{call, result, result}, false}, {"interrupted", []message.Message{call, user, result}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateBranchPrefix(test.path)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrBranchUnsafePrefix)
			}
		})
	}
}

func TestBranchUnfinishedAssistantRejected(t *testing.T) {
	env := testEnv(t)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	target, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	s, err = env.sessions.Get(t.Context(), s.ID)
	require.NoError(t, err)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	_, err = a.branchSession(t.Context(), s, BranchOrigin{TargetMessageID: target.ID, ExpectedSourceLeafID: target.ID})
	require.ErrorIs(t, err, ErrBranchInvalidTarget)
}

func TestAdmissionDirectSessionAgentSharesOwner(t *testing.T) {
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	_, err = a.admission.submit(t.Context(), s.ID, submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		_, err := a.Run(t.Context(), SessionAgentCall{SessionID: s.ID, Prompt: "ordinary"})
		require.NoError(t, err)
		_, err = a.Run(t.Context(), SessionAgentCall{SessionID: s.ID, Prompt: "branch", BranchOrigin: &BranchOrigin{}})
		require.ErrorIs(t, err, ErrSessionBusy)
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

func TestBranchSelectedMetadataDerivation(t *testing.T) {
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	root, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "old request"}}})
	require.NoError(t, err)
	selected, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.System, ParentMessageID: root.ID, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{message.MCPToggleContent{ServerName: "selected", Enabled: true}}})
	require.NoError(t, err)
	compact, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: selected.ID, MessageType: message.MessageTypeCompaction, Parts: []message.ContentPart{message.CompactionContent{Summary: "selected summary"}}})
	require.NoError(t, err)
	target, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: compact.ID})
	require.NoError(t, err)
	excluded, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.System, ParentMessageID: target.ID, MessageType: message.MessageTypeMCPToggle, Parts: []message.ContentPart{message.MCPToggleContent{ServerName: "excluded", Enabled: true}}})
	require.NoError(t, err)
	s, err = env.sessions.Get(t.Context(), s.ID)
	require.NoError(t, err)
	effective, err := a.branchSession(t.Context(), s, BranchOrigin{TargetMessageID: target.ID, ExpectedSourceLeafID: excluded.ID})
	require.NoError(t, err)
	filtered, raw, err := a.getSessionMessages(t.Context(), effective)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"selected": true}, deriveLazyMCPState(raw))
	require.Len(t, filtered, 1)
	require.Contains(t, filtered[0].Content().Text, "selected summary")
	persisted, err := env.sessions.Get(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, excluded.ID, persisted.LeafMessageID)
}

func TestBranchRetryRejectsReasoningMetadata(t *testing.T) {
	env := testEnv(t)
	a := NewSessionAgent(SessionAgentOptions{Sessions: env.sessions, Messages: env.messages}).(*sessionAgent)
	s, err := env.sessions.Create(t.Context(), "source", env.workingDir)
	require.NoError(t, err)
	user, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	assistant, err := env.messages.Create(t.Context(), s.ID, message.CreateMessageParams{Role: message.Assistant, ParentMessageID: user.ID, Parts: []message.ContentPart{message.ReasoningContent{Signature: "signed reasoning"}, message.Finish{Reason: message.FinishReasonError}}})
	require.NoError(t, err)
	err = a.restoreAttempt(t.Context(), s.ID, &runState{acceptedUserID: user.ID, attemptParent: user.ID, assistantIDs: []string{assistant.ID}})
	require.ErrorIs(t, err, ErrBranchUnsafePrefix)
	current, err := env.sessions.Get(t.Context(), s.ID)
	require.NoError(t, err)
	require.Equal(t, assistant.ID, current.LeafMessageID)
}
