package chat

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
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
