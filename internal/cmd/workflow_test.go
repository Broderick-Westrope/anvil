package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

const checkedScript = `requires({skills: ["s1"], agents: ["a1"], references: ["ref.md"]})
function main(input) {}
`

func TestCheckWorkflow(t *testing.T) {
	t.Parallel()

	fullCatalog := nameCatalog{skills: []string{"s1"}, agents: []string{"a1"}}
	tests := map[string]struct {
		script    string
		catalog   *nameCatalog
		noRef     bool
		wantErr   bool
		wantLines []string
	}{
		"everything resolves": {
			script:  checkedScript,
			catalog: &fullCatalog,
			wantLines: []string{
				"requires skills: s1",
				"requires agents: a1",
				"requires references: ref.md",
			},
		},
		"missing skill and agent": {
			script:    checkedScript,
			catalog:   &nameCatalog{},
			wantErr:   true,
			wantLines: []string{`missing: skill "s1" not found`, `missing: agent "a1" not found`},
		},
		"missing reference": {
			script:    checkedScript,
			catalog:   &fullCatalog,
			noRef:     true,
			wantErr:   true,
			wantLines: []string{`missing: reference "ref.md" not found`},
		},
		"parse error": {
			script:    "function main( {",
			catalog:   &fullCatalog,
			wantErr:   true,
			wantLines: []string{"error: parse workflow"},
		},
		"non-literal requires is a warning": {
			script:    "requires(deps)\nfunction main(input) {}\n",
			catalog:   &fullCatalog,
			wantLines: []string{"warning: requires() must be a literal"},
		},
		"no catalog skips skills and agents": {
			script:    checkedScript,
			wantLines: []string{"skills and agents not checked (--no-catalog)"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "workflow.js")
			writeTestFile(t, path, tc.script)
			if !tc.noRef {
				writeTestFile(t, filepath.Join(dir, "ref.md"), "ref")
			}

			var out bytes.Buffer
			var err error
			if tc.catalog == nil {
				err = checkWorkflow(&out, path, nil)
			} else {
				err = checkWorkflow(&out, path, *tc.catalog)
			}
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			for _, line := range tc.wantLines {
				require.Contains(t, out.String(), line)
			}
		})
	}
}

func TestCheckWorkflow_CommandPaths(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		frontmatter string
		target      string
		wantErr     string
	}{
		"command directory": {
			frontmatter: "workflow: workflow.js",
			target:      ".",
		},
		"COMMAND.md file": {
			frontmatter: "workflow: workflow.js",
			target:      "COMMAND.md",
		},
		"no workflow field": {
			frontmatter: "description: plain",
			target:      ".",
			wantErr:     "no workflow: field",
		},
		"workflow outside the command directory": {
			frontmatter: "workflow: ../workflow.js",
			target:      ".",
			wantErr:     "outside the command directory",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "flow")
			writeTestFile(t, filepath.Join(dir, "COMMAND.md"), "---\n"+tc.frontmatter+"\n---\n")
			writeTestFile(t, filepath.Join(dir, "workflow.js"), "function main(input) {}\n")

			var out bytes.Buffer
			err := checkWorkflow(&out, filepath.Join(dir, tc.target), nameCatalog{})
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, out.String(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Contains(t, out.String(), filepath.Join(dir, "workflow.js"))
		})
	}
}

func TestCheckWorkflow_NeverRunsTopLevelCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.js")
	writeTestFile(t, path, "while (true) {}\nfunction main(input) {}\n")

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- checkWorkflow(&out, path, nameCatalog{}) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("check ran the script's top-level code")
	}
}

func TestWorkflowCatalog(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	pluginDir := filepath.Join(t.TempDir(), "myplugin")
	writeTestFile(t, filepath.Join(pluginDir, "skills", "plugin-skill", "SKILL.md"),
		"---\nname: plugin-skill\ndescription: From a plugin.\n---\nBody.\n")
	writeTestFile(t, filepath.Join(pluginDir, "agents", "reviewer.md"), "---\ndescription: Reviews.\n---\nReview.\n")
	writeTestFile(t, filepath.Join(pluginDir, "agents", "retired.md"), "---\ndescription: Disabled.\n---\nGone.\n")
	userSkills := filepath.Join(t.TempDir(), "skills")
	writeTestFile(t, filepath.Join(userSkills, "user-skill", "SKILL.md"),
		"---\nname: user-skill\ndescription: From skills_paths.\n---\nBody.\n")
	writeTestFile(t, filepath.Join(userSkills, "off-skill", "SKILL.md"),
		"---\nname: off-skill\ndescription: Disabled.\n---\nBody.\n")

	cfgJSON := `{
  "models": {"large": {"provider": "test", "model": "test"}, "small": {"provider": "test", "model": "test"}},
  "providers": {"test": {"type": "openai", "api_key": "test", "base_url": "http://127.0.0.1:1", "models": [{"id": "test", "name": "Test", "context_window": 10000}]}},
  "plugins": [{"path": "` + filepath.ToSlash(pluginDir) + `"}],
  "agents": {"custom": {"name": "Custom"}},
  "disabled_agents": ["retired"],
  "options": {
    "disable_default_providers": true,
    "disable_provider_auto_update": true,
    "skills_paths": ["` + filepath.ToSlash(userSkills) + `"],
    "disabled_skills": ["off-skill"]
  }
}`
	writeTestFile(t, filepath.Join(project, "anvil.json"), cfgJSON)
	store, err := config.Load(project, filepath.Join(project, ".anvil"), false)
	require.NoError(t, err)

	cat := workflowCatalog(store)

	require.True(t, cat.HasSkill("plugin-skill"))
	require.True(t, cat.HasSkill("user-skill"))
	require.True(t, cat.HasSkill("jq"), "builtin skills are in the catalog")
	require.False(t, cat.HasSkill("off-skill"), "disabled skills can't be preloaded")
	require.False(t, cat.HasSkill("nope"))
	require.True(t, cat.HasAgent("reviewer"))
	require.True(t, cat.HasAgent("custom"), "agents defined in anvil.json are in the catalog")
	require.False(t, cat.HasAgent("retired"), "disabled agents can't run")
	require.False(t, cat.HasAgent(config.AgentOrchestrator), "workflows can't run the orchestrator as an agent")
	require.False(t, cat.HasAgent("nope"))
}
