package agent

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/stretchr/testify/require"
)

func TestRun_RootSessionIDPropagatesToSubagents(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	root, err := env.sessions.Create(t.Context(), "Root", t.TempDir())
	require.NoError(t, err)
	child, err := env.sessions.CreateTaskSession(t.Context(), "child-call", root.ID, "Child")
	require.NoError(t, err)
	grandchild, err := env.sessions.CreateTaskSession(t.Context(), "grandchild-call", child.ID, "Grandchild")
	require.NoError(t, err)

	type seen struct{ session, root string }
	got := map[string]seen{}
	var run func(ctx context.Context, sessionIDs []string) error
	run = func(ctx context.Context, sessionIDs []string) error {
		probe := stepTool("probe", func(ctx context.Context, _ int) fantasy.ToolResponse {
			got[sessionIDs[0]] = seen{tools.GetSessionFromContext(ctx), tools.GetRootSessionFromContext(ctx)}
			if len(sessionIDs) > 1 {
				require.NoError(t, run(ctx, sessionIDs[1:]))
			}
			return fantasy.NewTextResponse("ok")
		})
		agent := jobEventsAgent(env, env.messages, &scriptedModel{toolName: "probe", toolSteps: 1}, nil, probe)
		_, err := agent.Run(ctx, SessionAgentCall{SessionID: sessionIDs[0], Prompt: "go", NonInteractive: true})
		return err
	}
	require.NoError(t, run(t.Context(), []string{root.ID, child.ID, grandchild.ID}))

	require.Equal(t, map[string]seen{
		root.ID:       {root.ID, root.ID},
		child.ID:      {child.ID, root.ID},
		grandchild.ID: {grandchild.ID, root.ID},
	}, got)
}
