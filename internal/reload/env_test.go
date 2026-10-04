package reload

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func resetStartup(t *testing.T) {
	t.Helper()
	reset := func() {
		startupMu.Lock()
		startupEnv, startupHandoff, startupDone = nil, "", false
		startupMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestCaptureStartupEnv(t *testing.T) {
	resetStartup(t)
	t.Setenv(EnvHandoff, "/tmp/handoff.json")
	t.Setenv("ANVIL_RELOAD_TEST_BEFORE", "before")

	CaptureStartup()
	t.Setenv("ANVIL_RELOAD_TEST_AFTER", "after")

	env := StartupEnv()
	require.Contains(t, env, "ANVIL_RELOAD_TEST_BEFORE=before")
	require.NotContains(t, env, "ANVIL_RELOAD_TEST_AFTER=after")
	for _, kv := range env {
		require.NotContains(t, kv, EnvHandoff+"=")
	}
	require.Equal(t, "/tmp/handoff.json", StartupHandoffPath())
	_, set := os.LookupEnv(EnvHandoff)
	require.False(t, set)
}

func TestStartupEnvBeforeCapture(t *testing.T) {
	resetStartup(t)
	t.Setenv(EnvHandoff, "/tmp/handoff.json")
	t.Setenv("ANVIL_RELOAD_TEST_LIVE", "live")

	env := StartupEnv()
	require.Contains(t, env, "ANVIL_RELOAD_TEST_LIVE=live")
	for _, kv := range env {
		require.NotContains(t, kv, EnvHandoff+"=")
	}
	require.Empty(t, StartupHandoffPath())
}

func TestStartupEnvReturnsCopy(t *testing.T) {
	resetStartup(t)
	CaptureStartup()
	env := StartupEnv()
	require.NotEmpty(t, env)
	env[0] = "MUTATED=1"
	require.NotEqual(t, "MUTATED=1", StartupEnv()[0])
}
