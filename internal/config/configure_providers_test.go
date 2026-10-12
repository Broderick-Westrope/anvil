package config

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/env"
	"github.com/Broderick-Westrope/anvil/internal/oauth"
	anthropicoauth "github.com/Broderick-Westrope/anvil/internal/oauth/anthropic"
	"github.com/stretchr/testify/require"
)

// Messages configureProviders logs about the providers it keeps, drops or
// discovers. Each case asserts the exact count of every one of them, and
// every line is checked against its level in configureProvidersLogs.
const (
	logMissingKey            = "Skipping provider due to missing API key"
	logAnthropicOAuthMissing = "Skipping Anthropic provider — auth_mode is \"oauth\" but no OAuth credentials found. Run `claude /login` to authenticate."
	logAnthropicKeyMissing   = "Skipping Anthropic provider — auth_mode is \"api-key\" but no API key found. Set ANTHROPIC_API_KEY."
	logAnthropicNoAuth       = "Skipping Anthropic provider — no OAuth credentials or API key found. Run `claude /login` to authenticate, or set ANTHROPIC_API_KEY."
	logVertexMissing         = "Skipping Vertex AI provider due to missing credentials"
	logAzureMissing          = "Skipping Azure provider due to missing API endpoint"
	logBedrockMissing        = "Skipping Bedrock provider due to missing AWS credentials"
	logUnsupportedType       = "Skipping custom provider due to unsupported provider type"
	logCustomDisabled        = "Skipping custom provider due to disable flag"
	logCustomMissingKey      = "Provider is missing API key, this might be OK for local providers"
	logCustomMissingURL      = "Skipping custom provider due to missing API endpoint"
	logDiscoveryFailed       = "Model discovery failed"
	logNoModelsAfterFailure  = "Skipping provider with no models after failed discovery"
	logDiscovered            = "Discovered models for provider"
	logNoModels              = "Skipping custom provider because the provider has no models"
	logEnrichmentFailed      = "Model enrichment failed"
)

var configureProvidersLogs = map[string]slog.Level{
	logMissingKey:            slog.LevelWarn,
	logAnthropicOAuthMissing: slog.LevelWarn,
	logAnthropicKeyMissing:   slog.LevelWarn,
	logAnthropicNoAuth:       slog.LevelWarn,
	logVertexMissing:         slog.LevelWarn,
	logAzureMissing:          slog.LevelWarn,
	logBedrockMissing:        slog.LevelWarn,
	logUnsupportedType:       slog.LevelWarn,
	logCustomDisabled:        slog.LevelDebug,
	logCustomMissingKey:      slog.LevelWarn,
	logCustomMissingURL:      slog.LevelWarn,
	logDiscoveryFailed:       slog.LevelWarn,
	logNoModelsAfterFailure:  slog.LevelWarn,
	logDiscovered:            slog.LevelInfo,
	logNoModels:              slog.LevelWarn,
	logEnrichmentFailed:      slog.LevelDebug,
}

// testCatalog is the Catwalk catalog the characterization cases draw their
// known providers from.
func testCatalog() map[catwalk.InferenceProvider]catwalk.Provider {
	return map[catwalk.InferenceProvider]catwalk.Provider{
		catwalk.InferenceProviderOpenAI: {
			ID:          catwalk.InferenceProviderOpenAI,
			Name:        "OpenAI",
			Type:        catwalk.TypeOpenAI,
			APIKey:      "$OPENAI_API_KEY",
			APIEndpoint: "https://api.openai.com/v1",
			DefaultHeaders: map[string]string{
				"X-Org":    "$OPENAI_ORG",
				"X-Shared": "catalog",
			},
			Models: []catwalk.Model{
				{ID: "gpt-a", Name: "GPT A"},
				{ID: "gpt-b"},
				{ID: "gpt-b", Name: "duplicate"},
			},
		},
		catwalk.InferenceProviderAnthropic: {
			ID:          catwalk.InferenceProviderAnthropic,
			Name:        "Anthropic",
			Type:        catwalk.TypeAnthropic,
			APIKey:      "$ANTHROPIC_API_KEY",
			APIEndpoint: "https://api.anthropic.com/v1",
			Models:      []catwalk.Model{{ID: "claude", Name: "Claude"}},
		},
		catwalk.InferenceProviderVertexAI: {
			ID:     catwalk.InferenceProviderVertexAI,
			Name:   "Vertex AI",
			Type:   catwalk.TypeVertexAI,
			Models: []catwalk.Model{{ID: "gemini", Name: "Gemini"}},
		},
		catwalk.InferenceProviderAzure: {
			ID:          catwalk.InferenceProviderAzure,
			Name:        "Azure",
			Type:        catwalk.TypeAzure,
			APIKey:      "$AZURE_OPENAI_API_KEY",
			APIEndpoint: "$AZURE_OPENAI_API_ENDPOINT",
			Models:      []catwalk.Model{{ID: "gpt", Name: "GPT"}},
		},
		catwalk.InferenceProviderBedrock: {
			ID:     catwalk.InferenceProviderBedrock,
			Name:   "Bedrock",
			Type:   catwalk.TypeBedrock,
			Models: []catwalk.Model{{ID: "claude-b", Name: "Claude B"}},
		},
		catwalk.InferenceProviderBedrockEurope: {
			ID:     catwalk.InferenceProviderBedrockEurope,
			Name:   "Bedrock EU",
			Type:   catwalk.TypeBedrock,
			Models: []catwalk.Model{{ID: "claude-eu", Name: "Claude EU"}},
		},
	}
}

func preparedAnthropic(mode AuthMode) ProviderConfig {
	return ProviderConfig{
		ID:             "anthropic",
		Name:           "Anthropic",
		BaseURL:        "https://api.anthropic.com/v1",
		Type:           catwalk.TypeAnthropic,
		AuthMode:       mode,
		APIKey:         "$ANTHROPIC_API_KEY",
		APIKeyTemplate: "$ANTHROPIC_API_KEY",
		ExtraHeaders:   map[string]string{},
		ExtraParams:    map[string]string{},
		Models:         []catwalk.Model{{ID: "claude", Name: "Claude"}},
	}
}

func preparedAnthropicOAuth(mode AuthMode) ProviderConfig {
	p := preparedAnthropic(mode)
	p.OAuthToken = &oauth.Token{AccessToken: "tok"}
	p.APIKey = "Bearer tok"
	p.ExtraHeaders = anthropicoauth.Headers()
	return p
}

// discoveryServer serves model listings for custom-provider discovery and
// records every path it is asked for. Base URLs under /never must not be
// requested at all.
type discoveryServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newDiscoveryServer(t *testing.T) *discoveryServer {
	t.Helper()
	s := &discoveryServer{hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/ok/"):
			_, _ = w.Write([]byte(`{"data":[{"id":"found-a"},{"id":"found-b"}]}`))
		case strings.HasPrefix(r.URL.Path, "/empty/"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasPrefix(r.URL.Path, "/llama/"):
			_, _ = w.Write([]byte(`{"data":[{"id":"local","meta":{"n_ctx":4096}}]}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *discoveryServer) takeHits() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	hits := s.hits
	s.hits = map[string]int{}
	return hits
}

// captureLogs routes slog through a JSON handler at debug level and
// returns a function that counts each of configureProvidersLogs, failing
// the test when one is logged at a level other than its own.
func captureLogs(t *testing.T) func() map[string]int {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&lockedWriter{mu: &mu, w: &buf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		counts := make(map[string]int, len(configureProvidersLogs))
		for msg := range configureProvidersLogs {
			counts[msg] = 0
		}
		scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
		for scanner.Scan() {
			var line struct {
				Level string `json:"level"`
				Msg   string `json:"msg"`
			}
			if json.Unmarshal(scanner.Bytes(), &line) != nil {
				continue
			}
			if level, watched := configureProvidersLogs[line.Msg]; watched {
				require.Equalf(t, level.String(), line.Level, "level of %q", line.Msg)
				counts[line.Msg]++
			}
		}
		return counts
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestConfigureProviders_Characterization pins what configureProviders
// does with each kind of provider, from a real anvil.json, a Catwalk
// catalog and an environment, down to the provider map and log lines it
// leaves behind.
//
// It is not parallel: it isolates the Claude CLI credential lookup by
// pointing HOME at an empty directory and emptying PATH, and it swaps
// the default slog handler.
func TestConfigureProviders_Characterization(t *testing.T) {
	path := os.Getenv("PATH")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("USER", "")
	t.Setenv("PATH", "")

	server := newDiscoveryServer(t)

	// Many providers on each side of the custom-provider loop's skips, so
	// stopping the loop early misses some whatever order the map yields
	// them in.
	var manyKnown []catwalk.Provider
	knownWant := map[string]ProviderConfig{}
	validCustom := map[string]any{}
	rejectedCustom := map[string]any{}
	customWant := map[string]ProviderConfig{}
	for i := range 8 {
		knownID := fmt.Sprintf("known-%d", i)
		manyKnown = append(manyKnown, catwalk.Provider{
			ID:          catwalk.InferenceProvider(knownID),
			Name:        knownID,
			Type:        catwalk.TypeOpenAI,
			APIKey:      "k",
			APIEndpoint: "https://known.example",
			Models:      []catwalk.Model{{ID: "m", Name: "M"}},
		})
		knownWant[knownID] = ProviderConfig{
			ID:             knownID,
			Name:           knownID,
			BaseURL:        "https://known.example",
			Type:           catwalk.TypeOpenAI,
			APIKey:         "k",
			APIKeyTemplate: "k",
			ExtraHeaders:   map[string]string{},
			ExtraParams:    map[string]string{},
			Models:         []catwalk.Model{{ID: "m", Name: "M"}},
		}

		customID := fmt.Sprintf("custom-%d", i)
		validCustom[customID] = map[string]any{"base_url": "https://custom.example", "api_key": "k", "models": []any{map[string]any{"id": "m"}}}
		customWant[customID] = ProviderConfig{
			ID:      customID,
			Name:    customID,
			BaseURL: "https://custom.example",
			Type:    catwalk.TypeOpenAICompat,
			APIKey:  "k",
			Models:  []catwalk.Model{{ID: "m"}},
		}

		rejectedCustom[fmt.Sprintf("rejected-%d", i)] = map[string]any{"api_key": "k", "models": []any{map[string]any{"id": "m"}}}
	}
	providersConfig := func(groups ...map[string]any) string {
		providers := map[string]any{}
		for _, group := range groups {
			maps.Copy(providers, group)
		}
		data, err := json.Marshal(map[string]any{"providers": providers})
		require.NoError(t, err)
		return string(data)
	}
	knownAndCustomWant := maps.Clone(knownWant)
	maps.Copy(knownAndCustomWant, customWant)

	tests := map[string]struct {
		catalog  []catwalk.InferenceProvider
		known    []catwalk.Provider
		config   string
		env      map[string]string
		want     map[string]ProviderConfig
		wantErr  string
		wantLogs map[string]int
		wantHits map[string]int
	}{
		"known provider with its key set is prepared from the catalog": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:  `{}`,
			env:     map[string]string{"OPENAI_API_KEY": "k", "OPENAI_ORG": "org"},
			want: map[string]ProviderConfig{
				"openai": {
					ID:             "openai",
					Name:           "OpenAI",
					BaseURL:        "https://api.openai.com/v1",
					Type:           catwalk.TypeOpenAI,
					APIKey:         "$OPENAI_API_KEY",
					APIKeyTemplate: "$OPENAI_API_KEY",
					ExtraHeaders:   map[string]string{"X-Org": "org", "X-Shared": "catalog"},
					ExtraParams:    map[string]string{},
					Models: []catwalk.Model{
						{ID: "gpt-a", Name: "GPT A"},
						{ID: "gpt-b"},
						{ID: "gpt-b", Name: "duplicate"},
					},
				},
			},
		},
		"user config overrides the known provider endpoint, key, models and headers": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config: `{"providers": {"openai": {
				"base_url": "https://proxy.example/v1",
				"api_key": "user-key",
				"disable": true,
				"system_prompt_prefix": "pfx",
				"extra_headers": {"X-Shared": "user", "X-Drop": "$UNSET_HEADER"},
				"extra_body": {"k": "v"},
				"models": [{"id": "gpt-a", "name": "Mine"}, {"id": "own"}, {"id": "own", "name": "dup"}]
			}}}`,
			want: map[string]ProviderConfig{
				"openai": {
					ID:                 "openai",
					Name:               "OpenAI",
					BaseURL:            "https://proxy.example/v1",
					Type:               catwalk.TypeOpenAI,
					APIKey:             "user-key",
					APIKeyTemplate:     "user-key",
					Disable:            true,
					SystemPromptPrefix: "pfx",
					ExtraHeaders:       map[string]string{"X-Shared": "user"},
					ExtraBody:          map[string]any{"k": "v"},
					ExtraParams:        map[string]string{},
					Models: []catwalk.Model{
						{ID: "gpt-a", Name: "Mine"},
						{ID: "own", Name: "own"},
						{ID: "gpt-b", Name: "gpt-b"},
					},
				},
			},
		},
		"missing key removes a configured known provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:   `{"providers": {"openai": {"base_url": "https://proxy.example/v1"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logMissingKey: 1},
		},
		"missing key leaves an unconfigured known provider out silently": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:  `{}`,
			want:    map[string]ProviderConfig{},
		},
		"known provider header that fails to resolve fails the load": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:  `{"providers": {"openai": {"extra_headers": {"X-Broken": "$(false)"}}}}`,
			env:     map[string]string{"OPENAI_API_KEY": "k"},
			wantErr: `resolving provider openai header "X-Broken"`,
		},
		"anthropic api-key mode ignores a configured oauth token": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:  `{"providers": {"anthropic": {"auth_mode": "api-key", "oauth": {"access_token": "tok"}}}}`,
			env:     map[string]string{"ANTHROPIC_API_KEY": "k"},
			want:    map[string]ProviderConfig{"anthropic": preparedAnthropic(AuthModeAPIKey)},
		},
		"anthropic api-key mode without a key removes the provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:   `{"providers": {"anthropic": {"auth_mode": "api-key", "oauth": {"access_token": "tok"}}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logAnthropicKeyMissing: 1},
		},
		"anthropic oauth mode uses the configured token": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:  `{"providers": {"anthropic": {"auth_mode": "oauth", "oauth": {"access_token": "tok"}}}}`,
			want:    map[string]ProviderConfig{"anthropic": preparedAnthropicOAuth(AuthModeOAuth)},
		},
		"anthropic oauth mode without credentials removes the provider even with a key": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:   `{"providers": {"anthropic": {"auth_mode": "oauth"}}}`,
			env:      map[string]string{"ANTHROPIC_API_KEY": "k"},
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logAnthropicOAuthMissing: 1},
		},
		"anthropic auto mode falls back to the API key": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:  `{}`,
			env:     map[string]string{"ANTHROPIC_API_KEY": "k"},
			want:    map[string]ProviderConfig{"anthropic": preparedAnthropic(AuthModeAuto)},
		},
		"anthropic auto mode uses a configured oauth token without a key": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:  `{"providers": {"anthropic": {"oauth": {"access_token": "tok"}}}}`,
			want:    map[string]ProviderConfig{"anthropic": preparedAnthropicOAuth(AuthModeAuto)},
		},
		"anthropic auto mode without credentials removes a configured provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:   `{"providers": {"anthropic": {"base_url": "https://proxy.example"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logAnthropicNoAuth: 1},
		},
		"anthropic without credentials is left out silently when unconfigured": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAnthropic},
			config:  `{}`,
			want:    map[string]ProviderConfig{},
		},
		"vertex with project and location passes them as params": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderVertexAI},
			config:  `{}`,
			env:     map[string]string{"VERTEXAI_PROJECT": "p", "VERTEXAI_LOCATION": "l"},
			want: map[string]ProviderConfig{
				"vertexai": {
					ID:           "vertexai",
					Name:         "Vertex AI",
					Type:         catwalk.TypeVertexAI,
					ExtraHeaders: map[string]string{},
					ExtraParams:  map[string]string{"project": "p", "location": "l"},
					Models:       []catwalk.Model{{ID: "gemini", Name: "Gemini"}},
				},
			},
		},
		"vertex without a location removes a configured provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderVertexAI},
			config:   `{"providers": {"vertexai": {}}}`,
			env:      map[string]string{"VERTEXAI_PROJECT": "p"},
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logVertexMissing: 1},
		},
		"azure resolves its endpoint and reads the API version without a key": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderAzure},
			config:  `{}`,
			env: map[string]string{
				"AZURE_OPENAI_API_ENDPOINT": "https://az.example",
				"AZURE_OPENAI_API_VERSION":  "2024",
			},
			want: map[string]ProviderConfig{
				"azure": {
					ID:             "azure",
					Name:           "Azure",
					BaseURL:        "https://az.example",
					Type:           catwalk.TypeAzure,
					APIKey:         "$AZURE_OPENAI_API_KEY",
					APIKeyTemplate: "$AZURE_OPENAI_API_KEY",
					ExtraHeaders:   map[string]string{},
					ExtraParams:    map[string]string{"apiVersion": "2024"},
					Models:         []catwalk.Model{{ID: "gpt", Name: "GPT"}},
				},
			},
		},
		"azure without an endpoint removes a configured provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderAzure},
			config:   `{"providers": {"azure": {}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logAzureMissing: 1},
		},
		"azure with a failing endpoint command removes a configured provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderAzure},
			config:   `{"providers": {"azure": {"base_url": "$(false)"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logAzureMissing: 1},
		},
		"bedrock uses the AWS credential chain": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderBedrock},
			config:  `{}`,
			env:     map[string]string{"AWS_REGION": "us-east-1"},
			want: map[string]ProviderConfig{
				"bedrock": {
					ID:           "bedrock",
					Name:         "Bedrock",
					Type:         catwalk.TypeBedrock,
					ExtraHeaders: map[string]string{},
					ExtraParams:  map[string]string{},
					Models:       []catwalk.Model{{ID: "claude-b", Name: "Claude B"}},
				},
			},
		},
		"bedrock with an API key needs no AWS credentials": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderBedrock},
			config:  `{"providers": {"bedrock": {"api_key": "bk"}}}`,
			want: map[string]ProviderConfig{
				"bedrock": {
					ID:             "bedrock",
					Name:           "Bedrock",
					Type:           catwalk.TypeBedrock,
					APIKey:         "bk",
					APIKeyTemplate: "bk",
					ExtraHeaders:   map[string]string{},
					ExtraParams:    map[string]string{},
					Models:         []catwalk.Model{{ID: "claude-b", Name: "Claude B"}},
				},
			},
		},
		"bedrock without credentials removes a configured provider": {
			catalog:  []catwalk.InferenceProvider{catwalk.InferenceProviderBedrock},
			config:   `{"providers": {"bedrock": {}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logBedrockMissing: 1},
		},
		"bedrock europe follows the bedrock credential rule": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderBedrockEurope},
			config:  `{}`,
			env:     map[string]string{"AWS_REGION": "eu-west-1"},
			want: map[string]ProviderConfig{
				"bedrock-europe": {
					ID:           "bedrock-europe",
					Name:         "Bedrock EU",
					Type:         catwalk.TypeBedrock,
					ExtraHeaders: map[string]string{},
					ExtraParams:  map[string]string{},
					Models:       []catwalk.Model{{ID: "claude-eu", Name: "Claude EU"}},
				},
			},
		},
		"disabling default providers ignores known providers and treats their ids as custom": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config: `{"options": {"disable_default_providers": true}, "providers": {"openai": {
				"base_url": "https://own.example/v1", "api_key": "k", "models": [{"id": "m"}]
			}}}`,
			env: map[string]string{"OPENAI_API_KEY": "k"},
			want: map[string]ProviderConfig{
				"openai": {
					ID:      "openai",
					Name:    "openai",
					BaseURL: "https://own.example/v1",
					Type:    catwalk.TypeOpenAICompat,
					APIKey:  "k",
					Models:  []catwalk.Model{{ID: "m"}},
				},
			},
		},
		"disabling default providers with nothing configured fails": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:  `{"options": {"disable_default_providers": true}}`,
			env:     map[string]string{"OPENAI_API_KEY": "k"},
			wantErr: "default providers are disabled and there are no custom providers are configured",
		},
		"no usable providers is not an error by itself": {
			catalog: []catwalk.InferenceProvider{catwalk.InferenceProviderOpenAI},
			config:  `{}`,
			want:    map[string]ProviderConfig{},
		},
		"every custom provider is prepared alongside many known providers": {
			known:  manyKnown,
			config: providersConfig(validCustom),
			want:   knownAndCustomWant,
		},
		"every rejected custom provider is removed and every valid one kept": {
			config:   providersConfig(validCustom, rejectedCustom),
			want:     customWant,
			wantLogs: map[string]int{logCustomMissingURL: 8},
		},
		"custom provider defaults its name and type and resolves only its headers": {
			config: `{"providers": {"local": {
				"base_url": "$LOCAL_URL",
				"api_key": "$LOCAL_KEY",
				"models": [{"id": "m", "name": "M"}],
				"extra_headers": {"X-Token": "$LOCAL_TOKEN", "X-Empty": "$(echo)"}
			}}}`,
			env: map[string]string{"LOCAL_URL": "https://local.example", "LOCAL_KEY": "lk", "LOCAL_TOKEN": "tok"},
			want: map[string]ProviderConfig{
				"local": {
					ID:           "local",
					Name:         "local",
					BaseURL:      "$LOCAL_URL",
					Type:         catwalk.TypeOpenAICompat,
					APIKey:       "$LOCAL_KEY",
					ExtraHeaders: map[string]string{"X-Token": "tok"},
					Models:       []catwalk.Model{{ID: "m", Name: "M"}},
				},
			},
		},
		"custom provider with configured models is not discovered": {
			config: `{"providers": {"c": {"name": "Mine", "type": "openai", "base_url": "{{URL}}/never", "api_key": "k", "models": [{"id": "m"}]}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:      "c",
					Name:    "Mine",
					BaseURL: "{{URL}}/never",
					Type:    catwalk.TypeOpenAI,
					APIKey:  "k",
					Models:  []catwalk.Model{{ID: "m"}},
				},
			},
		},
		"custom provider that opts out of discovery with no models is removed": {
			config:   `{"providers": {"c": {"base_url": "{{URL}}/never", "discover_models": false}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logCustomMissingKey: 1, logNoModels: 1},
		},
		"disabled custom provider is removed without discovery": {
			config:   `{"providers": {"c": {"base_url": "{{URL}}/never", "disable": true}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logCustomDisabled: 1},
		},
		"custom provider of an unsupported type is removed": {
			config:   `{"providers": {"c": {"type": "carrier-pigeon", "base_url": "https://x.example", "api_key": "k", "models": [{"id": "m"}]}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logUnsupportedType: 1},
		},
		"custom provider without a base url is removed": {
			config:   `{"providers": {"c": {"api_key": "k", "models": [{"id": "m"}]}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logCustomMissingURL: 1},
		},
		"custom provider without a base url is removed before discovery": {
			config:   `{"providers": {"c": {"api_key": "k"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logCustomMissingURL: 1},
		},
		"custom base url that resolves empty removes the provider": {
			config:   `{"providers": {"c": {"base_url": "$UNSET_URL", "api_key": "k", "models": [{"id": "m"}]}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logCustomMissingURL: 1},
		},
		"custom api key that resolves empty only warns": {
			config: `{"providers": {"c": {"base_url": "https://x.example", "api_key": "$UNSET_KEY", "models": [{"id": "m"}]}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:      "c",
					Name:    "c",
					BaseURL: "https://x.example",
					Type:    catwalk.TypeOpenAICompat,
					APIKey:  "$UNSET_KEY",
					Models:  []catwalk.Model{{ID: "m"}},
				},
			},
			wantLogs: map[string]int{logCustomMissingKey: 1},
		},
		"custom provider without an api key warns twice": {
			config: `{"providers": {"c": {"base_url": "https://x.example", "models": [{"id": "m"}]}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:      "c",
					Name:    "c",
					BaseURL: "https://x.example",
					Type:    catwalk.TypeOpenAICompat,
					Models:  []catwalk.Model{{ID: "m"}},
				},
			},
			wantLogs: map[string]int{logCustomMissingKey: 2},
		},
		"custom provider header that fails to resolve fails the load": {
			config:  `{"providers": {"c": {"base_url": "https://x.example", "api_key": "k", "models": [{"id": "m"}], "extra_headers": {"X-Broken": "$(false)"}}}}`,
			wantErr: `resolving provider c header "X-Broken"`,
		},
		"discovery fills an empty model list": {
			config: `{"providers": {"c": {"base_url": "{{URL}}/ok", "api_key": "k"}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:      "c",
					Name:    "c",
					BaseURL: "{{URL}}/ok",
					Type:    catwalk.TypeOpenAICompat,
					APIKey:  "k",
					Models:  []catwalk.Model{{ID: "found-a", Name: "found-a"}, {ID: "found-b", Name: "found-b"}},
				},
			},
			wantLogs: map[string]int{logDiscovered: 1},
			wantHits: map[string]int{"/ok/models": 1},
		},
		"explicit discovery merges into configured models": {
			config: `{"providers": {"c": {"base_url": "{{URL}}/ok", "api_key": "k", "discover_models": true, "models": [{"id": "found-a", "name": "Mine"}]}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:                 "c",
					Name:               "c",
					BaseURL:            "{{URL}}/ok",
					Type:               catwalk.TypeOpenAICompat,
					APIKey:             "k",
					AutoDiscoverModels: new(true),
					Models:             []catwalk.Model{{ID: "found-a", Name: "Mine"}, {ID: "found-b", Name: "found-b"}},
				},
			},
			wantLogs: map[string]int{logDiscovered: 1},
			wantHits: map[string]int{"/ok/models": 1},
		},
		"failed discovery keeps configured models": {
			config: `{"providers": {"c": {"base_url": "{{URL}}/fail", "api_key": "k", "discover_models": true, "models": [{"id": "m"}]}}}`,
			want: map[string]ProviderConfig{
				"c": {
					ID:                 "c",
					Name:               "c",
					BaseURL:            "{{URL}}/fail",
					Type:               catwalk.TypeOpenAICompat,
					APIKey:             "k",
					AutoDiscoverModels: new(true),
					Models:             []catwalk.Model{{ID: "m"}},
				},
			},
			wantLogs: map[string]int{logDiscoveryFailed: 1},
			wantHits: map[string]int{"/fail/models": 1},
		},
		"failed discovery with no models removes the provider": {
			config:   `{"providers": {"c": {"base_url": "{{URL}}/fail", "api_key": "k"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logDiscoveryFailed: 1, logNoModelsAfterFailure: 1},
			wantHits: map[string]int{"/fail/models": 1},
		},
		"known custom type is accepted and its discovered models enriched": {
			config: `{"providers": {"llama": {"type": "llamacpp", "base_url": "{{URL}}/llama", "api_key": "k"}}}`,
			want: map[string]ProviderConfig{
				"llama": {
					ID:      "llama",
					Name:    "llama",
					BaseURL: "{{URL}}/llama",
					Type:    "llamacpp",
					APIKey:  "k",
					Models:  []catwalk.Model{{ID: "local", Name: "local", ContextWindow: 4096}},
				},
			},
			wantLogs: map[string]int{logDiscovered: 1},
			wantHits: map[string]int{"/llama/models": 1, "/llama/v1/models": 1},
		},
		"discovery that finds nothing removes the provider without enriching": {
			config:   `{"providers": {"llama": {"type": "llamacpp", "base_url": "{{URL}}/empty", "api_key": "k"}}}`,
			want:     map[string]ProviderConfig{},
			wantLogs: map[string]int{logNoModels: 1},
			wantHits: map[string]int{"/empty/models": 1},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			countLogs := captureLogs(t)
			server.takeHits()

			cfg, err := loadFromBytes([][]byte{[]byte(strings.ReplaceAll(tc.config, "{{URL}}", server.URL))})
			require.NoError(t, err)
			cfg.setDefaults(t.TempDir(), "")

			catalog := testCatalog()
			known := slices.Clone(tc.known)
			for _, id := range tc.catalog {
				known = append(known, catalog[id])
			}
			vars := map[string]string{"PATH": path}
			for k, v := range tc.env {
				vars[k] = v
			}
			testEnv := env.NewFromMap(vars)

			err = cfg.configureProviders(context.Background(), testStore(cfg), testEnv, NewShellVariableResolver(testEnv), known)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)

			got := make(map[string]ProviderConfig)
			for id, pc := range cfg.Providers.Seq2() {
				got[id] = pc
			}
			want := make(map[string]ProviderConfig, len(tc.want))
			for id, pc := range tc.want {
				pc.BaseURL = strings.ReplaceAll(pc.BaseURL, "{{URL}}", server.URL)
				want[id] = pc
			}
			require.Equal(t, want, got)

			wantLogs := make(map[string]int, len(configureProvidersLogs))
			for msg := range configureProvidersLogs {
				wantLogs[msg] = tc.wantLogs[msg]
			}
			require.Equal(t, wantLogs, countLogs())

			hits := server.takeHits()
			for path, n := range hits {
				require.Falsef(t, strings.HasPrefix(path, "/never/"), "discovery requested %s %d times", path, n)
			}
			if tc.wantHits == nil {
				tc.wantHits = map[string]int{}
			}
			require.Equal(t, tc.wantHits, hits)
		})
	}
}
