package agent

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// These pin what every subagent run does around its turns: set up the
// child session, hand surviving jobs to the parent, and roll its cost up.
func TestRunSubAgentChildSessionLifecycle(t *testing.T) {
	const providerID = "test-provider"
	providerCfg := config.ProviderConfig{ID: providerID}

	tests := map[string]struct {
		runErr       error
		wantContent  string
		wantCostUp   bool
		wantIsError  bool
		wantHandedOn bool
	}{
		"success hands jobs back with the output": {
			wantContent: "done", wantCostUp: true, wantHandedOn: true,
		},
		"run failure still hands jobs back and rolls its cost up": {
			runErr: errors.New("provider down"), wantContent: "Failed to generate response: provider down",
			wantCostUp: true, wantIsError: true, wantHandedOn: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			coord := newTestCoordinator(t, env, providerID, providerCfg)
			parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
			require.NoError(t, err)

			var setupID string
			agent := newMockAgent(providerID, 4096, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
				require.Equal(t, setupID, call.SessionID, "setup runs before the turn, on the child session")
				startTestJob(t, call.SessionID, "sleep 30", shell.OriginExplicit)
				require.NoError(t, env.sessions.AddCost(t.Context(), call.SessionID, 1.5))
				if tc.runErr != nil {
					return nil, tc.runErr
				}
				return agentResultWithText("done"), nil
			})

			resp, err := coord.runSubAgent(t.Context(), subAgentParams{
				Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1", ToolCallID: "call-" + name,
				Prompt: "go", SessionTitle: "Child",
				OnSession: func(id string) { setupID = id },
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantIsError, resp.IsError)
			require.Contains(t, resp.Content, tc.wantContent)
			if tc.wantHandedOn {
				require.Contains(t, resp.Content, "<background_jobs>")
				require.NotEmpty(t, shell.GetBackgroundShellManager().ListBySession(parent.ID))
			}
			got, err := env.sessions.Get(t.Context(), parent.ID)
			require.NoError(t, err)
			if tc.wantCostUp {
				require.InDelta(t, 1.5, got.Cost, 1e-9)
			} else {
				require.Zero(t, got.Cost)
			}
			for _, job := range shell.GetBackgroundShellManager().ListBySession(parent.ID) {
				_ = shell.GetBackgroundShellManager().Kill(job.ID)
			}
		})
	}
}

// captureLogs sends the default logger to a buffer until the test ends.
// Tests using it must not run in parallel.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return logs
}

// A subagent run logs what it can't put in its response: jobs handed off
// when it fails outright, and a failed cost roll-up.
func TestRunSubAgentLogsWhatItCannotReturn(t *testing.T) {
	const providerID = "test-provider"

	t.Run("jobs handed off after a failure", func(t *testing.T) {
		logs := captureLogs(t)
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)
		agent := newMockAgent("unconfigured-provider", 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			t.Fatal("the agent ran without a configured provider")
			return nil, nil
		})

		_, err = coord.runSubAgent(t.Context(), subAgentParams{
			Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1", ToolCallID: "call-failed",
			Prompt: "go", SessionTitle: "Child",
			OnSession: func(id string) { startTestJob(t, id, "sleep 30", shell.OriginExplicit) },
		})
		require.ErrorIs(t, err, errModelProviderNotConfigured)
		jobs := shell.GetBackgroundShellManager().ListBySession(parent.ID)
		require.Len(t, jobs, 1)
		require.Contains(t, logs.String(), "Subagent jobs handed off after error")
	})

	t.Run("failed cost roll-up keeps the output", func(t *testing.T) {
		logs := captureLogs(t)
		env := testEnv(t)
		coord := newTestCoordinator(t, env, providerID, config.ProviderConfig{ID: providerID})
		parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
		require.NoError(t, err)
		agent := newMockAgent(providerID, 4096, func(context.Context, SessionAgentCall) (*fantasy.AgentResult, error) {
			require.NoError(t, env.sessions.Delete(t.Context(), parent.ID))
			return agentResultWithText("done"), nil
		})

		resp, err := coord.runSubAgent(t.Context(), subAgentParams{
			Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1", ToolCallID: "call-orphaned",
			Prompt: "go", SessionTitle: "Child",
		})
		require.NoError(t, err)
		require.False(t, resp.IsError)
		require.Equal(t, "done", resp.Content)
		require.Contains(t, logs.String(), "Failed to roll up subagent cost")
	})
}
