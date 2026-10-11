package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/lsp"
	"github.com/stretchr/testify/require"
)

// An app that starts unconfigured must still announce configured LSPs,
// because finishing onboarding only initialises agents.
func TestNew_UnconfiguredTracksConfiguredLSPs(t *testing.T) {
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	t.Setenv("ANVIL_CACHE_DIR", t.TempDir())
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	const server = "anvil-test-unconfigured-lsp"
	workingDir := t.TempDir()
	cfgJSON := `{"lsp": {"` + server + `": {"command": "anvil-test-missing-lsp-binary"}}}`
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "anvil.json"), []byte(cfgJSON), 0o644))

	store, err := config.Load(workingDir, t.TempDir(), false)
	require.NoError(t, err)
	// Provider API keys in the developer's environment would make the
	// config look configured, so drop every provider.
	store.Config().Providers.Reset(map[string]config.ProviderConfig{})
	require.False(t, store.Config().IsConfigured())

	conn, err := db.ConnectGlobal(t.Context())
	require.NoError(t, err)

	a, err := app.New(t.Context(), conn, store)
	require.NoError(t, err)
	t.Cleanup(a.Shutdown)

	require.Eventually(t, func() bool {
		info, ok := app.GetLSPState(server)
		return ok && info.State == lsp.StateUnstarted
	}, 5*time.Second, 10*time.Millisecond)
}
