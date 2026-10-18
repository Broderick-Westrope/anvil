package chat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/x/ansi"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

func TestWorkflowOutcomeTextDescribesEveryStatus(t *testing.T) {
	t.Parallel()

	tests := map[workflowhost.Status]struct {
		text string
		want string
	}{
		workflowhost.StatusRunning:     {want: "running"},
		workflowhost.StatusCompleted:   {text: `"done"`, want: `completed: "done"`},
		workflowhost.StatusFailed:      {text: "BashError: exit 1", want: "failed: BashError: exit 1"},
		workflowhost.StatusAborted:     {text: "aborted", want: "aborted"},
		workflowhost.StatusInterrupted: {text: "interrupted: agents reloaded", want: "interrupted: agents reloaded"},
		workflowhost.StatusDenied:      {text: "workflow not approved", want: "not approved"},
	}
	for _, status := range workflowhost.Statuses() {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			tc, ok := tests[status]
			require.True(t, ok, "status %q has no outcome text", status)
			require.Equal(t, tc.want, WorkflowOutcomeText(status, tc.text))
		})
	}
	require.Len(t, tests, len(workflowhost.Statuses()))
}

func TestAFailedSessionRowSaysItFailed(t *testing.T) {
	t.Parallel()

	view := workflowhost.RunView{RunID: "run-1", Command: "build", Status: workflowhost.StatusRunning, Phases: []workflowhost.RunPhase{{
		Items: []workflowhost.RunItem{{Kind: workflowhost.RunItemSession, Status: workflowhost.StatusFailed, Err: "fork() of a run's session can't run inside workflow.exclusive\nmore"}},
	}}}
	sty := styles.TokyoNight()
	rows := BuildWorkflowRunItems(&sty, "build", view, func(string) bool { return false })
	var text string
	for _, row := range rows {
		text += ansi.Strip(row.(*WorkflowRunRowItem).RawRender(200)) + "\n"
	}
	require.Contains(t, text, "failed: fork() of a run's session can't run inside workflow.exclusive")
	require.NotContains(t, text, "· session")
}
