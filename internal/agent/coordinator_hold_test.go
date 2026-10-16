package agent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

func TestCoordinatorHasAgent(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer"))

	tests := map[string]struct {
		name string
		want bool
	}{
		"configured agent": {name: "reviewer", want: true},
		"orchestrator":     {name: config.AgentOrchestrator, want: false},
		"unknown agent":    {name: "nobody", want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, f.Coordinator.HasAgent(tc.name))
		})
	}
}

func TestCoordinatorHoldQueuesRunUntilRelease(t *testing.T) {
	f := branchfixture.New(t)
	sessionID := newParent(t, f)
	f.Provider.Enqueue(branchfixture.Response{Text: "done"})

	release, err := f.Coordinator.Hold(f.Context, []string{sessionID})
	require.NoError(t, err)
	result, err := f.Coordinator.Run(f.Context, sessionID, "hello")
	require.NoError(t, err)
	require.Nil(t, result)
	require.Equal(t, []string{"hello"}, f.Coordinator.QueuedPromptsList(sessionID))
	require.Empty(t, f.Provider.Requests())

	release()
	require.Eventually(t, func() bool {
		return len(f.Coordinator.QueuedPromptsList(sessionID)) == 0 && !f.Coordinator.IsSessionBusy(sessionID)
	}, 5*time.Second, 10*time.Millisecond)
	require.NotEmpty(t, f.Provider.Requests())
}
