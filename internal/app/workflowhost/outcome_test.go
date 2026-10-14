package workflowhost

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

// A run that fails on its own just as shutdown begins can't be staged
// through the Service, so the classification is tested directly.
func TestRunOutcome(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err        error
		stopping   bool
		wantStatus Status
		wantMsg    string
	}{
		"completed":                {wantStatus: StatusCompleted},
		"aborted":                  {err: workflow.ErrAborted, wantStatus: StatusAborted, wantMsg: "aborted"},
		"failed":                   {err: errors.New("boom"), wantStatus: StatusFailed, wantMsg: "boom"},
		"aborted while stopping":   {err: workflow.ErrAborted, stopping: true, wantStatus: StatusInterrupted, wantMsg: "interrupted: reloaded"},
		"cancelled while stopping": {err: context.Canceled, stopping: true, wantStatus: StatusInterrupted, wantMsg: "interrupted: reloaded"},
		"failed while stopping":    {err: errors.New("boom"), stopping: true, wantStatus: StatusFailed, wantMsg: "boom"},
		"completed while stopping": {stopping: true, wantStatus: StatusCompleted},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			status, msg := runOutcome(tc.err, tc.stopping, "reloaded")
			require.Equal(t, tc.wantStatus, status)
			require.Equal(t, tc.wantMsg, msg)
		})
	}
}
