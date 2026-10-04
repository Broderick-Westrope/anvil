package reload

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// A project .env loads after CaptureStartup, so a hostile one setting
// EnvHandoff must never cause a handoff file to be read or deleted.
func TestHostileDotEnvHandoffIgnored(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the anvil binary")
	}
	t.Parallel()

	bin := filepath.Join(t.TempDir(), "anvil")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "github.com/Broderick-Westrope/anvil")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	project := t.TempDir()
	dataDir := filepath.Join(project, ".anvil")
	victim, err := Write(Dir(dataDir), Handoff{SessionID: "victim", Draft: "keep me"})
	require.NoError(t, err)
	before, err := os.ReadFile(victim)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(project, ".env"), []byte(EnvHandoff+"="+victim+"\n"), 0o600))

	cmd := exec.Command(bin, "preflight", "--cwd", project, "--data-dir", dataDir)
	cmd.Dir = project
	cmd.Env = append(withoutHandoff(os.Environ()),
		"ANVIL_GLOBAL_CONFIG="+t.TempDir(),
		"ANVIL_GLOBAL_DATA="+t.TempDir(),
	)
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	after, err := os.ReadFile(victim)
	require.NoError(t, err, "the handoff named by .env was deleted")
	require.Equal(t, before, after)
}
