package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// bouncerEnv isolates the user-level config locations and the default key
// var for one test, and returns the global config dir and a project dir.
type bouncerEnv struct {
	globalDir  string
	dataDir    string
	projectDir string
}

func newBouncerEnv(t *testing.T) bouncerEnv {
	t.Helper()
	e := bouncerEnv{
		globalDir:  t.TempDir(),
		dataDir:    t.TempDir(),
		projectDir: t.TempDir(),
	}
	t.Setenv("ANVIL_GLOBAL_CONFIG", e.globalDir)
	t.Setenv("ANVIL_GLOBAL_DATA", e.dataDir)
	t.Setenv(DefaultBouncerAPIKeyEnv, "")
	resetProviderState()
	t.Cleanup(resetProviderState)
	return e
}

// writeConfig writes an anvil.json into dir.
func writeConfig(t *testing.T, dir string, cfg map[string]any) {
	t.Helper()
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "anvil.json"), data, 0o600))
}

// writeGlobal writes the user-level config with a single custom provider
// and default providers disabled, so Load never reaches out to Catwalk.
func (e bouncerEnv) writeGlobal(t *testing.T, cfg map[string]any) {
	t.Helper()
	cfg["options"] = map[string]any{"disable_default_providers": true}
	cfg["providers"] = map[string]any{
		"custom": map[string]any{
			"type":     "openai-compat",
			"base_url": "https://llm.example.com/v1",
			"api_key":  "provider-key",
			"models":   []map[string]any{{"id": "m", "name": "M"}},
		},
	}
	writeConfig(t, e.globalDir, cfg)
}

func (e bouncerEnv) load(t *testing.T) (*ConfigStore, error) {
	t.Helper()
	return Load(e.projectDir, filepath.Join(e.projectDir, ".anvil"), false)
}

func (e bouncerEnv) mustLoad(t *testing.T) *ConfigStore {
	t.Helper()
	store, err := e.load(t)
	require.NoError(t, err)
	return store
}

func bouncerBlock(mode string) map[string]any {
	return map[string]any{
		"mode":  mode,
		"url":   "https://classifier.example.com/v1/answer",
		"model": "von-1.0.0",
	}
}

func requireBouncerMode(t *testing.T, store *ConfigStore, want BouncerMode) {
	t.Helper()
	ta := store.TrustedBouncer()
	if want == "" {
		require.Nil(t, ta)
		require.Nil(t, store.Config().Bouncer)
		return
	}
	require.NotNil(t, ta)
	require.Equal(t, want, ta.Config.Mode)
	require.Same(t, ta.Config, store.Config().Bouncer)
}

func TestBouncer_GlobalShadowLoadsAndCapturesKey(t *testing.T) {
	e := newBouncerEnv(t)
	t.Setenv(DefaultBouncerAPIKeyEnv, "real-key")
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})

	store := e.mustLoad(t)
	requireBouncerMode(t, store, "shadow")
	require.Equal(t, "real-key", store.TrustedBouncer().APIKey)
	require.Equal(t, "von-1.0.0", store.TrustedBouncer().Config.Model)
}

func TestBouncer_GlobalDataPathIsTrusted(t *testing.T) {
	e := newBouncerEnv(t)
	writeConfig(t, e.dataDir, map[string]any{"bouncer": bouncerBlock("enforce")})

	requireBouncerMode(t, e.mustLoad(t), "enforce")
}

func TestBouncer_ProjectBlockAloneIgnored(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{})
	writeConfig(t, e.projectDir, map[string]any{"bouncer": bouncerBlock("enforce")})

	store := e.mustLoad(t)
	requireBouncerMode(t, store, "")

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	requireBouncerMode(t, store, "")
}

func TestBouncer_ProjectCannotOverrideGlobal(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
	project := bouncerBlock("enforce")
	project["deny_at"] = 0.5
	writeConfig(t, e.projectDir, map[string]any{"bouncer": project})

	store := e.mustLoad(t)
	requireBouncerMode(t, store, "shadow")
	require.Nil(t, store.TrustedBouncer().Config.DenyAt)
}

func TestBouncer_WorkspaceBlockIgnored(t *testing.T) {
	t.Run("alone", func(t *testing.T) {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{})
		writeConfig(t, filepath.Join(e.projectDir, ".anvil"), map[string]any{"bouncer": bouncerBlock("enforce")})

		store := e.mustLoad(t)
		requireBouncerMode(t, store, "")

		require.NoError(t, store.ReloadFromDisk(context.Background()))
		requireBouncerMode(t, store, "")
	})

	t.Run("over global shadow", func(t *testing.T) {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
		writeConfig(t, filepath.Join(e.projectDir, ".anvil"), map[string]any{"bouncer": bouncerBlock("enforce")})

		store := e.mustLoad(t)
		requireBouncerMode(t, store, "shadow")

		require.NoError(t, store.ReloadFromDisk(context.Background()))
		requireBouncerMode(t, store, "shadow")
	})

	t.Run("added before reload", func(t *testing.T) {
		e := newBouncerEnv(t)
		e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})

		store := e.mustLoad(t)
		requireBouncerMode(t, store, "shadow")

		writeConfig(t, filepath.Join(e.projectDir, ".anvil"), map[string]any{"bouncer": bouncerBlock("enforce")})
		require.NoError(t, store.ReloadFromDisk(context.Background()))
		requireBouncerMode(t, store, "shadow")
	})
}

func TestBouncer_ProjectEnvCannotRedirectTrustedPaths(t *testing.T) {
	for _, tc := range []struct {
		name       string
		globalMode string
		envVar     string
	}{
		{name: "config without global block", envVar: "ANVIL_GLOBAL_CONFIG"},
		{name: "config with global shadow", globalMode: "shadow", envVar: "ANVIL_GLOBAL_CONFIG"},
		{name: "data without global block", envVar: "ANVIL_GLOBAL_DATA"},
		{name: "data with global shadow", globalMode: "shadow", envVar: "ANVIL_GLOBAL_DATA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBouncerEnv(t)
			global := map[string]any{}
			if tc.globalMode != "" {
				global["bouncer"] = bouncerBlock(tc.globalMode)
			}
			e.writeGlobal(t, global)

			attackerDir := filepath.Join(e.projectDir, "attacker")
			writeConfig(t, attackerDir, map[string]any{"bouncer": bouncerBlock("enforce")})
			writeConfig(t, e.projectDir, map[string]any{"env": map[string]string{tc.envVar: attackerDir}})

			store := e.mustLoad(t)
			require.Equal(t, attackerDir, os.Getenv(tc.envVar), "project env should have been applied")
			requireBouncerMode(t, store, BouncerMode(tc.globalMode))

			require.NoError(t, store.ReloadFromDisk(context.Background()))
			requireBouncerMode(t, store, BouncerMode(tc.globalMode))
		})
	}
}

func TestBouncer_ProjectEnvCannotSwapKey(t *testing.T) {
	e := newBouncerEnv(t)
	t.Setenv(DefaultBouncerAPIKeyEnv, "real-key")
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
	writeConfig(t, e.projectDir, map[string]any{"env": map[string]string{DefaultBouncerAPIKeyEnv: "attacker"}})

	store := e.mustLoad(t)
	require.Equal(t, "attacker", os.Getenv(DefaultBouncerAPIKeyEnv), "project env should have been applied")
	require.Equal(t, "real-key", store.TrustedBouncer().APIKey)

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	require.Equal(t, "real-key", store.TrustedBouncer().APIKey)
}

func TestBouncer_APIKeyEnv(t *testing.T) {
	t.Run("omitted reads BASETEN_API_KEY", func(t *testing.T) {
		e := newBouncerEnv(t)
		t.Setenv(DefaultBouncerAPIKeyEnv, "baseten-key")
		e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})

		require.Equal(t, "baseten-key", e.mustLoad(t).TrustedBouncer().APIKey)
	})

	t.Run("custom name is captured and default ignored", func(t *testing.T) {
		e := newBouncerEnv(t)
		t.Setenv(DefaultBouncerAPIKeyEnv, "baseten-key")
		t.Setenv("MY_CLASSIFIER_KEY", "custom-key")
		block := bouncerBlock("shadow")
		block["api_key_env"] = "MY_CLASSIFIER_KEY"
		e.writeGlobal(t, map[string]any{"bouncer": block})

		require.Equal(t, "custom-key", e.mustLoad(t).TrustedBouncer().APIKey)
	})

	t.Run("custom name unset leaves key empty", func(t *testing.T) {
		e := newBouncerEnv(t)
		t.Setenv(DefaultBouncerAPIKeyEnv, "baseten-key")
		t.Setenv("MY_CLASSIFIER_KEY", "")
		require.NoError(t, os.Unsetenv("MY_CLASSIFIER_KEY"))
		block := bouncerBlock("shadow")
		block["api_key_env"] = "MY_CLASSIFIER_KEY"
		e.writeGlobal(t, map[string]any{"bouncer": block})

		store, err := e.load(t)
		require.NoError(t, err)
		requireBouncerMode(t, store, "shadow")
		require.Empty(t, store.TrustedBouncer().APIKey)
	})

	t.Run("invalid name fails load", func(t *testing.T) {
		e := newBouncerEnv(t)
		block := bouncerBlock("shadow")
		block["api_key_env"] = "1BAD-NAME"
		e.writeGlobal(t, map[string]any{"bouncer": block})

		_, err := e.load(t)
		require.ErrorContains(t, err, "api_key_env")
		require.NotContains(t, err.Error(), "1BAD-NAME", "the value may be a misplaced key, so it is never echoed")
	})
}

func TestBouncer_InvalidGlobalBlockFailsLoad(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   map[string]any
		match string
	}{
		{name: "mode", set: map[string]any{"mode": "yolo"}, match: "mode"},
		{name: "auth_scheme", set: map[string]any{"auth_scheme": "Basic"}, match: "auth_scheme"},
		{name: "explicit_ask", set: map[string]any{"explicit_ask": "robot"}, match: "explicit_ask"},
		{name: "http url", set: map[string]any{"url": "http://classifier.example.com"}, match: "url"},
		{name: "escalate above deny", set: map[string]any{"escalate_at": 0.95, "deny_at": 0.9}, match: "escalate_at"},
		{name: "timeout too large", set: map[string]any{"timeout_seconds": 61}, match: "timeout_seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBouncerEnv(t)
			block := bouncerBlock("shadow")
			for k, v := range tc.set {
				block[k] = v
			}
			e.writeGlobal(t, map[string]any{"bouncer": block})

			_, err := e.load(t)
			require.ErrorContains(t, err, tc.match)
		})
	}
}

func TestBouncer_InvalidGlobalBlockFailsReload(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
	store := e.mustLoad(t)

	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("yolo")})
	require.ErrorContains(t, store.ReloadFromDisk(context.Background()), "mode")
	requireBouncerMode(t, store, "shadow")
}

func TestBouncer_WarnsOnlyWhenProjectDiffers(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const warning = "Ignoring bouncer from project config"

	e := newBouncerEnv(t)
	t.Setenv(DefaultBouncerAPIKeyEnv, "secret-key-value")
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})

	e.mustLoad(t)
	require.NotContains(t, buf.String(), warning)

	writeConfig(t, e.projectDir, map[string]any{"bouncer": bouncerBlock("enforce")})
	resetProviderState()
	e.mustLoad(t)
	require.Contains(t, buf.String(), warning)
	require.NotContains(t, buf.String(), "secret-key-value")
}

func TestBouncer_Validate(t *testing.T) {
	t.Parallel()

	f := func(v float64) *float64 { return &v }

	valid := []*Bouncer{
		nil,
		{},
		{
			Mode: "enforce", URL: "https://x.example.com/p", AuthScheme: "Bearer",
			ExplicitAsk: "human", APIKeyEnv: "_KEY_2", TimeoutSeconds: 60,
			EscalateAt: f(0), DenyAt: f(1), SeverityEscalate: f(3), UserRequestedAt: f(1),
		},
	}
	for _, p := range valid {
		require.NoError(t, p.Validate())
	}

	invalid := map[string]*Bouncer{
		"mode":                 {Mode: "on"},
		"auth_scheme":          {AuthScheme: "api-key"},
		"explicit_ask":         {ExplicitAsk: "both"},
		"url scheme":           {URL: "http://x.example.com"},
		"url relative":         {URL: "/answer"},
		"api_key_env dash":     {APIKeyEnv: "MY-KEY"},
		"api_key_env digit":    {APIKeyEnv: "1KEY"},
		"timeout negative":     {TimeoutSeconds: -1},
		"timeout high":         {TimeoutSeconds: 61},
		"escalate_at range":    {EscalateAt: f(1.1)},
		"deny_at range":        {DenyAt: f(-0.1)},
		"user_requested range": {UserRequestedAt: f(2)},
		"severity range":       {SeverityEscalate: f(3.5)},
		"escalate equals deny": {EscalateAt: f(0.9), DenyAt: f(0.9)},
	}
	for name, p := range invalid {
		require.Error(t, p.Validate(), name)
	}
}
