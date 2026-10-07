package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupConfigs_UserConfigOutranksRuntimeData(t *testing.T) {
	e := newBouncerEnv(t)

	got := lookupConfigs(e.projectDir)

	userIdx := indexOf(got, GlobalConfig())
	dataIdx := indexOf(got, GlobalConfigData())
	require.GreaterOrEqual(t, userIdx, 0)
	require.GreaterOrEqual(t, dataIdx, 0)
	require.Less(t, dataIdx, userIdx, "runtime data must merge before the user config so the user config wins")
}

func indexOf(paths []string, want string) int {
	for i, p := range paths {
		if p == want {
			return i
		}
	}
	return -1
}

func writePrecedenceProviders(t *testing.T, e bouncerEnv, models map[string]any) {
	t.Helper()
	cfg := map[string]any{
		"options": map[string]any{"disable_default_providers": true},
		"providers": map[string]any{
			"custom": map[string]any{
				"type":     "openai-compat",
				"base_url": "https://llm.example.com/v1",
				"api_key":  "provider-key",
				"models": []map[string]any{
					{"id": "from-user", "name": "From user"},
					{"id": "from-data", "name": "From data"},
				},
			},
		},
	}
	for k, v := range models {
		cfg[k] = v
	}
	writeConfig(t, e.globalDir, cfg)
}

func TestLoad_UserConfigModelBeatsRuntimeData(t *testing.T) {
	e := newBouncerEnv(t)
	writePrecedenceProviders(t, e, map[string]any{
		"models": map[string]any{
			"large": map[string]any{"provider": "custom", "model": "from-user"},
		},
	})
	writeConfig(t, e.dataDir, map[string]any{
		"models": map[string]any{
			"large": map[string]any{"provider": "custom", "model": "from-data"},
		},
	})

	store := e.mustLoad(t)
	require.Equal(t, "from-user", store.Config().Models[SelectedModelTypeLarge].Model)

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	require.Equal(t, "from-user", store.Config().Models[SelectedModelTypeLarge].Model)
}

func TestLoad_RuntimeDataFillsWhatUserConfigLeavesUnset(t *testing.T) {
	e := newBouncerEnv(t)
	writePrecedenceProviders(t, e, map[string]any{
		"models": map[string]any{
			"large": map[string]any{"provider": "custom", "model": "from-user"},
		},
	})
	writeConfig(t, e.dataDir, map[string]any{
		"models": map[string]any{
			"small": map[string]any{"provider": "custom", "model": "from-data"},
		},
	})

	store := e.mustLoad(t)
	require.Equal(t, "from-user", store.Config().Models[SelectedModelTypeLarge].Model)
	require.Equal(t, "from-data", store.Config().Models[SelectedModelTypeSmall].Model)
}

func TestBouncer_UserConfigBlockBeatsRuntimeData(t *testing.T) {
	e := newBouncerEnv(t)
	e.writeGlobal(t, map[string]any{"bouncer": bouncerBlock("shadow")})
	writeConfig(t, e.dataDir, map[string]any{"bouncer": bouncerBlock("enforce")})

	store := e.mustLoad(t)
	requireBouncerMode(t, store, "shadow")

	require.NoError(t, store.ReloadFromDisk(context.Background()))
	requireBouncerMode(t, store, "shadow")
}
