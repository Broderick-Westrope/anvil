package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/azure"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openaicompat"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// TestGetProviderOptionsMatrix pins the full provider options produced for
// every provider type, the provider IDs with quirks, and the model
// settings that steer reasoning. Regenerate with -update only when a
// change to the request is intended.
func TestGetProviderOptionsMatrix(t *testing.T) {
	t.Parallel()

	quirkIDs := []catwalk.InferenceProvider{
		"custom",
		catwalk.InferenceProviderIoNet,
		catwalk.InferenceProviderOpenCodeGo,
		catwalk.InferenceProviderOpenCodeZen,
		catwalk.InferenceProviderZAI,
		catwalk.InferenceProviderDeepSeek,
		catwalk.InferenceProviderFireworks,
		catwalk.InferenceProviderBaseten,
		catwalk.InferenceProviderAlibabaSingapore,
		catwalk.InferenceProviderAlibabaUS,
	}
	// Types without ID quirks run against a plain ID and an ID with quirks
	// in both the Anthropic and openai-compat types, so a quirk leaking
	// into the wrong type shows up.
	crossIDs := []catwalk.InferenceProvider{
		"custom",
		catwalk.InferenceProviderAlibabaUS,
	}
	providers := []struct {
		typ catwalk.Type
		ids []catwalk.InferenceProvider
	}{
		{openai.Name, crossIDs},
		{azure.Name, crossIDs},
		{anthropic.Name, quirkIDs},
		{bedrock.Name, quirkIDs},
		{openrouter.Name, crossIDs},
		{vercel.Name, crossIDs},
		{google.Name, crossIDs},
		{openaicompat.Name, quirkIDs},
		{"ollama", crossIDs},
		{"unknown-type", crossIDs},
	}

	levels := []string{"none", "low", "medium", "high"}
	variants := []struct {
		name         string
		catwalk      catwalk.Model
		selected     config.SelectedModel
		providerOpts map[string]any
	}{
		{name: "no-reasoning", catwalk: catwalk.Model{ID: "model-x"}},
		{name: "no-reasoning-think", catwalk: catwalk.Model{ID: "model-x"}, selected: config.SelectedModel{Think: true}},
		{name: "reasoning-no-levels", catwalk: catwalk.Model{ID: "model-x", CanReason: true}},
		{name: "reasoning-no-levels-think", catwalk: catwalk.Model{ID: "model-x", CanReason: true}, selected: config.SelectedModel{Think: true}},
		{name: "effort-high", catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "high"}},
		{name: "effort-none", catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "none"}},
		{name: "effort-default", catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels, DefaultReasoningEffort: "medium"}},
		{name: "effort-unlisted", catwalk: catwalk.Model{ID: "model-x", CanReason: false, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "high", Think: true}},
		{name: "effort-and-think", catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "low", Think: true}},
		{name: "gpt-5", catwalk: catwalk.Model{ID: "gpt-5", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "medium"}},
		{name: "gpt-3.5", catwalk: catwalk.Model{ID: "gpt-3.5-turbo"}},
		{name: "gemini-2", catwalk: catwalk.Model{ID: "gemini-2.5-pro", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "high"}},
		{name: "gemini-3", catwalk: catwalk.Model{ID: "gemini-3-pro", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "high"}},
		{name: "minimax-effort", catwalk: catwalk.Model{ID: "MiniMax-M2", CanReason: true, ReasoningLevels: levels}, selected: config.SelectedModel{ReasoningEffort: "high"}},
		{name: "minimax-think", catwalk: catwalk.Model{ID: "minimax-m2", CanReason: true}, selected: config.SelectedModel{Think: true}},
		{name: "minimax-off", catwalk: catwalk.Model{ID: "minimax-m2", CanReason: true}},
		{name: "minimax-no-reasoning", catwalk: catwalk.Model{ID: "minimax-m2"}, selected: config.SelectedModel{Think: true}},
		{
			name:    "user-effort",
			catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels},
			selected: config.SelectedModel{ReasoningEffort: "high", ProviderOptions: map[string]any{
				"reasoning_effort": "low",
				"effort":           "low",
				"reasoning":        map[string]any{"effort": "low"},
				"thinking_config":  map[string]any{"thinking_budget": 1},
			}},
		},
		{
			name:    "user-thinking",
			catwalk: catwalk.Model{ID: "model-x", CanReason: true},
			selected: config.SelectedModel{Think: true, ProviderOptions: map[string]any{
				"thinking": map[string]any{"budget_tokens": 5000},
			}},
		},
		{
			name: "layered",
			catwalk: catwalk.Model{ID: "model-x", Options: catwalk.ModelOptions{ProviderOptions: map[string]any{
				"user": "catwalk", "parallel_tool_calls": true, "extra_body": map[string]any{"from": "catwalk"},
			}}},
			selected:     config.SelectedModel{ProviderOptions: map[string]any{"user": "model"}},
			providerOpts: map[string]any{"user": "provider", "parallel_tool_calls": false},
		},
		{
			name: "provider-layer",
			catwalk: catwalk.Model{ID: "model-x", Options: catwalk.ModelOptions{ProviderOptions: map[string]any{
				"user": "catwalk",
			}}},
			providerOpts: map[string]any{"user": "provider"},
		},
		{
			name:    "invalid",
			catwalk: catwalk.Model{ID: "model-x", CanReason: true, ReasoningLevels: levels},
			selected: config.SelectedModel{ProviderOptions: map[string]any{
				"reasoning_effort": 5, "effort": 5, "thinking": "x", "reasoning": 1, "thinking_config": 1, "user": 1,
			}},
		},
	}

	var out strings.Builder
	for _, p := range providers {
		for _, id := range p.ids {
			for _, v := range variants {
				model := Model{CatwalkCfg: v.catwalk, ModelCfg: v.selected}
				providerCfg := config.ProviderConfig{ID: string(id), Type: p.typ, ProviderOptions: v.providerOpts}
				data, err := json.Marshal(getProviderOptions(model, providerCfg))
				require.NoError(t, err)
				fmt.Fprintf(&out, "%s/%s/%s: %s\n", p.typ, id, v.name, withoutNulls(t, data))
			}
		}
	}
	golden.RequireEqual(t, []byte(out.String()))
}

// withoutNulls drops null fields so the golden file shows only the options
// that were set.
func withoutNulls(t *testing.T, data []byte) []byte {
	t.Helper()
	var v any
	require.NoError(t, json.Unmarshal(data, &v))
	out, err := json.Marshal(dropNulls(v))
	require.NoError(t, err)
	return out
}

func dropNulls(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, val := range v {
			if val == nil {
				delete(v, k)
				continue
			}
			v[k] = dropNulls(val)
		}
	case []any:
		for i := range v {
			v[i] = dropNulls(v[i])
		}
	}
	return v
}
