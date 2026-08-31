package config

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/stretchr/testify/require"
)

// resolveAgentModelConfig builds a Config with two providers: "anthropic"
// (large/small tiers, one model with reasoning levels and one without) and
// "openai" (used to assert cross-provider option handling).
func resolveAgentModelConfig(large SelectedModel) *Config {
	return &Config{
		Models: map[SelectedModelType]SelectedModel{
			SelectedModelTypeLarge: large,
		},
		Providers: csync.NewMapFrom(map[string]ProviderConfig{
			"anthropic": {
				ID: "anthropic",
				Models: []catwalk.Model{
					{
						ID:                     "big",
						DefaultMaxTokens:       128000,
						CanReason:              true,
						ReasoningLevels:        []string{"low", "medium", "high"},
						DefaultReasoningEffort: "high",
					},
					{
						ID:               "tiny",
						DefaultMaxTokens: 64000,
						CanReason:        true,
					},
				},
			},
			"openai": {
				ID: "openai",
				Models: []catwalk.Model{
					{ID: "gpt", DefaultMaxTokens: 32000},
				},
			},
		}),
	}
}

func ptr[T any](v T) *T { return &v }

func TestResolveAgentModel_NoOverrideReturnsGlobalLarge(t *testing.T) {
	t.Parallel()

	temp := 0.3
	large := SelectedModel{
		Provider:        "anthropic",
		Model:           "big",
		MaxTokens:       99,
		ReasoningEffort: "low",
		Think:           true,
		Temperature:     &temp,
	}
	cfg := resolveAgentModelConfig(large)

	got, err := ResolveAgentModel(Agent{ID: "fixer"}, cfg)
	require.NoError(t, err)
	require.Equal(t, large, got)
}

func TestResolveAgentModel_MissingLargeModelErrors(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{})
	cfg.Models = map[SelectedModelType]SelectedModel{}

	_, err := ResolveAgentModel(Agent{ID: "fixer", Model: "anthropic/big"}, cfg)
	require.ErrorContains(t, err, "no large model configured")
}

// Regression: the previous implementation rebuilt SelectedModel from scratch,
// silently discarding globally configured sampling parameters whenever an
// agent declared its own model.
func TestResolveAgentModel_InheritsSamplingFromGlobalLarge(t *testing.T) {
	t.Parallel()

	temp, topP := 0.2, 0.85
	topK := int64(40)
	cfg := resolveAgentModelConfig(SelectedModel{
		Provider:    "anthropic",
		Model:       "big",
		Temperature: &temp,
		TopP:        &topP,
		TopK:        &topK,
	})

	got, err := ResolveAgentModel(Agent{ID: "explorer", Model: "anthropic/tiny"}, cfg)
	require.NoError(t, err)

	require.Equal(t, "anthropic", got.Provider)
	require.Equal(t, "tiny", got.Model)
	require.Equal(t, int64(64000), got.MaxTokens, "max tokens come from the target model")
	require.Equal(t, &temp, got.Temperature)
	require.Equal(t, &topP, got.TopP)
	require.Equal(t, &topK, got.TopK)
}

func TestResolveAgentModel_TargetModelDefaultsWinOverGlobal(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{
		Provider:        "anthropic",
		Model:           "tiny",
		MaxTokens:       64000,
		ReasoningEffort: "low",
	})

	got, err := ResolveAgentModel(Agent{ID: "planner", Model: "anthropic/big"}, cfg)
	require.NoError(t, err)
	require.Equal(t, int64(128000), got.MaxTokens)
	require.Equal(t, "high", got.ReasoningEffort, "target model default, not the global effort")
}

func TestResolveAgentModel_AgentReasoningEffortWins(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{Provider: "anthropic", Model: "big"})

	got, err := ResolveAgentModel(Agent{
		ID:              "explorer",
		Model:           "anthropic/big",
		ReasoningEffort: "low",
	}, cfg)
	require.NoError(t, err)
	require.Equal(t, "low", got.ReasoningEffort)
}

func TestResolveAgentModel_UnsupportedReasoningEffortFallsBackToModelDefault(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{Provider: "anthropic", Model: "big"})

	got, err := ResolveAgentModel(Agent{
		ID:              "explorer",
		Model:           "anthropic/big",
		ReasoningEffort: "nonsense",
	}, cfg)
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
}

// A model with no reasoning levels cannot validate an effort value, so the
// agent's choice is passed through for the call layer to filter.
func TestResolveAgentModel_EffortPassedThroughForModelWithoutLevels(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{Provider: "anthropic", Model: "big"})

	got, err := ResolveAgentModel(Agent{
		ID:              "explorer",
		Model:           "anthropic/tiny",
		ReasoningEffort: "low",
	}, cfg)
	require.NoError(t, err)
	require.Equal(t, "low", got.ReasoningEffort)
}

func TestResolveAgentModel_AgentThinkOverridesGlobal(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{
		Provider: "anthropic",
		Model:    "big",
		Think:    true,
	})

	inherited, err := ResolveAgentModel(Agent{ID: "fixer", Model: "anthropic/tiny"}, cfg)
	require.NoError(t, err)
	require.True(t, inherited.Think, "nil Think inherits the global setting")

	disabled, err := ResolveAgentModel(Agent{
		ID:    "explorer",
		Model: "anthropic/tiny",
		Think: ptr(false),
	}, cfg)
	require.NoError(t, err)
	require.False(t, disabled.Think)

	enabled, err := ResolveAgentModel(Agent{
		ID:    "oracle",
		Model: "anthropic/tiny",
		Think: ptr(true),
	}, cfg)
	require.NoError(t, err)
	require.True(t, enabled.Think)
}

func TestResolveAgentModel_DropsProviderOptionsAcrossProviders(t *testing.T) {
	t.Parallel()

	opts := map[string]any{"thinking": map[string]any{"budget_tokens": 2000}}
	cfg := resolveAgentModelConfig(SelectedModel{
		Provider:        "anthropic",
		Model:           "big",
		ProviderOptions: opts,
	})

	same, err := ResolveAgentModel(Agent{ID: "fixer", Model: "anthropic/tiny"}, cfg)
	require.NoError(t, err)
	require.Equal(t, opts, same.ProviderOptions, "same provider keeps its options")

	cross, err := ResolveAgentModel(Agent{ID: "fixer", Model: "openai/gpt"}, cfg)
	require.NoError(t, err)
	require.Nil(t, cross.ProviderOptions, "options do not transfer across providers")
}

func TestResolveAgentModel_VariantOverride(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{Provider: "anthropic", Model: "big"})

	got, err := ResolveAgentModel(Agent{ID: "fixer", Model: "anthropic/tiny", Variant: "v2"}, cfg)
	require.NoError(t, err)
	require.Equal(t, "v2", got.Variant)
}

func TestResolveAgentModel_Errors(t *testing.T) {
	t.Parallel()

	cfg := resolveAgentModelConfig(SelectedModel{Provider: "anthropic", Model: "big"})

	tests := []struct {
		name    string
		model   string
		wantErr string
	}{
		{"no slash", "claude-big", "must be in provider/model format"},
		{"unknown provider", "nope/big", `provider "nope" not found`},
		{"unknown model", "anthropic/nope", `model "nope" not found`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveAgentModel(Agent{ID: "fixer", Model: tt.model}, cfg)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestApplyOverrides_ReasoningEffortAndThink(t *testing.T) {
	t.Parallel()

	agents := map[string]Agent{
		"explorer": {ID: "explorer", Model: "anthropic/tiny", ReasoningEffort: "low", Think: ptr(false)},
		"fixer":    {ID: "fixer", Model: "anthropic/big", ReasoningEffort: "medium"},
	}
	user := map[string]Agent{
		"explorer": {ReasoningEffort: "high"},
		"fixer":    {Think: ptr(true)},
	}

	got := applyOverrides(agents, user, nil)

	require.Equal(t, "high", got["explorer"].ReasoningEffort)
	require.NotNil(t, got["explorer"].Think)
	require.False(t, *got["explorer"].Think, "unset user Think leaves the default alone")

	require.Equal(t, "medium", got["fixer"].ReasoningEffort, "unset user effort leaves the default alone")
	require.NotNil(t, got["fixer"].Think)
	require.True(t, *got["fixer"].Think)
}
