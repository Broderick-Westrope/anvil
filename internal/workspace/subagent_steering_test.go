package workspace

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/stretchr/testify/require"
)

// steeringCoordinator answers the subagent steering calls with fixed values.
type steeringCoordinator struct {
	agent.Coordinator
	steered []string
}

func (c *steeringCoordinator) SteerSubagent(child, text string, interrupt bool) error {
	c.steered = append(c.steered, child+":"+text)
	return nil
}
func (c *steeringCoordinator) CancelSubagent(string) bool  { return true }
func (c *steeringCoordinator) SubagentRunning(string) bool { return true }
func (c *steeringCoordinator) SubagentPendingSteers(string) []string {
	return []string{"queued"}
}

func (c *steeringCoordinator) TakeReturnedSteers(root string) []agent.SubagentSteersReturned {
	return []agent.SubagentSteersReturned{{RootSessionID: root}}
}

func TestSubagentSteeringReachesTheCoordinator(t *testing.T) {
	t.Parallel()
	coord := &steeringCoordinator{}
	w := NewAppWorkspace(&app.App{AgentCoordinator: coord}, nil)

	require.NoError(t, w.AgentSteerSubagent("child", "go", false))
	require.Equal(t, []string{"child:go"}, coord.steered)
	require.True(t, w.AgentCancelSubagent("child"))
	require.True(t, w.AgentSubagentRunning("child"))
	require.Equal(t, []string{"queued"}, w.AgentSubagentPendingSteers("child"))
	require.Equal(t, []agent.SubagentSteersReturned{{RootSessionID: "root"}}, w.AgentTakeReturnedSteers("root"))
}

func TestSubagentSteeringWithoutACoordinator(t *testing.T) {
	t.Parallel()
	w := NewAppWorkspace(&app.App{}, nil)

	require.EqualError(t, w.AgentSteerSubagent("child", "go", false), "agent coordinator not initialized")
	require.False(t, w.AgentCancelSubagent("child"))
	require.False(t, w.AgentSubagentRunning("child"))
	require.Nil(t, w.AgentSubagentPendingSteers("child"))
	require.Nil(t, w.AgentTakeReturnedSteers("root"))
}
