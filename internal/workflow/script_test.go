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
	require.Empty(t, script.Warnings)
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
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			script, err := workflow.Parse("workflow.js", []byte(src))
			require.NoError(t, err)
			require.NotEmpty(t, script.Warnings)
		})
	}
}

func TestParseRequiresMain(t *testing.T) {
	t.Parallel()

	_, err := workflow.Parse("workflow.js", []byte(`requires({})`))
	require.ErrorContains(t, err, "main")
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
  references: ["references/da.md", "references/nope.md", "../escape.md"],
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
		`reference "references/nope.md" not found`,
		`reference "../escape.md" is outside the workflow directory`,
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
