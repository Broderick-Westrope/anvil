package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The agent cache has no exported view, so this test is internal.
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
