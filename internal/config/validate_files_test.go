package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateFiles(t *testing.T) {
	t.Run("valid config passes", func(t *testing.T) {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
		writeConfig(t, e.projectDir, map[string]any{
			"permissions": map[string]any{"bash": "ask"},
			"hooks": map[string]any{
				"PreToolUse": []map[string]any{{"matcher": "^bash$", "command": "true"}},
			},
		})
		require.NoError(t, ValidateFiles(e.projectDir, filepath.Join(e.projectDir, ".anvil"), nil))
	})

	t.Run("invalid project json fails", func(t *testing.T) {
		e := newBouncerEnv(t)
		require.NoError(t, os.WriteFile(filepath.Join(e.projectDir, "anvil.json"), []byte(`{"broken":`), 0o600))
		err := ValidateFiles(e.projectDir, "", nil)
		require.ErrorContains(t, err, "invalid JSON")
	})

	t.Run("invalid workspace json fails", func(t *testing.T) {
		e := newBouncerEnv(t)
		dataDir := filepath.Join(e.projectDir, ".anvil")
		require.NoError(t, os.MkdirAll(dataDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, "anvil.json"), []byte(`{nope`), 0o600))
		err := ValidateFiles(e.projectDir, dataDir, nil)
		require.ErrorContains(t, err, "invalid JSON")
	})

	t.Run("workspace hooks are validated", func(t *testing.T) {
		e := newBouncerEnv(t)
		dataDir := filepath.Join(e.projectDir, ".anvil")
		writeConfig(t, dataDir, map[string]any{
			"hooks": map[string]any{
				"PreToolUse": []map[string]any{{"matcher": "(", "command": "true"}},
			},
		})
		err := ValidateFiles(e.projectDir, dataDir, nil)
		require.ErrorContains(t, err, "invalid matcher regex")
	})

	t.Run("invalid permission action fails", func(t *testing.T) {
		e := newBouncerEnv(t)
		writeConfig(t, e.projectDir, map[string]any{"permissions": map[string]any{"bash": "maybe"}})
		err := ValidateFiles(e.projectDir, "", nil)
		require.ErrorContains(t, err, "unknown permission action")
	})

	t.Run("invalid mcp auth fails", func(t *testing.T) {
		e := newBouncerEnv(t)
		writeConfig(t, e.projectDir, map[string]any{
			"mcp": map[string]any{"s": map[string]any{"type": "stdio", "command": "x", "auth": "oauth"}},
		})
		err := ValidateFiles(e.projectDir, "", nil)
		require.ErrorContains(t, err, "not supported for stdio")
	})

	t.Run("unknown bouncer axis fails", func(t *testing.T) {
		e := newBouncerEnv(t)
		block := bouncerBlock("shadow")
		block["escalate_at_axes"] = map[string]any{"not-an-axis": 0.5}
		e.writeGlobal(t, map[string]any{"bouncer": block})
		err := ValidateFiles(e.projectDir, "", nil)
		require.ErrorContains(t, err, "unknown axis")
	})

	t.Run("bouncer validator runs", func(t *testing.T) {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
		var got *Bouncer
		err := ValidateFiles(e.projectDir, "", func(b *Bouncer) error {
			got = b
			return errors.New("thresholds rejected")
		})
		require.ErrorContains(t, err, "thresholds rejected")
		require.NotNil(t, got)
		require.Equal(t, BouncerShadow, got.Mode)
	})

	t.Run("project bouncer block is not trusted", func(t *testing.T) {
		e := newBouncerEnv(t)
		writeConfig(t, e.projectDir, map[string]any{"bouncer": bouncerBlock("enforce")})
		called := false
		err := ValidateFiles(e.projectDir, "", func(*Bouncer) error {
			called = true
			return nil
		})
		require.NoError(t, err)
		require.False(t, called)
	})

	t.Run("shell substitution is not executed", func(t *testing.T) {
		e := newBouncerEnv(t)
		pwned := filepath.Join(t.TempDir(), "pwned")
		writeConfig(t, e.projectDir, map[string]any{
			"env": map[string]any{"X": "$(touch " + pwned + ")"},
			"mcp": map[string]any{"s": map[string]any{
				"type":    "stdio",
				"command": "x",
				"env":     map[string]any{"Y": "$(touch " + pwned + ")"},
			}},
			"providers": map[string]any{"p": map[string]any{
				"type":     "openai-compat",
				"base_url": "https://llm.example.com/v1",
				"api_key":  "$(touch " + pwned + ")",
			}},
		})
		require.NoError(t, ValidateFiles(e.projectDir, "", nil))
		_, err := os.Stat(pwned)
		require.True(t, os.IsNotExist(err), "ValidateFiles executed a $(...) substitution")
		_, set := os.LookupEnv("X")
		require.False(t, set, "ValidateFiles applied config env")
	})

	t.Run("writes nothing", func(t *testing.T) {
		e := newBouncerEnv(t)
		dataDir := filepath.Join(e.projectDir, ".anvil")
		writeConfig(t, e.projectDir, map[string]any{"options": map[string]any{"debug": true}})
		require.NoError(t, ValidateFiles(e.projectDir, dataDir, nil))
		_, err := os.Stat(dataDir)
		require.True(t, os.IsNotExist(err))
		entries, err := os.ReadDir(e.dataDir)
		require.NoError(t, err)
		require.Empty(t, entries)
	})
}
