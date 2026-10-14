package app

import (
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

type yoloPermissions struct {
	permission.Service
	level config.YoloLevel
}

func (p yoloPermissions) YoloLevel() config.YoloLevel { return p.level }

func TestSkipWorkflowApprovalOnlyAtFullYolo(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		level config.YoloLevel
		want  bool
	}{
		"off":      {level: config.YoloOff},
		"standard": {level: config.YoloStandard},
		"full":     {level: config.YoloFull, want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := &App{Permissions: yoloPermissions{level: tc.level}}
			require.Equal(t, tc.want, app.skipWorkflowApproval())
		})
	}
}

func TestStartWorkflowsShutsDownTheServiceItReplaces(t *testing.T) {
	t.Parallel()

	events := pubsub.NewBroker[tea.Msg]()
	t.Cleanup(events.Shutdown)
	app := &App{
		globalCtx:       t.Context(),
		eventsCtx:       t.Context(),
		events:          events,
		serviceEventsWG: &sync.WaitGroup{},
		config:          config.NewTestStore(&config.Config{Options: &config.Options{}}),
	}

	app.startWorkflows()
	first := app.Workflows
	firstEvents := first.Subscribe(t.Context())
	app.startWorkflows()
	app.retiredWorkflows.Wait()

	require.NotSame(t, first, app.Workflows)
	select {
	case _, ok := <-firstEvents:
		require.False(t, ok, "the replaced service is shut down")
	case <-time.After(5 * time.Second):
		t.Fatal("the replaced service is still running")
	}
	current := app.Workflows.Subscribe(t.Context())
	select {
	case <-current:
		t.Fatal("the new service is shut down too")
	default:
	}
}
