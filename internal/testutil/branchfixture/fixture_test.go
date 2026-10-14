package branchfixture_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
)

func TestWithAgentsDefinesAgentsThatRun(t *testing.T) {
	f := branchfixture.New(t, branchfixture.WithAgents("reviewer", "planner"))
	parent, err := f.Workspace.CreateSession(f.Context, "parent")
	require.NoError(t, err)
	for _, name := range []string{"reviewer", "planner"} {
		f.Provider.Enqueue(branchfixture.Response{Text: "done by " + name})
		out, err := f.Coordinator.RunWorkflowAgent(f.Context, agent.WorkflowAgentCall{
			ParentSessionID: parent.ID, Agent: name, Prompt: "go", Tools: []string{"view", "grep", "bash"},
		})
		require.NoError(t, err)
		require.Equal(t, "done by "+name, out.Text)
	}
	require.Contains(t, f.Provider.Requests()[0].Body, "You are reviewer.")
}

func TestNewWithoutAgentsLoadsNoPlugins(t *testing.T) {
	f := branchfixture.New(t)
	require.Empty(t, f.Config.Config().Plugins)
}
