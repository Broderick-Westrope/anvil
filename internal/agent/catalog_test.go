package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/config"
)

func writeCatalogFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestDiscoverCatalog(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	writeCatalogFile(t, filepath.Join(first, "skills", "plugin-skill", "SKILL.md"),
		"---\nname: plugin-skill\ndescription: From a plugin.\n---\nBody.\n")
	writeCatalogFile(t, filepath.Join(first, "agents", "reviewer.md"), "---\ndescription: Reviews.\n---\nReview.\n")
	writeCatalogFile(t, filepath.Join(first, "agents", "retired.md"), "---\ndescription: Disabled.\n---\nGone.\n")
	writeCatalogFile(t, filepath.Join(second, "agents", "planner.md"), "---\ndescription: Plans.\n---\nPlan.\n")
	userSkills := filepath.Join(t.TempDir(), "skills")
	writeCatalogFile(t, filepath.Join(userSkills, "user-skill", "SKILL.md"),
		"---\nname: user-skill\ndescription: From skills_paths.\n---\nBody.\n")
	writeCatalogFile(t, filepath.Join(userSkills, "off-skill", "SKILL.md"),
		"---\nname: off-skill\ndescription: Disabled.\n---\nBody.\n")

	cfgJSON := `{
  "models": {"large": {"provider": "test", "model": "test"}, "small": {"provider": "test", "model": "test"}},
  "providers": {"test": {"type": "openai", "api_key": "test", "base_url": "http://127.0.0.1:1", "models": [{"id": "test", "name": "Test", "context_window": 10000}]}},
  "plugins": [{"path": "` + filepath.ToSlash(first) + `"}, {"path": "` + filepath.ToSlash(second) + `"}],
  "agents": {"custom": {"name": "Custom"}},
  "disabled_agents": ["retired"],
  "options": {
    "disable_default_providers": true,
    "disable_provider_auto_update": true,
    "skills_paths": ["` + filepath.ToSlash(userSkills) + `"],
    "disabled_skills": ["off-skill"]
  }
}`
	writeCatalogFile(t, filepath.Join(project, "anvil.json"), cfgJSON)
	store, err := config.Load(project, filepath.Join(project, ".anvil"), false)
	require.NoError(t, err)

	cat, err := agent.DiscoverCatalog(store)
	require.NoError(t, err)

	tests := map[string]struct {
		has  func(string) bool
		name string
		want bool
	}{
		"plugin skill":             {has: cat.HasSkill, name: "plugin-skill", want: true},
		"skills_paths skill":       {has: cat.HasSkill, name: "user-skill", want: true},
		"builtin skill":            {has: cat.HasSkill, name: "jq", want: true},
		"disabled skill":           {has: cat.HasSkill, name: "off-skill"},
		"unknown skill":            {has: cat.HasSkill, name: "nope"},
		"plugin agent":             {has: cat.HasAgent, name: "reviewer", want: true},
		"second plugin agent":      {has: cat.HasAgent, name: "planner", want: true},
		"anvil.json agent":         {has: cat.HasAgent, name: "custom", want: true},
		"disabled agent":           {has: cat.HasAgent, name: "retired"},
		"orchestrator isn't agent": {has: cat.HasAgent, name: config.AgentOrchestrator},
		"unknown agent":            {has: cat.HasAgent, name: "nope"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.has(tc.name))
		})
	}
}
