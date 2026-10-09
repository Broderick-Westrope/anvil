package shell

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSpillOutput_WritesFullOutput(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	path, err := SpillOutput("full output", dataDir)
	require.NoError(t, err)

	require.Equal(t, SpillDir(dataDir), filepath.Dir(path))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "full output", string(got))
}

func TestSpillOutput_PrunesOldFilesOnly(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	spillDir := SpillDir(dataDir)
	require.NoError(t, os.MkdirAll(spillDir, 0o700))

	stale := filepath.Join(spillDir, "output-stale.log")
	fresh := filepath.Join(spillDir, "output-fresh.log")
	keep := filepath.Join(spillDir, "notes.txt")
	for _, path := range []string{stale, fresh, keep} {
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	}
	old := time.Now().Add(-spillRetention - time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))
	require.NoError(t, os.Chtimes(keep, old, old))

	_, err := SpillOutput("hello", dataDir)
	require.NoError(t, err)

	require.NoFileExists(t, stale)
	require.FileExists(t, fresh)
	require.FileExists(t, keep, "pruning must not touch unrelated files")
}

func TestSpillOutput_LeavesTempDirAlone(t *testing.T) {
	t.Parallel()
	bystander, err := os.CreateTemp("", "output-*.log")
	require.NoError(t, err)
	require.NoError(t, bystander.Close())
	t.Cleanup(func() { os.Remove(bystander.Name()) })
	old := time.Now().Add(-spillRetention - time.Hour)
	require.NoError(t, os.Chtimes(bystander.Name(), old, old))

	path, err := SpillOutput("hello", "")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(path) })

	require.FileExists(t, bystander.Name())
}

func TestSpillOutput_EnforcesDirectoryLimit(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	spillDir := SpillDir(dataDir)
	require.NoError(t, os.MkdirAll(spillDir, 0o700))

	// Three fresh files, together over the cap. The oldest go first. The
	// files are sparse, so the test does not write hundreds of megabytes.
	names := []string{"output-1.log", "output-2.log", "output-3.log"}
	for i, name := range names {
		path := filepath.Join(spillDir, name)
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(spillDirLimit/2))
		require.NoError(t, f.Close())
		age := time.Duration(len(names)-i) * time.Hour
		stamp := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, stamp, stamp))
	}

	_, err := SpillOutput("hello", dataDir)
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(spillDir, "output-1.log"), "oldest goes first")
	require.FileExists(t, filepath.Join(spillDir, "output-3.log"), "newest survives")
}
