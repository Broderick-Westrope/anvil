package workflow_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/workflow"
)

func TestParseReadsRequiresFromAST(t *testing.T) {
	t.Parallel()

	src := `
requires({
  skills: ["go-style", "build-tdd"],
  agents: ["devils-advocate"],
  references: ["references/da.md"],
})

function main(input) { return input.raw }
`
	script, err := workflow.Parse("/tmp/build/workflow.js", []byte(src))
	require.NoError(t, err)
	require.Equal(t, workflow.Requirements{
		Skills:     []string{"go-style", "build-tdd"},
		Agents:     []string{"devils-advocate"},
		References: []string{"references/da.md"},
	}, script.Requires)
	require.Empty(t, script.Problems)
	require.Len(t, script.Hash, 64)
}

func TestParseWarnsOnNonLiteralRequires(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"variable argument": `const r = {skills: []}; requires(r); function main() {}`,
		"computed string":   `requires({skills: ["a" + "b"]}); function main() {}`,
		"unknown key":       `requires({tools: ["bash"]}); function main() {}`,
		"called twice":      `requires({}); requires({}); function main() {}`,
		"template string":   "requires({skills: [`x`]}); function main() {}",
		"computed key":      `requires({["skills"]: ["x"]}); function main() {}`,
		"shorthand key":     `const skills = []; requires({skills}); function main() {}`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			script, err := workflow.Parse("workflow.js", []byte(src))
			require.NoError(t, err)
			require.NotEmpty(t, script.Problems)
		})
	}
}

func TestParseRequiresMain(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"no function":    `requires({})`,
		"only a helper":  `function helper() {}`,
		"main is called": `main()`,
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := workflow.Parse("workflow.js", []byte(src))
			require.ErrorContains(t, err, "main")
		})
	}
}

func TestParseReadsEveryTopLevelStatement(t *testing.T) {
	t.Parallel()

	src := `let ready = false
ready = true
setup()
function main() {}
requires({skills: ["x"]})
function setup() {}`
	script, err := workflow.Parse("workflow.js", []byte(src))
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, script.Requires.Skills)
	require.Empty(t, script.Problems)
}

func TestParseReportsSyntaxErrors(t *testing.T) {
	t.Parallel()

	_, err := workflow.Parse("workflow.js", []byte(`function main( {`))
	require.Error(t, err)
}

func TestParseNeverExecutesTopLevelCode(t *testing.T) {
	t.Parallel()

	src := `while (true) {}
requires({skills: ["x"]})
function main() {}`
	script, err := workflow.Parse("workflow.js", []byte(src))
	require.NoError(t, err)
	require.Equal(t, []string{"x"}, script.Requires.Skills)
}

type catalog struct {
	skills, agents []string
}

func (c catalog) HasSkill(name string) bool { return slices.Contains(c.skills, name) }
func (c catalog) HasAgent(name string) bool { return slices.Contains(c.agents, name) }

func TestMissingListsUnresolvedRequirements(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "references"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "references", "da.md"), []byte("x"), 0o644))
	src := `requires({
  skills: ["go-style", "nope-skill"],
  agents: ["reviewer", "nope-agent"],
  references: ["../escape.md", "references/da.md", "references/nope.md"],
})
function main() {}`
	path := filepath.Join(dir, "workflow.js")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	script, err := workflow.Load(path)
	require.NoError(t, err)

	missing := script.Missing(catalog{skills: []string{"go-style"}, agents: []string{"reviewer"}})
	require.Equal(t, []string{
		`skill "nope-skill" not found`,
		`agent "nope-agent" not found`,
		`reference "../escape.md" is outside the workflow directory`,
		`reference "references/nope.md" not found`,
	}, missing)
}

func TestMissingReportsReferencesSymlinkedOutsideTheWorkflowDir(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "x.md"), []byte("secret"), 0o644))
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inside.md"), []byte("ok"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(dir, "inside.md"), filepath.Join(dir, "alias.md")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(dir, "escape.md")))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "linkdir")))
	src := `requires({references: ["alias.md", "escape.md", "linkdir/x.md"]})
function main() {}`
	path := filepath.Join(dir, "workflow.js")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))
	script, err := workflow.Load(path)
	require.NoError(t, err)

	require.Equal(t, []string{
		`reference "escape.md" not found`,
		`reference "linkdir/x.md" not found`,
	}, script.Missing(catalog{}))
}

func TestBundledListsOnlyRegularFilesOutsideHiddenAndDependencyDirs(t *testing.T) {
	t.Parallel()

	// The workflow directory's own name doesn't hide it.
	dir := filepath.Join(t.TempDir(), ".workflow")
	writeFile(t, dir, "workflow.js", "function main() {}")
	writeFile(t, dir, "lib/a.js", "a")
	writeFile(t, dir, ".git/HEAD", "ref")
	writeFile(t, dir, "node_modules/x/index.js", "x")
	require.NoError(t, os.Symlink(filepath.Join(dir, "lib"), filepath.Join(dir, "linked")))
	script, err := workflow.Load(filepath.Join(dir, "workflow.js"))
	require.NoError(t, err)

	files, err := script.Bundled()
	require.NoError(t, err)
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
	}
	require.Equal(t, []string{filepath.Join("lib", "a.js"), "workflow.js"}, rels)
}

func TestBundledFailsWithoutTheScript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "notes.md", "x")
	script, err := workflow.Parse(filepath.Join(dir, "workflow.js"), []byte("function main() {}"))
	require.NoError(t, err)

	_, err = script.Bundled()
	require.ErrorContains(t, err, "doesn't contain the script")
}
