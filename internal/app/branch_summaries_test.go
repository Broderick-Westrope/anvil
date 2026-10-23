package app

import (
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/stretchr/testify/require"
)

type stubCoordinator struct{ agent.Coordinator }

func TestBranchSummaryCoordinatorIsTheCurrentCoordinator(t *testing.T) {
	t.Parallel()

	app := &App{}
	require.Nil(t, app.branchSummaryCoordinator())

	coord := &stubCoordinator{}
	app.AgentCoordinator = coord
	require.Same(t, coord, app.branchSummaryCoordinator())
}

func TestShutdownEndsBranchSummaries(t *testing.T) {
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	t.Setenv("ANVIL_CACHE_DIR", t.TempDir())
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	store, err := config.Load(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	store.Config().Providers.Reset(map[string]config.ProviderConfig{})
	conn, err := db.ConnectGlobal(t.Context())
	require.NoError(t, err)
	app, err := New(t.Context(), conn, store)
	require.NoError(t, err)
	events := app.BranchSummaries.Subscribe(t.Context())

	app.Shutdown()

	select {
	case _, ok := <-events:
		require.False(t, ok, "shutdown closes branch summary events")
	case <-time.After(5 * time.Second):
		t.Fatal("branch summaries were not shut down")
	}
}
