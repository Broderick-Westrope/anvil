package app

import (
	"context"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/stretchr/testify/require"
)

// updateModelsRecorder is an agent.Coordinator that only counts rebuilds.
type updateModelsRecorder struct {
	agent.Coordinator
	updates int
}

func (r *updateModelsRecorder) UpdateModels(context.Context) error {
	r.updates++
	return nil
}

func newReasoningEffortApp(t *testing.T, selected config.SelectedModel) (*App, *updateModelsRecorder) {
	t.Helper()
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("openai", config.ProviderConfig{ID: "openai", Models: []catwalk.Model{
		{ID: "gpt-5", CanReason: true, ReasoningLevels: []string{"low", "medium", "high"}},
		{ID: "gpt-4o"},
	}})
	cfg := &config.Config{
		Providers: providers,
		Options:   &config.Options{},
		Models:    map[config.SelectedModelType]config.SelectedModel{config.SelectedModelTypeLarge: selected},
	}
	rec := &updateModelsRecorder{}
	return &App{config: config.NewTestStore(cfg), AgentCoordinator: rec}, rec
}

func TestOverrideReasoningEffort(t *testing.T) {
	t.Parallel()

	t.Run("supported effort overrides the large model for this run", func(t *testing.T) {
		t.Parallel()
		app, rec := newReasoningEffortApp(t, config.SelectedModel{
			Provider: "openai", Model: "gpt-5", ReasoningEffort: "low", MaxTokens: 4096,
		})

		require.NoError(t, app.overrideReasoningEffort(t.Context(), "high"))

		got := app.config.Config().Models[config.SelectedModelTypeLarge]
		require.Equal(t, "high", got.ReasoningEffort)
		require.Equal(t, "gpt-5", got.Model)
		require.Equal(t, int64(4096), got.MaxTokens, "other model settings are kept")
		require.Equal(t, 1, rec.updates, "the agent is rebuilt with the new effort")
	})

	t.Run("unsupported effort is rejected with the accepted values", func(t *testing.T) {
		t.Parallel()
		app, rec := newReasoningEffortApp(t, config.SelectedModel{Provider: "openai", Model: "gpt-5", ReasoningEffort: "low"})

		err := app.overrideReasoningEffort(t.Context(), "max")

		require.ErrorContains(t, err, `does not support reasoning effort "max"`)
		require.ErrorContains(t, err, "low, medium, high")
		require.Equal(t, "low", app.config.Config().Models[config.SelectedModelTypeLarge].ReasoningEffort)
		require.Zero(t, rec.updates)
	})

	t.Run("model without reasoning is rejected", func(t *testing.T) {
		t.Parallel()
		app, rec := newReasoningEffortApp(t, config.SelectedModel{Provider: "openai", Model: "gpt-4o"})

		require.ErrorContains(t, app.overrideReasoningEffort(t.Context(), "high"), "does not support reasoning effort")
		require.Zero(t, rec.updates)
	})

	t.Run("no large model selected", func(t *testing.T) {
		t.Parallel()
		app, _ := newReasoningEffortApp(t, config.SelectedModel{})
		delete(app.config.Config().Models, config.SelectedModelTypeLarge)

		require.ErrorContains(t, app.overrideReasoningEffort(t.Context(), "high"), "no large model selected")
	})
}
