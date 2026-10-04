package entrydir

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStrayMarkdown(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"old.md", "Shout.MD", "README.md", ".hidden.md", "notes.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proper"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "proper", "SKILL.md"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dir.md"), 0o755))

	require.ElementsMatch(t, []string{
		filepath.Join(root, "old.md"),
		filepath.Join(root, "Shout.MD"),
	}, StrayMarkdown(root))
}

func TestStrayMarkdown_MissingRoot(t *testing.T) {
	t.Parallel()

	require.Empty(t, StrayMarkdown(filepath.Join(t.TempDir(), "missing")))
}
