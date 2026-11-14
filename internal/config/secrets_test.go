package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/secrets"

	"github.com/stretchr/testify/require"
)

func TestSecrets_OnByDefault(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{})

	cfg := e.mustLoad(t).Config()
	require.True(t, cfg.Secrets.MaskingEnabled())
	require.Empty(t, cfg.Secrets.AllowList())
}

func TestSecrets_GlobalSettingsLoad(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"enabled": false, "allow": []string{"sk_test_sample"}}})

	cfg := e.mustLoad(t).Config()
	require.False(t, cfg.Secrets.MaskingEnabled())
	require.Equal(t, []string{"sk_test_sample"}, cfg.Secrets.AllowList())
}

func TestSecrets_ProjectAndWorkspaceIgnored(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"allow": []string{"global"}}})
	project := map[string]any{"secrets": map[string]any{"enabled": false, "allow": []string{"re:.*"}}}
	writeConfig(t, e.projectDir, project)
	writeConfig(t, filepath.Join(e.projectDir, ".anvil"), project)

	store := e.mustLoad(t)
	require.True(t, store.Config().Secrets.MaskingEnabled())
	require.Equal(t, []string{"global"}, store.Config().Secrets.AllowList())

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	require.True(t, store.Config().Secrets.MaskingEnabled())
	require.Equal(t, []string{"global"}, store.Config().Secrets.AllowList())
}

func TestSecrets_ProjectAloneCannotDisable(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{})
	writeConfig(t, e.projectDir, map[string]any{"secrets": map[string]any{"enabled": false}})

	require.True(t, e.mustLoad(t).Config().Secrets.MaskingEnabled())
}

func TestSecrets_InvalidGlobalRejected(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"secrets": "off"})

	_, err := e.load(t)
	require.ErrorContains(t, err, "secrets")
}

func TestSecrets_StartupSettingsSurviveReload(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{})
	store := e.mustLoad(t)

	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"enabled": false}})
	require.NoError(t, store.ReloadFromDisk(context.Background()))

	require.True(t, store.SecretsAtStartup().MaskingEnabled())
	startup, _, startupRaw := store.StartupSnapshot()
	require.Contains(t, RestartRequired(startup, store.Config(), nil, nil, startupRaw, store.RawProjectDirectory()), "secrets")
}

func TestObserveProviderCredentials(t *testing.T) {
	// Not parallel: sets env and swaps fetchSubscriptionModels.
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	stubSubscriptionFetch(t, map[string][]catwalk.Model{"openai": {{ID: "gpt-5.1-codex"}}}, nil)

	s := newDiskStore(t, `{"providers":{"openai":{"id":"openai","api_key":"`+`sk-old","models":[{"id":"m"}]}}}`)
	var seen []ProviderConfig
	s.ObserveProviderCredentials(func(pc ProviderConfig) { seen = append(seen, pc) })

	token := &oauth.Token{AccessToken: "login-at", RefreshToken: "login-rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, "openai", token))

	require.Len(t, seen, 1)
	require.Equal(t, "openai", seen[0].ID)
	require.Equal(t, token, seen[0].OAuthToken)
}

// Every refresh path (a 401, expiry, adopting a peer's token from disk,
// the startup subscription refresh) lands in applyToken.
func TestObserveProviderCredentials_HearsRefreshedTokens(t *testing.T) {
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())

	s := newDiskStore(t, `{"providers":{"openai":{"id":"openai","models":[{"id":"m"}]}}}`)
	var seen []ProviderConfig
	s.ObserveProviderCredentials(func(pc ProviderConfig) { seen = append(seen, pc) })

	pc, _ := s.Config().Providers.Get("openai")
	refreshed := &oauth.Token{AccessToken: "refreshed-at", RefreshToken: "refreshed-rt"}
	require.NoError(t, s.applyToken(pc, refreshed, "openai"))

	require.Len(t, seen, 1)
	require.Equal(t, refreshed, seen[0].OAuthToken)
}

func TestSecrets_InvalidAllowRegexRejected(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"allow": []string{"ok", "re:("}}})

	_, err := e.load(t)
	require.ErrorContains(t, err, "allow[1]")
}

func TestSecrets_AllowChangeNeedsRestart(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"allow": []string{"a"}}})
	store := e.mustLoad(t)

	e.writeGlobal(t, map[string]any{"secrets": map[string]any{"allow": []string{"b"}}})
	require.NoError(t, store.ReloadFromDisk(context.Background()))

	startup, _, startupRaw := store.StartupSnapshot()
	require.Equal(t, []string{"secrets"}, RestartRequired(startup, store.Config(), nil, nil, startupRaw, store.RawProjectDirectory()))
}

func TestProviderConfig_Credentials(t *testing.T) {
	t.Parallel()

	values := func(known []secrets.Known) map[string]string {
		got := map[string]string{}
		for _, k := range known {
			got[k.Value] = k.Kind
		}
		return got
	}

	p := ProviderConfig{ID: "anthropic", ExtraHeaders: map[string]string{"X-Api-Key": "hdr", "Accept": "json"}}
	require.Equal(t, map[string]string{"key": "anthropic_api_key", "hdr": "anthropic_api_key"}, values(p.Credentials("key")))

	p.OAuthToken = &oauth.Token{AccessToken: "at", RefreshToken: "rt"}
	got := values(p.Credentials("Bearer at"))
	require.Equal(t, map[string]string{"Bearer at": "anthropic_api_key", "at": "anthropic_api_key", "rt": "anthropic_api_key", "hdr": "anthropic_api_key"}, got)
}

func TestLoadTrustedBlock_SkipsMissingEmptyAndNullBlocks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		return path
	}
	paths := []string{
		"",
		filepath.Join(dir, "missing.json"),
		write("empty.json", ""),
		write("null.json", `{"secrets": `+`null}`),
		write("none.json", `{"yolo": "off"}`),
		write("set.json", `{"secrets": `+`{"allow": ["a"]}}`),
	}
	merged, err := loadTrustedBlock(paths, "secrets")
	require.NoError(t, err)
	require.JSONEq(t, `{"allow": ["a"]}`, string(merged))

	merged, err = loadTrustedBlock(paths[:5], "secrets")
	require.NoError(t, err)
	require.Nil(t, merged)
}
