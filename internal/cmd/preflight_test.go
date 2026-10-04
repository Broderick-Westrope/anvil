package cmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/version"
	"github.com/stretchr/testify/require"
)

type preflightEnv struct {
	globalDir  string
	globalData string
	projectDir string
	dataDir    string
}

// newPreflightEnv isolates the user-level config locations. Not parallel:
// it uses t.Setenv.
func newPreflightEnv(t *testing.T) preflightEnv {
	t.Helper()
	e := preflightEnv{
		globalDir:  t.TempDir(),
		globalData: t.TempDir(),
		projectDir: t.TempDir(),
	}
	e.dataDir = filepath.Join(e.projectDir, ".anvil")
	t.Setenv("ANVIL_GLOBAL_CONFIG", e.globalDir)
	t.Setenv("ANVIL_GLOBAL_DATA", e.globalData)
	return e
}

func writeJSON(t *testing.T, dir string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "anvil.json"), data, 0o600))
}

func (e preflightEnv) run(t *testing.T) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runPreflight(&out, e.projectDir, e.dataDir)
	return out.String(), err
}

func preflightBouncer(extra map[string]any) map[string]any {
	b := map[string]any{
		"mode":  "shadow",
		"url":   "https://classifier.example.com/v1/answer",
		"model": "von-1.0.0",
	}
	for k, v := range extra {
		b[k] = v
	}
	return map[string]any{"bouncer": b}
}

func TestPreflight(t *testing.T) {
	t.Run("valid config passes and prints the version", func(t *testing.T) {
		e := newPreflightEnv(t)
		writeJSON(t, e.globalDir, preflightBouncer(nil))
		writeJSON(t, e.projectDir, map[string]any{"permissions": map[string]any{"bash": "ask"}})
		out, err := e.run(t)
		require.NoError(t, err)
		require.Equal(t, version.Version, strings.SplitN(out, "\n", 2)[0])
	})

	t.Run("invalid json fails", func(t *testing.T) {
		e := newPreflightEnv(t)
		require.NoError(t, os.WriteFile(filepath.Join(e.projectDir, "anvil.json"), []byte(`{"x":`), 0o600))
		out, err := e.run(t)
		require.ErrorContains(t, err, "invalid JSON")
		require.True(t, strings.HasPrefix(out, version.Version+"\n"))
	})

	t.Run("unknown axis fails", func(t *testing.T) {
		e := newPreflightEnv(t)
		writeJSON(t, e.globalDir, preflightBouncer(map[string]any{
			"escalate_at_axes": map[string]any{"vibes": 0.5},
		}))
		_, err := e.run(t)
		require.ErrorContains(t, err, "unknown axis")
	})

	t.Run("deny_at below default escalation fails", func(t *testing.T) {
		e := newPreflightEnv(t)
		writeJSON(t, e.globalDir, preflightBouncer(map[string]any{"deny_at": 0.4}))
		_, err := e.run(t)
		require.ErrorContains(t, err, "deny_at")
	})

	t.Run("missing cwd fails", func(t *testing.T) {
		e := newPreflightEnv(t)
		var out bytes.Buffer
		require.Error(t, runPreflight(&out, filepath.Join(e.projectDir, "missing"), ""))
	})

	t.Run("does not execute substitutions or touch the database", func(t *testing.T) {
		e := newPreflightEnv(t)
		pwned := filepath.Join(t.TempDir(), "pwned")
		writeJSON(t, e.projectDir, map[string]any{
			"mcp": map[string]any{"s": map[string]any{
				"type":    "stdio",
				"command": "x",
				"env":     map[string]any{"Y": "$(touch " + pwned + ")"},
			}},
			"env": map[string]any{"Z": "$(touch " + pwned + ")"},
		})
		_, err := e.run(t)
		require.NoError(t, err)

		_, err = os.Stat(pwned)
		require.True(t, os.IsNotExist(err), "preflight executed a $(...) substitution")

		for _, dir := range []string{e.globalData, e.projectDir} {
			require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				require.NotContains(t, d.Name(), ".db", "preflight created %s", path)
				return nil
			}))
		}
		_, err = os.Stat(e.dataDir)
		require.True(t, os.IsNotExist(err), "preflight created the data dir")
	})
}

func TestPreflightCommandIsHidden(t *testing.T) {
	t.Parallel()
	c, _, err := rootCmd.Find([]string{"preflight"})
	require.NoError(t, err)
	require.Same(t, preflightCmd, c)
	require.True(t, c.Hidden)
}
