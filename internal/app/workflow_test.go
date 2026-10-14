package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
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

// Stopping workflows warns when a run outlasts the wait, since its
// outcome may never be recorded. It replaces the default logger, so it
// doesn't run in parallel.
func TestStopWorkflowsWarnsWhenARunOutlastsTheWait(t *testing.T) {
	const warning = "Workflows did not stop in time"
	for name, stuck := range map[string]bool{"stuck run": true, "idle": false} {
		t.Run(name, func(t *testing.T) {
			logs := &lockedBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })

			dataDir := t.TempDir()
			conn, err := db.Connect(t.Context(), dataDir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Release(dataDir) })
			entered, release := make(chan struct{}), make(chan struct{})
			svc := workflowhost.NewService(t.Context(), workflowhost.Deps{
				Store: workflowhost.NewStore(db.New(conn), t.TempDir(), "inst-test"),
				// Approval is the one wait shutdown can't end.
				SkipApproval: func() bool {
					close(entered)
					<-release
					return true
				},
			})
			if stuck {
				path := filepath.Join(t.TempDir(), "workflow.js")
				require.NoError(t, os.WriteFile(path, []byte("function main() {}"), 0o644))
				require.NoError(t, svc.Start(t.Context(), workflowhost.Invocation{CommandID: "demo", ScriptPath: path, SessionID: "sess-1"}))
				<-entered
			}

			stopWorkflows(svc, workflowReloadReason, 10*time.Millisecond)
			close(release)
			require.NoError(t, svc.Shutdown(context.Background(), workflowReloadReason), "the run ends once released")

			require.Equal(t, stuck, strings.Contains(logs.String(), warning), logs.String())
			if stuck {
				require.Contains(t, logs.String(), workflowReloadReason)
			}
		})
	}
}
