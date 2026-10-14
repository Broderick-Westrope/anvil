package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/env"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	"github.com/stretchr/testify/require"
)

func TestGetModelSearchesSubscriptionCatalogs(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{
		Models:        []catwalk.Model{{ID: "gpt-5.1"}},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex"}},
	})
	providers.Set("xai", ProviderConfig{GrokModels: []catwalk.Model{{ID: "grok-4-fast", Name: "Grok 4 Fast"}}})
	cfg := &Config{Providers: providers}

	require.Equal(t, "GPT-5.1 Codex", cfg.GetModel("openai", "gpt-5.1-codex").Name)
	require.NotNil(t, cfg.GetModel("openai", "gpt-5.1"))
	require.Equal(t, "Grok 4 Fast", cfg.GetModel("xai", "grok-4-fast").Name)
	require.Nil(t, cfg.GetModel("openai", "missing"))
}

func TestSubscriptionModels(t *testing.T) {
	t.Parallel()

	token := &oauth.Token{AccessToken: "at"}
	chat := []catwalk.Model{{ID: "codex"}}
	grokModels := []catwalk.Model{{ID: "grok"}}

	require.Equal(t, chat, (&ProviderConfig{ID: "openai", OAuthToken: token, ChatGPTModels: chat}).SubscriptionModels())
	require.Equal(t, grokModels, (&ProviderConfig{ID: "xai", OAuthToken: token, GrokModels: grokModels}).SubscriptionModels())
	require.Nil(t, (&ProviderConfig{ID: "openai", ChatGPTModels: chat}).SubscriptionModels(), "no login, no subscription catalog")
	require.Nil(t, (&ProviderConfig{ID: "anthropic", OAuthToken: token}).SubscriptionModels())
}

func TestTokenFields(t *testing.T) {
	t.Parallel()

	fields := tokenFields("openai", &oauth.Token{AccessToken: "at"})
	require.Contains(t, fields, "providers.openai.oauth")
	require.NotContains(t, fields, "providers.openai.api_key", "a ChatGPT login is not an API key")

	for _, id := range []string{"xai", "anthropic"} {
		fields := tokenFields(id, &oauth.Token{AccessToken: "at"})
		require.Equal(t, "at", fields["providers."+id+".api_key"], id)
	}
}

func TestTokenAccountIDRoundTrip(t *testing.T) {
	t.Parallel()

	token := &oauth.Token{AccessToken: "at", RefreshToken: "rt", IDToken: "idt", AccountID: "acct-42", ExpiresAt: 4102444800}
	data, err := json.Marshal(token)
	require.NoError(t, err)
	var restored oauth.Token
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Equal(t, *token, restored)
}

// stubSubscriptionFetch replaces the catalog fetch for the test and
// records the provider and token it was called with.
func stubSubscriptionFetch(t *testing.T, models map[string][]catwalk.Model, err error) *[]string {
	t.Helper()
	var calls []string
	original := fetchSubscriptionModels
	fetchSubscriptionModels = func(_ context.Context, providerID string, token *oauth.Token) ([]catwalk.Model, error) {
		calls = append(calls, providerID+":"+token.AccessToken)
		return models[providerID], err
	}
	t.Cleanup(func() { fetchSubscriptionModels = original })
	return &calls
}

// newDiskStore writes initialConfig to a global data file and returns a
// store whose in-memory config mirrors it, as a real load would.
func newDiskStore(t *testing.T, initialConfig string) *ConfigStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "anvil.json")
	require.NoError(t, os.WriteFile(path, []byte(initialConfig), 0o600))
	providers := csync.NewMap[string, ProviderConfig]()
	require.NoError(t, json.Unmarshal([]byte(initialConfig), &struct {
		Providers *csync.Map[string, ProviderConfig] `json:"providers"`
	}{Providers: providers}))
	return &ConfigStore{config: &Config{Providers: providers}, globalDataPath: path, workingDir: filepath.Dir(path)}
}

func readDisk(t *testing.T, s *ConfigStore) string {
	t.Helper()
	data, err := os.ReadFile(s.globalDataPath)
	require.NoError(t, err)
	return string(data)
}

func TestSetProviderAPIKey_SubscriptionLoginsAreEitherOr(t *testing.T) {
	// Not parallel: swaps fetchSubscriptionModels and sets env.
	t.Setenv("ANVIL_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_DATA", t.TempDir())

	token := &oauth.Token{AccessToken: "login-at", RefreshToken: "login-rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	stubSubscriptionFetch(t, map[string][]catwalk.Model{
		"openai": {{ID: "gpt-5.1-codex"}},
		"xai":    {{ID: "grok-4-fast"}},
	}, nil)

	for _, tc := range []struct {
		provider, catalogField, catalogModel string
		mirrorsKey                           bool
	}{
		{"openai", "chatgpt_models", "gpt-5.1-codex", false},
		{"xai", "grok_models", "grok-4-fast", true},
	} {
		t.Run(tc.provider+" login retires the api key", func(t *testing.T) {
			s := newDiskStore(t, `{"providers":{"`+tc.provider+`":{"id":"`+tc.provider+`","api_key":"sk-keep","models":[{"id":"api-model"}]}}}`)

			require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, tc.provider, token))

			pc, ok := s.Config().Providers.Get(tc.provider)
			require.True(t, ok)
			require.Equal(t, token, pc.OAuthToken)
			require.Equal(t, tc.catalogModel, pc.SubscriptionModels()[0].ID, "the login fetches its catalog")
			require.Equal(t, "api-model", pc.Models[0].ID, "the API catalog is untouched")
			disk := readDisk(t, s)
			require.NotContains(t, disk, "sk-keep", "the old key is gone from disk")
			require.Contains(t, disk, "login-rt")
			require.Contains(t, disk, tc.catalogField)
			if tc.mirrorsKey {
				require.Equal(t, "login-at", pc.APIKey, "xAI sends the token as its API key")
			} else {
				require.Empty(t, pc.APIKey, "OpenAI keeps no API key next to a ChatGPT login")
			}
		})

		t.Run(tc.provider+" api key retires the login", func(t *testing.T) {
			s := newDiskStore(t, `{"providers":{"`+tc.provider+`":{"id":"`+tc.provider+`",
				"oauth":{"access_token":"old-at","refresh_token":"old-rt"},
				"`+tc.catalogField+`":[{"id":"stale-sub-model"}]}}}`)

			require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, tc.provider, "sk-new"))

			pc, ok := s.Config().Providers.Get(tc.provider)
			require.True(t, ok)
			require.Equal(t, "sk-new", pc.APIKey)
			require.Nil(t, pc.OAuthToken)
			require.Nil(t, pc.SubscriptionModels())
			require.Empty(t, pc.ChatGPTModels)
			require.Empty(t, pc.GrokModels)
			disk := readDisk(t, s)
			require.NotContains(t, disk, "old-rt")
			require.NotContains(t, disk, "stale-sub-model")
		})
	}

	t.Run("anthropic login still mirrors the token and fetches no catalog", func(t *testing.T) {
		calls := stubSubscriptionFetch(t, nil, nil)
		s := newDiskStore(t, `{"providers":{"anthropic":{"id":"anthropic"}}}`)

		require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, "anthropic", token))

		pc, _ := s.Config().Providers.Get("anthropic")
		require.Equal(t, "Bearer login-at", pc.APIKey)
		require.Empty(t, *calls)
	})
}

func TestSetProviderAPIKey_RejectsUnknownCredentialTypes(t *testing.T) {
	s := newDiskStore(t, `{"providers":{"openai":{"id":"openai","api_key":"sk-old"}}}`)
	before := readDisk(t, s)

	require.ErrorContains(t, s.SetProviderAPIKey(ScopeGlobal, "openai", 42), "unsupported credential type int")

	pc, _ := s.Config().Providers.Get("openai")
	require.Equal(t, "sk-old", pc.APIKey)
	require.Equal(t, before, readDisk(t, s))
}

func TestSetProviderAPIKey_UnconfiguredProvider(t *testing.T) {
	t.Run("known provider the reload skipped is added from the catalog", func(t *testing.T) {
		s := newDiskStore(t, `{}`)

		// An empty key leaves the provider unconfigured after the reload
		// that saving it triggers, so the store builds it from the catalog.
		require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, "groq", ""))

		idx := slices.IndexFunc(s.KnownProviders(), func(p catwalk.Provider) bool { return p.ID == "groq" })
		require.GreaterOrEqual(t, idx, 0, "groq must be in the embedded catalog")
		known := s.KnownProviders()[idx]
		pc, exists := s.Config().Providers.Get("groq")
		require.True(t, exists)
		require.Equal(t, ProviderConfig{
			ID:           "groq",
			Name:         known.Name,
			BaseURL:      known.APIEndpoint,
			Type:         known.Type,
			ExtraHeaders: map[string]string{},
			ExtraParams:  map[string]string{},
			Models:       known.Models,
		}, pc)
	})

	t.Run("a failed save changes nothing in memory", func(t *testing.T) {
		s := newDiskStore(t, `{"providers":{"openai":{"id":"openai","api_key":"sk-old"}}}`)
		// A path under a regular file can't be written.
		s.globalDataPath = filepath.Join(s.globalDataPath, "anvil.json")

		err := s.SetProviderAPIKey(ScopeGlobal, "openai", "sk-new")

		require.ErrorContains(t, err, "failed to save api key to config file")
		pc, _ := s.Config().Providers.Get("openai")
		require.Equal(t, "sk-old", pc.APIKey)
	})

	t.Run("unknown provider is an error", func(t *testing.T) {
		s := newDiskStore(t, `{}`)

		err := s.SetProviderAPIKey(ScopeGlobal, "nope", "sk-new")

		require.EqualError(t, err, "provider with ID nope not found in known providers")
		_, exists := s.Config().Providers.Get("nope")
		require.False(t, exists)
	})
}

func TestSetProviderAPIKey_FailedCatalogFetchKeepsTheLogin(t *testing.T) {
	stubSubscriptionFetch(t, nil, errors.New("backend down"))
	s := newDiskStore(t, `{"providers":{"openai":{"id":"openai"}}}`)
	token := &oauth.Token{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour).Unix()}

	require.NoError(t, s.SetProviderAPIKey(ScopeGlobal, "openai", token))

	pc, _ := s.Config().Providers.Get("openai")
	require.Equal(t, token, pc.OAuthToken)
	require.Empty(t, pc.ChatGPTModels)
}

func TestRefetchSubscriptionModels_RefreshesAnExpiredToken(t *testing.T) {
	for _, id := range []string{"openai", "xai"} {
		t.Run(id, func(t *testing.T) {
			expired := &oauth.Token{AccessToken: "expired-at", RefreshToken: "expired-rt", ExpiresIn: 3600, ExpiresAt: time.Now().Add(-time.Hour).Unix()}
			fresh := &oauth.Token{AccessToken: "fresh-at", RefreshToken: "fresh-rt", ExpiresIn: 3600, ExpiresAt: time.Now().Add(time.Hour).Unix()}
			providers := csync.NewMap[string, ProviderConfig]()
			providers.Set(id, ProviderConfig{ID: id, OAuthToken: expired})
			s := &ConfigStore{
				config:         &Config{Providers: providers},
				globalDataPath: filepath.Join(t.TempDir(), "anvil.json"),
				exchangeToken: func(_ context.Context, providerID, refreshToken string) (*oauth.Token, error) {
					require.Equal(t, id, providerID)
					require.Equal(t, "expired-rt", refreshToken)
					return fresh, nil
				},
			}
			calls := stubSubscriptionFetch(t, map[string][]catwalk.Model{id: {{ID: "fresh-model"}}}, nil)

			s.refetchSubscriptionModels(t.Context(), ScopeGlobal, id)

			require.Equal(t, []string{id + ":fresh-at"}, *calls, "the catalog is fetched with the renewed token")
			pc, _ := s.Config().Providers.Get(id)
			require.Equal(t, fresh, pc.OAuthToken)
			require.Equal(t, "fresh-model", pc.SubscriptionModels()[0].ID)
			disk := readDisk(t, s)
			require.Contains(t, disk, "fresh-model")
			require.Contains(t, disk, "fresh-rt")
		})
	}
}

func TestRefetchMissingSubscriptionModels_OnlyFillsGaps(t *testing.T) {
	token := &oauth.Token{AccessToken: "at", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	providers := csync.NewMap[string, ProviderConfig]()
	providers.Set("openai", ProviderConfig{ID: "openai", OAuthToken: token, ChatGPTModels: []catwalk.Model{{ID: "have"}}})
	providers.Set("xai", ProviderConfig{ID: "xai", OAuthToken: token})
	s := &ConfigStore{config: &Config{Providers: providers}, globalDataPath: filepath.Join(t.TempDir(), "anvil.json")}
	calls := stubSubscriptionFetch(t, map[string][]catwalk.Model{"xai": {{ID: "grok-4-fast"}}}, nil)

	s.RefetchMissingSubscriptionModels(t.Context())
	require.Equal(t, []string{"xai:at"}, *calls, "only the provider without a catalog is fetched")

	s.RefreshSubscriptionModels(t.Context())
	require.ElementsMatch(t, []string{"xai:at", "openai:at", "xai:at"}, *calls, "a refresh renews every signed-in catalog")
}

func TestConfigureProviders_SubscriptionLoginCountsAsACredential(t *testing.T) {
	t.Parallel()

	known := []catwalk.Provider{
		{ID: "openai", APIKey: "$OPENAI_API_KEY", APIEndpoint: "https://api.openai.com/v1", Models: []catwalk.Model{{ID: "gpt-5.1"}}},
		{ID: "xai", APIKey: "$XAI_API_KEY", APIEndpoint: "https://api.x.ai/v1", Models: []catwalk.Model{{ID: "grok-4"}}},
	}
	cfg := &Config{Providers: csync.NewMap[string, ProviderConfig]()}
	cfg.Providers.Set("openai", ProviderConfig{
		OAuthToken:    &oauth.Token{AccessToken: "at"},
		ChatGPTModels: []catwalk.Model{{ID: "gpt-5.1-codex"}},
	})
	cfg.Providers.Set("xai", ProviderConfig{
		OAuthToken: &oauth.Token{AccessToken: "at"},
		GrokModels: []catwalk.Model{{ID: "grok-4-fast"}},
	})
	cfg.setDefaults("/tmp", "")
	e := env.NewFromMap(map[string]string{})

	require.NoError(t, cfg.configureProviders(t.Context(), testStore(cfg), e, NewShellVariableResolver(e), known))

	for id, model := range map[string]string{"openai": "gpt-5.1-codex", "xai": "grok-4-fast"} {
		pc, ok := cfg.Providers.Get(id)
		require.True(t, ok, "%s signed in without an API key must stay configured", id)
		require.Equal(t, model, pc.SubscriptionModels()[0].ID, "%s keeps its subscription catalog through load", id)
	}
}
