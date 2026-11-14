package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/secrets"
)

// newSecretsDetector builds the process's one detector from the settings
// at startup, or returns nil when masking is off. It starts out knowing the
// secret-looking values in environ (KEY=value pairs, os.Environ in
// production) and every credential the config and database already hold,
// since a resumed session's history is masked before any provider is built
// or MCP server connects. The code that resolves the rest (a $(command)
// value, a refreshed token) adds them as it goes.
func newSecretsDetector(ctx context.Context, store *config.ConfigStore, q db.Querier, environ []string) *secrets.Detector {
	settings := store.SecretsAtStartup()
	if !settings.MaskingEnabled() {
		slog.Info("Secret masking is off; credentials reach models as written")
		return nil
	}
	key, err := secrets.LoadKey(config.GlobalDataDir())
	if err != nil {
		// Masking matters more than stable placeholders, so a key that
		// can't be stored is replaced by one for this process alone.
		slog.Warn("Failed to load secrets key; placeholders will differ from other instances", "error", err)
		key = secrets.RandomKey()
	}
	d := secrets.New(settings.AllowList(), key)
	d.AddKnown(secrets.EnvKnown(environ)...)
	tokens := func(server string) (db.McpOauthToken, error) {
		return q.GetMCPOAuthToken(ctx, server)
	}
	d.AddKnown(configuredSecrets(store.Config(), store.Resolver(), tokens)...)
	store.ObserveProviderCredentials(learnProviderCredentials(d, store.Resolver))
	return d
}

// learnProviderCredentials returns an observer that adds a provider's
// credentials to d whenever one is set, such as a refreshed OAuth token.
// It asks for the resolver each time, since a reload replaces it.
func learnProviderCredentials(d *secrets.Detector, resolver func() config.VariableResolver) func(config.ProviderConfig) {
	return func(p config.ProviderConfig) {
		d.AddKnown(p.Credentials(withoutCommands(resolver())(p.APIKey))...)
	}
}

// withoutCommands resolves $VAR references but leaves values that run a
// command ($(...) or backticks) unresolved, so learning secrets never runs
// anything.
func withoutCommands(resolver config.VariableResolver) func(string) string {
	return func(value string) string {
		if value == "" || strings.Contains(value, "$(") || strings.Contains(value, "`") {
			return ""
		}
		v, err := resolver.ResolveValue(value)
		if err != nil {
			return ""
		}
		return v
	}
}

// configuredSecrets returns the credentials in cfg and the stored MCP
// tokens: every provider's key and OAuth tokens, every MCP server's
// credential headers, env and tokens, and the web tools' keys. Values that
// run a command are left for their first use.
func configuredSecrets(cfg *config.Config, resolver config.VariableResolver, tokens func(server string) (db.McpOauthToken, error)) []secrets.Known {
	resolve := withoutCommands(resolver)
	resolveAll := func(values map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range values {
			out[k] = resolve(v)
		}
		return out
	}

	var known []secrets.Known
	if cfg.Providers != nil {
		// Sorted, so a key two providers share always gets the first
		// one's kind and its placeholder is stable across restarts.
		// Provider headers were resolved when the config loaded.
		providers := cfg.Providers.Copy()
		for _, id := range slices.Sorted(maps.Keys(providers)) {
			p := providers[id]
			known = append(known, p.Credentials(resolve(p.APIKey))...)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.MCP)) {
		m := cfg.MCP[name]
		var envs []string
		for _, k := range slices.Sorted(maps.Keys(m.Env)) {
			envs = append(envs, k+"="+resolve(m.Env[k]))
		}
		known = append(known, mcp.ServerSecrets(name, resolveAll(m.Headers), envs, resolve(m.ClientSecret))...)
		row, err := tokens(name)
		switch {
		case err == nil:
			known = append(known, mcp.TokenSecrets(name, row.AccessToken, row.RefreshToken.String, row.ClientSecret.String)...)
		case !errors.Is(err, sql.ErrNoRows):
			// The token is learned when the server connects instead.
			slog.Warn("Failed to load MCP token for secret masking", "server", name, "error", err)
		}
	}
	known = append(known,
		secrets.Known{Kind: agent.GitHubTokenSecretKind, Value: resolve(cfg.Tools.Fetch.GitHubToken)},
		secrets.Known{Kind: agent.WebSearchKeySecretKind, Value: resolve(cfg.Tools.WebSearch.APIKey)},
	)
	return known
}
