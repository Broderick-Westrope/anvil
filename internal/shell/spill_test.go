package shell

import (
	"fmt"
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

	// A stale file listed first must not stop the sweep from counting the
	// fresh files after it.
	stale := filepath.Join(spillDir, "output-0.log")
	require.NoError(t, os.WriteFile(stale, []byte("x"), 0o600))
	old := time.Now().Add(-spillRetention - time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	// Three fresh files, together over the cap. The oldest go first.
	writeSparseSpills(t, spillDir, spillDirLimit/2, spillDirLimit/2, spillDirLimit/2)

	_, err := SpillOutput("hello", dataDir)
	require.NoError(t, err)

	require.NoFileExists(t, stale)
	require.NoFileExists(t, filepath.Join(spillDir, "output-1.log"), "oldest goes first")
	require.FileExists(t, filepath.Join(spillDir, "output-2.log"))
	require.FileExists(t, filepath.Join(spillDir, "output-3.log"), "newest survives")
}

func TestSpillOutput_KeepsDirectoryAtLimit(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	spillDir := SpillDir(dataDir)
	require.NoError(t, os.MkdirAll(spillDir, 0o700))

	writeSparseSpills(t, spillDir, spillDirLimit/2, spillDirLimit/2)

	_, err := SpillOutput("hello", dataDir)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(spillDir, "output-1.log"))
	require.FileExists(t, filepath.Join(spillDir, "output-2.log"))
}

func TestSpillOutput_PrunesUntilUnderLimit(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	spillDir := SpillDir(dataDir)
	require.NoError(t, os.MkdirAll(spillDir, 0o700))

	// Dropping the tiny oldest file is not enough; the next one goes too.
	writeSparseSpills(t, spillDir, 1, spillDirLimit/2, spillDirLimit/2, spillDirLimit/2)

	_, err := SpillOutput("hello", dataDir)
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(spillDir, "output-1.log"))
	require.NoFileExists(t, filepath.Join(spillDir, "output-2.log"))
	require.FileExists(t, filepath.Join(spillDir, "output-3.log"))
	require.FileExists(t, filepath.Join(spillDir, "output-4.log"))
}

// writeSparseSpills creates fresh spill files output-1.log, output-2.log, ...
// with the given sizes, oldest first. The files are sparse, so the tests do
// not write hundreds of megabytes.
func writeSparseSpills(t *testing.T, dir string, sizes ...int64) {
	t.Helper()
	for i, size := range sizes {
		path := filepath.Join(dir, fmt.Sprintf("output-%d.log", i+1))
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, f.Truncate(size))
		require.NoError(t, f.Close())
		age := time.Duration(len(sizes)-i) * time.Hour
		stamp := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, stamp, stamp))
	}
}
