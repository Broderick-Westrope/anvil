package app

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionSmallModel(t *testing.T) {
	t.Parallel()

	ids := func(ids ...string) []catwalk.Model {
		out := make([]catwalk.Model, len(ids))
		for i, id := range ids {
			out[i] = catwalk.Model{ID: id}
		}
		return out
	}
	require.Equal(t, "gpt-5.1-codex-mini", subscriptionSmallModel(ids("gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5")).ID)
	require.Equal(t, "grok-4-fast", subscriptionSmallModel(ids("grok-4", "grok-4-fast")).ID)
	require.Equal(t, "gpt-5.1-mini", subscriptionSmallModel(ids("gpt-5.1-fast", "gpt-5.1-mini")).ID, "mini is preferred over fast")
	require.Equal(t, "last", subscriptionSmallModel(ids("first", "last")).ID, "falls back to the lightest (last) entry")
}

func TestGetDefaultSmallModel_UsesTheSubscriptionCatalog(t *testing.T) {
	// Not parallel: config.Providers caches the catalog process-wide.
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{
		ID:            "openai",
		OAuthToken:    &oauth.Token{AccessToken: "at"},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex"}, {ID: "gpt-5.1-codex-mini", DefaultMaxTokens: 777}},
	})
	cfg := &config.Config{
		Providers: providers,
		Options:   &config.Options{DisableProviderAutoUpdate: true},
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "openai", Model: "gpt-5.1-codex"},
		},
	}
	app := &App{config: config.NewTestStore(cfg)}

	small := app.GetDefaultSmallModel("openai")

	require.Equal(t, "gpt-5.1-codex-mini", small.Model, "the plan's catalog, not the API default small model")
	require.Equal(t, int64(777), small.MaxTokens)
}
