package cmd

import (
	"bytes"
	"log/slog"
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

// runWorkflowCheck runs `anvil workflow check` with args and returns its
// output.
func runWorkflowCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newWorkflowCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append([]string{"check"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

func TestWorkflowCheck(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		script    string
		noRef     bool
		wantErr   bool
		wantLines []string
		// notLines must not appear, such as a requirement kind with no names.
		notLines []string
	}{
		"everything resolves": {
			script: "requires({skills: [\"s1\"], agents: [\"a1\"], references: [\"ref.md\"]})\nfunction main(input) {}\n",
			wantLines: []string{
				"requires skills: s1",
				"requires agents: a1",
				"requires references: ref.md",
				"skills and agents not checked (--no-catalog)",
			},
		},
		"missing reference": {
			script:    "requires({references: [\"ref.md\"]})\nfunction main(input) {}\n",
			noRef:     true,
			wantErr:   true,
			wantLines: []string{`missing: reference "ref.md" not found`},
			notLines:  []string{"requires skills", "requires agents"},
		},
		"parse error": {
			script:    "function main( {",
			wantErr:   true,
			wantLines: []string{"error: parse workflow"},
		},
		"non-literal requires fails": {
			script:    "requires(deps)\nfunction main(input) {}\n",
			wantErr:   true,
			wantLines: []string{"error: requires() must be a literal"},
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

			out, err := runWorkflowCheck(t, "--no-catalog", path)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			for _, line := range tc.wantLines {
				require.Contains(t, out, line)
			}
			for _, line := range tc.notLines {
				require.NotContains(t, out, line)
			}
		})
	}
}

func TestWorkflowCheck_CommandPaths(t *testing.T) {
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

			out, err := runWorkflowCheck(t, "--no-catalog", filepath.Join(dir, tc.target))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, out, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Contains(t, out, filepath.Join(dir, "workflow.js"))
		})
	}
}

func TestWorkflowCheck_NeverRunsTopLevelCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.js")
	writeTestFile(t, path, "while (true) {}\nfunction main(input) {}\n")

	done := make(chan error, 1)
	go func() {
		_, err := runWorkflowCheck(t, "--no-catalog", path)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("check ran the script's top-level code")
	}
}

func TestWorkflowCheck_ChecksDiscoveredSkillsAndAgents(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	t.Chdir(project)

	pluginDir := filepath.Join(t.TempDir(), "myplugin")
	writeTestFile(t, filepath.Join(pluginDir, "skills", "s1", "SKILL.md"),
		"---\nname: s1\ndescription: From a plugin.\n---\nBody.\n")
	writeTestFile(t, filepath.Join(pluginDir, "agents", "a1.md"), "---\ndescription: Reviews.\n---\nReview.\n")
	writeTestFile(t, filepath.Join(project, "anvil.json"), `{
  "models": {"large": {"provider": "test", "model": "test"}, "small": {"provider": "test", "model": "test"}},
  "providers": {"test": {"type": "openai", "api_key": "test", "base_url": "http://127.0.0.1:1", "models": [{"id": "test", "name": "Test", "context_window": 10000}]}},
  "plugins": [{"path": "`+filepath.ToSlash(pluginDir)+`"}],
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true}
}`)

	tests := map[string]struct {
		requires  string
		wantErr   bool
		wantLines []string
	}{
		"discovered": {requires: `{skills: ["s1"], agents: ["a1"]}`},
		"missing": {
			requires:  `{skills: ["s2"], agents: ["a2"]}`,
			wantErr:   true,
			wantLines: []string{`missing: skill "s2" not found`, `missing: agent "a2" not found`},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.js")
			writeTestFile(t, path, "requires("+tc.requires+")\nfunction main(input) {}\n")

			out, err := runWorkflowCheck(t, path)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err, out)
			}
			require.NotContains(t, out, "--no-catalog")
			for _, line := range tc.wantLines {
				require.Contains(t, out, line)
			}
		})
	}
}

// RefuseWorkflowCommand is tested directly: going through `anvil run`
// would boot the whole app and need a configured provider.
func TestRunRefusesWorkflowCommands(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	dataDir := filepath.Join(project, ".anvil")
	writeTestFile(t, filepath.Join(dataDir, "commands", "demo", "COMMAND.md"), "---\nworkflow: workflow.js\n---\n")
	writeTestFile(t, filepath.Join(dataDir, "commands", "demo", "workflow.js"), "function main() {}\n")
	writeTestFile(t, filepath.Join(dataDir, "commands", "plain", "COMMAND.md"), "Say hi.\n")
	writeTestFile(t, filepath.Join(dataDir, "commands", "escape", "COMMAND.md"), "---\nworkflow: ../workflow.js\n---\nBODY\n")
	store, err := config.Load(project, dataDir, false)
	require.NoError(t, err)

	require.ErrorContains(t, refuseWorkflowCommand(store.Config(), "/demo some args"), "workflow command")
	require.ErrorContains(t, refuseWorkflowCommand(store.Config(), "/escape"), "workflow command",
		"a workflow that can't load is still never sent as a prompt")
	require.NoError(t, refuseWorkflowCommand(store.Config(), "/plain"))
	require.NoError(t, refuseWorkflowCommand(store.Config(), "demo please"))
}

// `anvil run` refuses a workflow command before sending anything.
func TestRunCommandRefusesAWorkflowCommand(t *testing.T) {
	global, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANVIL_GLOBAL_CONFIG", global)
	t.Setenv("ANVIL_GLOBAL_DATA", global)
	t.Setenv("ANVIL_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	// The run changes into --cwd; this restores the directory afterwards.
	t.Chdir(project)
	prevLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevLogger) })
	prevStdin := os.Stdin
	stdin, err := os.Open(os.DevNull)
	require.NoError(t, err)
	os.Stdin = stdin
	t.Cleanup(func() {
		os.Stdin = prevStdin
		_ = stdin.Close()
	})
	dataDir := filepath.Join(project, ".anvil")
	// A provider nothing listens on, so a run that got past the refusal
	// would fail without leaving the machine.
	writeTestFile(t, filepath.Join(project, "anvil.json"), `{
  "models": {"large": {"provider": "test", "model": "test"}, "small": {"provider": "test", "model": "test"}},
  "providers": {"test": {"type": "openai", "api_key": "test", "base_url": "http://127.0.0.1:1", "models": [{"id": "test", "name": "Test", "context_window": 10000}]}},
  "options": {"disable_default_providers": true, "disable_provider_auto_update": true}
}`)
	writeTestFile(t, filepath.Join(dataDir, "commands", "demo", "COMMAND.md"), "---\nworkflow: workflow.js\n---\n")
	writeTestFile(t, filepath.Join(dataDir, "commands", "demo", "workflow.js"), "function main() {}\n")

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	rootCmd.SetArgs([]string{"run", "--quiet", "--cwd", project, "--data-dir", dataDir, "/demo now"})
	err = rootCmd.ExecuteContext(t.Context())
	require.ErrorContains(t, err, "/demo is a workflow command")
	// The refusal comes before Anvil starts: no database or log file is
	// opened, so nothing is left holding files in the data dir.
	require.NoFileExists(t, filepath.Join(dataDir, "logs", "anvil.log"))
}
