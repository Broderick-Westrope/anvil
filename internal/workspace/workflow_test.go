package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
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

func TestWorkflowMissingReportsUnknownRequirements(t *testing.T) {
	t.Parallel()

	coord := catalogCoordinator{agents: map[string]bool{"reviewer": true}}
	a := &app.App{
		AgentCoordinator: coord,
		Workflows:        workflowhost.NewService(t.Context(), workflowhost.Deps{Coordinator: coord}),
	}
	w := &AppWorkspace{app: a}
	path := writeWorkflow(t, `requires({agents: ["reviewer", "ghost"], skills: ["nope"]})
function main() {}`)

	missing, err := w.WorkflowMissing(path)
	require.NoError(t, err)
	require.Equal(t, []string{`skill "nope" not found`, `agent "ghost" not found`}, missing)
}

func TestWorkflowMissingFailsWhenScriptDoesNotLoad(t *testing.T) {
	t.Parallel()

	coord := catalogCoordinator{}
	w := &AppWorkspace{app: &app.App{
		AgentCoordinator: coord,
		Workflows:        workflowhost.NewService(t.Context(), workflowhost.Deps{Coordinator: coord}),
	}}
	_, err := w.WorkflowMissing(filepath.Join(t.TempDir(), "absent.js"))
	require.Error(t, err)
}

func TestWorkflowControlsNeedTheService(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{}}
	require.ErrorIs(t, w.WorkflowStart(t.Context(), workflowhost.Invocation{SessionID: "s"}), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowRespond("r", "x"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowPause("s"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowResume("s"), ErrWorkflowsUnavailable)
	require.ErrorIs(t, w.WorkflowAbort("s"), ErrWorkflowsUnavailable)
	_, err := w.WorkflowMissing("x")
	require.ErrorIs(t, err, ErrWorkflowsUnavailable)
}

func TestWorkflowControlsFindNothingWithoutRuns(t *testing.T) {
	t.Parallel()

	w := &AppWorkspace{app: &app.App{Workflows: workflowhost.NewService(t.Context(), workflowhost.Deps{})}}
	require.ErrorIs(t, w.WorkflowPause("run-1"), workflowhost.ErrNoRun)
}
