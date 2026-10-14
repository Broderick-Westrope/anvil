package workflowhost

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

type sliceRecorder struct {
	events []workflow.Event
	err    error
}

func (r *sliceRecorder) Record(_ context.Context, ev workflow.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	return nil
}

// A change is announced only once its event is in the log, so a view
// reloaded on the announcement always includes it.
func TestChangeNotifierAnnouncesOnlyRecordedEvents(t *testing.T) {
	t.Parallel()

	inner := &sliceRecorder{}
	var seen []int
	n := changeNotifier{Recorder: inner, changed: func() { seen = append(seen, len(inner.events)) }}
	require.NoError(t, n.Record(t.Context(), workflow.Event{Seq: 1}))
	require.NoError(t, n.Record(t.Context(), workflow.Event{Seq: 2}))
	require.Equal(t, []int{1, 2}, seen)

	inner.err = errors.New("disk full")
	require.ErrorIs(t, n.Record(t.Context(), workflow.Event{Seq: 3}), inner.err)
	require.Equal(t, []int{1, 2}, seen, "nothing changed, so nothing is announced")
}
