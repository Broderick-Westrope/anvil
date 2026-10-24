package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

// catalogCoordinator answers requirement checks for a fixed set of agents.
type catalogCoordinator struct {
	agent.Coordinator
	agents map[string]bool
}

func (c catalogCoordinator) HasAgent(name string) bool { return c.agents[name] }

func (catalogCoordinator) ActiveSkillByName(string) *skills.Skill { return nil }

func writeWorkflow(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workflow.js")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	return path
}

func TestAnnotateCommandsAddsWhatStopsAWorkflowRunning(t *testing.T) {
	t.Parallel()

	coord := catalogCoordinator{agents: map[string]bool{"reviewer": true}}
	w := &AppWorkspace{app: &app.App{
		AgentCoordinator: coord,
		Workflows:        workflowhost.NewService(t.Context(), workflowhost.Deps{Coordinator: coord}),
	}}
	missing := writeWorkflow(t, `requires({agents: ["reviewer", "ghost"], skills: ["nope"]})
function main() {}`)
	ok := writeWorkflow(t, `requires({agents: ["reviewer"]})
function main() {}`)
	absent := filepath.Join(t.TempDir(), "absent.js")
	cmds := []commands.CustomCommand{
		{Name: "prompt"},
		{Name: "missing", Workflow: missing},
		{Name: "ok", Workflow: ok},
		{Name: "absent", Workflow: absent},
		{Name: "broken", Workflow: missing, Problems: []string{"already broken"}},
		{Name: "missing after absent", Workflow: missing},
	}

	got := w.AnnotateCommands(cmds)

	require.Len(t, got, len(cmds))
	require.Empty(t, got[0].Problems)
	require.Equal(t, []string{`skill "nope" not found`, `agent "ghost" not found`}, got[1].Problems)
	require.Empty(t, got[2].Problems)
	require.Len(t, got[3].Problems, 1, "a script that doesn't load can't run")
	require.Equal(t, got[1].Problems, got[5].Problems, "a script that doesn't load doesn't stop the rest being checked")
	require.Equal(t, []string{"already broken"}, got[4].Problems, "a command that already has problems isn't checked again")
	require.Empty(t, cmds[1].Problems, "the input is not modified")
}

func TestAnnotateCommandsLeavesWorkflowsAloneWithoutTheService(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{}}
	cmds := []commands.CustomCommand{{Name: "a", Workflow: filepath.Join(t.TempDir(), "absent.js")}}
	require.Equal(t, cmds, w.AnnotateCommands(cmds), "workflows aren't broken just because the agent isn't ready")
}

func TestWorkflowControlsNeedTheService(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{}}
	require.ErrorIs(t, w.WorkflowStart(t.Context(), workflowhost.Invocation{SessionID: "s"}), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowRespond("r", "x"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowPause("s"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowResume("s"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowAbort("s"), ErrWorkflowsUnavailable)
}

func TestWorkflowControlsReachTheService(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{Workflows: workflowhost.NewService(t.Context(), workflowhost.Deps{})}}
	require.ErrorIs(t, w.WorkflowPause("s"), workflowhost.ErrNoRun)
}

func TestWorkflowControlsFindNothingWithoutRuns(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{Workflows: workflowhost.NewService(t.Context(), workflowhost.Deps{})}}
	require.ErrorIs(t, w.WorkflowPause("run-1"), workflowhost.ErrNoRun)
}
