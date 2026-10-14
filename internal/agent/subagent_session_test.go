package agent

import (
	"context"
	"errors"
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
		"run failure still hands jobs back": {
			runErr: errors.New("provider down"), wantContent: "Failed to generate response: provider down",
			wantIsError: true, wantHandedOn: true,
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
				child, err := env.sessions.Get(t.Context(), call.SessionID)
				require.NoError(t, err)
				child.Cost = 1.5
				_, err = env.sessions.Save(t.Context(), child)
				require.NoError(t, err)
				if tc.runErr != nil {
					return nil, tc.runErr
				}
				return agentResultWithText("done"), nil
			})

			resp, err := coord.runSubAgent(t.Context(), subAgentParams{
				Agent: agent, SessionID: parent.ID, AgentMessageID: "msg-1", ToolCallID: "call-" + name,
				Prompt: "go", SessionTitle: "Child",
				SessionSetup: func(id string) { setupID = id },
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
