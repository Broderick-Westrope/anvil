package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/env"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
	"github.com/stretchr/testify/require"
)

// A resumed session's history is masked on its first request, before any
// provider is built or MCP server connects, so every credential the config
// and database already hold must be known at startup. $(command) values
// stay lazy: running them at startup would defeat lazy loading.
func TestConfiguredSecrets_SharedKeyKeepsTheFirstProvidersKind(t *testing.T) {
	t.Parallel()

	shared := "shared-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	providers := csync.NewMap[string, config.ProviderConfig]()
	for _, id := range []string{"zeta", "alpha", "mid"} {
		providers.Set(id, config.ProviderConfig{ID: id, APIKey: shared})
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))
	none := func(string) (db.McpOauthToken, error) { return db.McpOauthToken{}, sql.ErrNoRows }
	for range 5 {
		d := secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
		d.AddKnown(configuredSecrets(&config.Config{Providers: providers}, resolver, none)...)
		masked, _ := d.Mask(shared)
		require.Contains(t, masked, "[SECRET:"+"alpha_api_key:")
	}
}

func TestConfiguredSecrets(t *testing.T) {
	t.Parallel()

	const (
		providerKey = "provider-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
		unselected  = "unselected-" + "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"
		refresh     = "refresh-" + "Er5Ty7" + "Ui1Op2" + "AsZx9Q" + "w3"
		mcpHeader   = "lin_api_" + "Ty7Ui1" + "Op2AsZ" + "x9Qw3E" + "r5"
		mcpEnv      = "env-" + "Ui1Op2" + "AsZx9Q" + "w3Er5T" + "y7"
		mcpToken    = "mcp-access-" + "Op2AsZ" + "x9Qw3E" + "r5Ty7U" + "i1"
		fetchToken  = "fetch-" + "AsZx9Q" + "w3Er5T" + "y7Ui1O" + "p2"
		searchKey   = "BSA" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "AsQ"
	)
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("anthropic", config.ProviderConfig{ID: "anthropic", APIKey: "$ANTHROPIC_KEY", OAuthToken: &oauth.Token{RefreshToken: refresh}})
	providers.Set("other", config.ProviderConfig{ID: "other", APIKey: unselected})
	providers.Set("cmd", config.ProviderConfig{ID: "cmd", APIKey: "$(echo never-run-at-startup)"})
	providers.Set("backtick", config.ProviderConfig{ID: "backtick", APIKey: "`echo never-run-backtick`"})
	cfg := &config.Config{
		Providers: providers,
		MCP: config.MCPs{
			"linear": {Type: config.MCPHttp, Headers: map[string]string{"Author" + "izatio" + "n": mcpHeader, "Accept": "application/json"}, Auth: config.MCPAuthOAuth},
			"local":  {Type: config.MCPStdio, Env: map[string]string{"API_TOKEN": mcpEnv, "LOG_LEVEL": "debug"}},
		},
		Tools: config.Tools{
			Fetch:     config.ToolFetch{GitHubToken: fetchToken},
			WebSearch: config.ToolWebSearch{APIKey: searchKey},
		},
	}
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{"ANTHROPIC_KEY": providerKey}))
	tokens := func(server string) (db.McpOauthToken, error) {
		if server != "linear" {
			return db.McpOauthToken{}, sql.ErrNoRows
		}
		return db.McpOauthToken{ServerName: server, AccessToken: mcpToken}, nil
	}

	d := secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
	d.AddKnown(configuredSecrets(cfg, resolver, tokens)...)

	for _, value := range []string{providerKey, unselected, refresh, mcpHeader, mcpEnv, mcpToken, fetchToken, searchKey} {
		masked, _ := d.Mask("x " + value)
		require.NotContains(t, masked, value)
	}
	for _, ordinary := range []string{"application/json", "never-run-at-startup", "never-run-backtick", "debug"} {
		masked, _ := d.Mask("x " + ordinary)
		require.Equal(t, "x "+ordinary, masked)
	}
}

func TestProviderCredentialObserver(t *testing.T) {
	t.Parallel()

	d := secrets.New(nil, []byte("012345"+"6789ab"+"cdef01"+"234567"+"89abcd"+"ef"))
	resolver := config.NewShellVariableResolver(env.NewFromMap(map[string]string{}))
	refreshed := "refreshed-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	learnProviderCredentials(d, func() config.VariableResolver { return resolver })(config.ProviderConfig{ID: "openai", OAuthToken: &oauth.Token{AccessToken: refreshed}})

	masked, _ := d.Mask("x " + refreshed)
	require.NotContains(t, masked, refreshed)
}

// isolatedSecretsStore returns a config store that can't hold the
// developer's real credentials: config, data and HOME point at temp dirs
// and no provider is configured from the environment. Tests that build a
// detector must still assert only on booleans and masked strings, since a
// failed assertion on the detector itself would print what it knows.
func isolatedSecretsStore(t *testing.T, globalConfig string) *config.ConfigStore {
	t.Helper()
	configDir := t.TempDir()
	if globalConfig != "" {
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "anvil.json"), []byte(globalConfig), 0o600))
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_CONFIG", configDir)
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())
	store, err := config.Init(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	store.Config().Providers.Reset(map[string]config.ProviderConfig{})
	return store
}

func testQuerier(t *testing.T) db.Querier {
	t.Helper()
	dir := t.TempDir()
	conn, err := db.Connect(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Release(dir)) })
	return db.New(conn)
}

// Not parallel: sets the environment.
func TestNewSecretsDetector(t *testing.T) {
	envToken := "env-" + "Zx9Qw3" + "Er5Ty7" + "Ui1Op2" + "As"
	login := "login-" + "Qw3Er5" + "Ty7Ui1" + "Op2AsZ" + "x9"
	store := isolatedSecretsStore(t, "")
	d := newSecretsDetector(t.Context(), store, testQuerier(t), []string{"SECRETS_TEST_TOKEN=" + envToken})
	require.True(t, d != nil)

	masked, _ := d.Mask(envToken)
	require.NotContains(t, masked, envToken, "secret-named env values are known at startup")

	key, err := os.ReadFile(filepath.Join(config.GlobalDataDir(), "secrets.key"))
	require.NoError(t, err, "the key is stored for other instances")
	stored := secrets.New(nil, key)
	stored.AddKnown(secrets.Known{Kind: "secrets_test_token", Value: envToken})
	want, _ := stored.Mask(envToken)
	require.Equal(t, want, masked, "placeholders use the stored key")

	require.NoError(t, store.SetProviderAPIKey(config.ScopeGlobal, "openai", login))
	masked, _ = d.Mask(login)
	require.NotContains(t, masked, login, "a credential set later is learned through the observer")
}

// Not parallel: sets the environment.
func TestNewSecretsDetector_Off(t *testing.T) {
	store := isolatedSecretsStore(t, `{"secrets":`+`{"enabled":false}}`)
	require.True(t, newSecretsDetector(t.Context(), store, testQuerier(t), nil) == nil)
}

// Not parallel: sets the environment.
func TestNewSecretsDetector_KeyCantBeStored(t *testing.T) {
	store := isolatedSecretsStore(t, "")
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	t.Setenv("ANVIL_GLOBAL_DATA", filepath.Join(blocker, "data"))

	d := newSecretsDetector(t.Context(), store, testQuerier(t), nil)
	require.True(t, d != nil, "masking stays on with a key for this process alone")
	masked, _ := d.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.Contains(t, masked, "[SECRET:"+"stripe_key:")
	_, err := os.Stat(filepath.Join(blocker, "data", "secrets.key"))
	require.Error(t, err, "nothing was stored")
	other := newSecretsDetector(t.Context(), store, testQuerier(t), nil)
	again, _ := other.Mask("sk_live_" + "51HxYz" + "AbCdEf" + "GhIjKl" + "Mn")
	require.NotEqual(t, masked, again, "each process gets its own key")
}
