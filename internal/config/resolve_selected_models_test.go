package config

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/stretchr/testify/require"
)

// TestResolveSelectedModels_Characterization pins how configured large and
// small selections are laid over the default selection and checked
// against the configured providers' models.
func TestResolveSelectedModels_Characterization(t *testing.T) {
	t.Parallel()

	catalog := []catwalk.Provider{{
		ID:                  "openai",
		DefaultLargeModelID: "big",
		DefaultSmallModelID: "tiny",
	}}
	openai := ProviderConfig{
		ID: "openai",
		Models: []catwalk.Model{
			{ID: "big", DefaultMaxTokens: 1000, DefaultReasoningEffort: "medium"},
			{ID: "tiny", DefaultMaxTokens: 500, DefaultReasoningEffort: "low"},
			{ID: "alt", DefaultMaxTokens: 2000, DefaultReasoningEffort: "high"},
		},
	}
	local := ProviderConfig{
		ID: "local",
		Models: []catwalk.Model{
			{ID: "l1", DefaultMaxTokens: 300},
			{ID: "l2", DefaultMaxTokens: 400},
		},
	}
	everyField := func(provider, model string) SelectedModel {
		return SelectedModel{
			Provider:         provider,
			Model:            model,
			Variant:          "v",
			MaxTokens:        42,
			ReasoningEffort:  "xhigh",
			Think:            true,
			Temperature:      new(0.1),
			TopP:             new(0.2),
			TopK:             new(int64(3)),
			FrequencyPenalty: new(0.4),
			PresencePenalty:  new(0.5),
		}
	}
	// everyFieldResolved is everyField as resolved: Variant isn't carried
	// over from the configured selection.
	everyFieldResolved := func(provider, model string) SelectedModel {
		m := everyField(provider, model)
		m.Variant = ""
		return m
	}
	defaultLarge := SelectedModel{Provider: "openai", Model: "big", MaxTokens: 1000, ReasoningEffort: "medium"}
	defaultSmall := SelectedModel{Provider: "openai", Model: "tiny", MaxTokens: 500, ReasoningEffort: "low"}

	tests := map[string]struct {
		catalog   []catwalk.Provider
		providers map[string]ProviderConfig
		models    map[SelectedModelType]SelectedModel
		want      resolvedModels
		wantErr   string
	}{
		"nothing configured uses the default selection": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			want:      resolvedModels{Large: defaultLarge, Small: defaultSmall},
		},
		"configured large overrides every field it sets": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeLarge: everyField("", "alt")},
			want:      resolvedModels{Large: everyFieldResolved("openai", "alt"), Small: defaultSmall},
		},
		"configured large with only a model takes that model's defaults": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeLarge: {Model: "alt"}},
			want: resolvedModels{
				Large: SelectedModel{Provider: "openai", Model: "alt", MaxTokens: 2000, ReasoningEffort: "high"},
				Small: defaultSmall,
			},
		},
		"configured small overrides every field it sets": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeSmall: everyField("", "alt")},
			want:      resolvedModels{Large: defaultLarge, Small: everyFieldResolved("openai", "alt")},
		},
		"configured small on another provider takes that model's defaults": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai, "local": local},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeSmall: {Provider: "local", Model: "l1"}},
			want: resolvedModels{
				Large: defaultLarge,
				Small: SelectedModel{Provider: "local", Model: "l1", MaxTokens: 300},
			},
		},
		"large model missing from its provider falls back to the default": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeLarge: everyField("ghost", "missing")},
			want:      resolvedModels{Large: defaultLarge, Small: defaultSmall, LargeFallback: true},
		},
		"small model missing from its provider falls back to the default": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"openai": openai},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeSmall: everyField("openai", "missing")},
			want:      resolvedModels{Large: defaultLarge, Small: defaultSmall, SmallFallback: true},
		},
		"unknown provider's large model doubles as the small model": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"local": local},
			models:    map[SelectedModelType]SelectedModel{SelectedModelTypeLarge: {Provider: "local", Model: "l2"}},
			want: resolvedModels{
				Large: SelectedModel{Provider: "local", Model: "l2", MaxTokens: 400},
				Small: SelectedModel{Provider: "local", Model: "l2", MaxTokens: 400},
			},
		},
		"unknown provider keeps a configured small model": {
			catalog:   catalog,
			providers: map[string]ProviderConfig{"local": local},
			models: map[SelectedModelType]SelectedModel{
				SelectedModelTypeLarge: {Provider: "local", Model: "l2"},
				SelectedModelTypeSmall: {Provider: "local", Model: "l1"},
			},
			want: resolvedModels{
				Large: SelectedModel{Provider: "local", Model: "l2", MaxTokens: 400},
				Small: SelectedModel{Provider: "local", Model: "l1", MaxTokens: 300},
			},
		},
		"no providers is an error": {
			wantErr: "failed to select default models: no providers configured",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				Providers: csync.NewMapFrom(tc.providers),
				Models:    tc.models,
			}
			if cfg.Models == nil {
				cfg.Models = map[SelectedModelType]SelectedModel{}
			}
			if tc.providers == nil {
				cfg.Providers = csync.NewMap[string, ProviderConfig]()
			}

			got, err := resolveSelectedModels(cfg, tc.catalog)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
