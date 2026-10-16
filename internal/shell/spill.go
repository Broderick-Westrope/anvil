package shell

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// spillSubdir is where oversized command output is parked, relative to the
// caller-supplied data directory.
const spillSubdir = "shell-output"

// spillPattern matches the files SpillOutput creates. Pruning is limited to
// this shape so a misconfigured directory cannot lose unrelated files.
const spillPattern = "output-*.log"

// spillRetention is how long a spill file outlives the command that wrote
// it. Long enough to resume yesterday's session and still read what it
// referenced, short enough that stale output does not pile up.
const spillRetention = 7 * 24 * time.Hour

// spillDirLimit caps the spill directory. Age alone does not bound disk:
// a busy afternoon of large tool output can fill it well inside the
// retention window, so the oldest files go early to stay under this.
const spillDirLimit = 256 << 20

// prunedDirs records the spill directories this process has already swept,
// so the sweep costs one ReadDir per directory rather than one per command.
var prunedDirs sync.Map

// SpillDir returns the directory SpillOutput writes to for dataDir, or ""
// when dataDir is empty and spills fall back to the system temp directory.
func SpillDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, spillSubdir)
}

// SpillOutput writes command output that was too large to keep in context to
// a file the agent can read back, and returns that file's path.
//
// Files land in [SpillDir] of dataDir. An empty dataDir falls back to the
// system temp directory.
//
// Spill files outlive the command that wrote them, since the agent may want
// to read one later in the session. They are swept on a retention window
// instead; see [pruneSpills].
func SpillOutput(output, dataDir string) (string, error) {
	dir := SpillDir(dataDir)
	if dir == "" {
		// No data directory to own: fall back to the system temp
		// directory and leave pruning to the OS. Sweeping a shared
		// directory for a name we do not exclusively own would risk
		// deleting someone else's files.
		dir = os.TempDir()
	} else {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		pruneSpills(dir)
	}

	f, err := os.CreateTemp(dir, spillPattern)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.WriteString(output); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// pruneSpills deletes spill files older than [spillRetention], then the
// oldest of what remains until the directory fits [spillDirLimit]. It runs
// once per directory per process. Failures are not worth surfacing: the
// spill this sweep precedes is still perfectly good.
func pruneSpills(dir string) {
	if _, swept := prunedDirs.LoadOrStore(dir, true); swept {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Debug("Could not read spill directory", "dir", dir, "error", err)
		return
	}

	cutoff := time.Now().Add(-spillRetention)
	var kept []fs.FileInfo
	var keptBytes int64
	for _, entry := range entries {
		if match, _ := filepath.Match(spillPattern, entry.Name()); !match {
			continue
		}
		// Info fails only when the file vanished since ReadDir, which
		// leaves nothing to prune.
		info, err := entry.Info()
		switch {
		case err != nil:
		case info.ModTime().Before(cutoff):
			removeSpill(dir, info.Name())
		default:
			kept = append(kept, info)
			keptBytes += info.Size()
		}
	}

	// Still over the cap once the stale files are gone: drop the oldest
	// survivors until it fits.
	slices.SortFunc(kept, func(a, b fs.FileInfo) int {
		return a.ModTime().Compare(b.ModTime())
	})
	for _, info := range kept {
		if keptBytes > spillDirLimit {
			removeSpill(dir, info.Name())
			keptBytes -= info.Size()
		}
	}
}

func removeSpill(dir, name string) {
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		slog.Debug("Could not prune spill file", "name", name, "error", err)
	}
}
