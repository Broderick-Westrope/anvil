package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

// These tests are internal: the agent cache, lazyServersFor and the
// subagent a workflow runs have no exported view.

func TestWorkflowAgentPinningOnlyAModelReusesTheCachedAgent(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	model := reloadTestProvider + "/" + reloadTestModel

	// A cancelled context stops the call once its agent is chosen, before
	// any model request.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := c.RunWorkflowAgent(ctx, WorkflowAgentCall{ParentSessionID: "parent", Agent: "helper", Prompt: "x", Model: model})
	require.Error(t, err)

	_, ok := c.agents.Get("helper|2|" + model)
	require.True(t, ok, "a model-only call built an uncached agent")
}

func TestLazyServersForToolNames(t *testing.T) {
	t.Parallel()

	lazy := map[string]string{"datadog": "obs", "linear": "issues", "my_srv": "x"}
	tests := map[string]struct {
		names []string
		want  []string
	}{
		"plain name":           {names: []string{"mcp_datadog_query_logs"}, want: []string{"datadog"}},
		"oauth-renamed prefix": {names: []string{"mcp_Linear_list_issues"}, want: []string{"linear"}},
		"underscore in server": {names: []string{"mcp_my_srv_do"}, want: []string{"my_srv"}},
		"built-in name":        {names: []string{"view"}, want: nil},
		"unknown server":       {names: []string{"mcp_ghost_x"}, want: nil},
		"deduplicated":         {names: []string{"mcp_datadog_a", "mcp_datadog_b"}, want: []string{"datadog"}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, lazyServersFor(tc.names, lazy))
		})
	}
}

// A workflow gets only the subagent's text and output, so jobs handed to
// the parent session are logged instead. It replaces the default logger,
// so it doesn't run in parallel.
func TestWorkflowAgentLogsJobsHandedToTheParent(t *testing.T) {
	logs := captureLogs(t)
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	parent, err := env.sessions.Create(t.Context(), "Parent", t.TempDir())
	require.NoError(t, err)
	c.agents.Set("helper|2", newMockAgent(reloadTestProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		startTestJob(t, call.SessionID, "sleep 30", shell.OriginExplicit)
		return agentResultWithText("done"), nil
	}))

	out, err := c.RunWorkflowAgent(t.Context(), WorkflowAgentCall{ParentSessionID: parent.ID, Agent: "helper", Prompt: "x"})
	require.NoError(t, err)
	require.Equal(t, "done", out.Text)
	require.Len(t, shell.GetBackgroundShellManager().ListBySession(parent.ID), 1)
	require.Contains(t, logs.String(), "Workflow subagent jobs handed off")
}
