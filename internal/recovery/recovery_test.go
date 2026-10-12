package recovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackerCleanExitRemovesRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "session-a", WorkingDir: "/project", Title: "Work"})
	require.NoError(t, tracker.Close(true))
	entries, err := List(root)
	require.NoError(t, err)
	require.Empty(t, entries)
	tracker.Track(Entry{SessionID: "late"})
	require.NoError(t, tracker.Close(true))
}

func TestTrackerInterruptedExitPreservesLatestSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "old", WorkingDir: "/old"})
	tracker.Track(Entry{SessionID: "latest", WorkingDir: "/new path", Title: "Latest title"})
	require.NoError(t, tracker.Close(false))
	entries, err := List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "latest", entries[0].SessionID)
	require.Equal(t, "/new path", entries[0].WorkingDir)
	require.Equal(t, "Latest title", entries[0].Title)
	require.Equal(t, os.Getpid(), entries[0].PID)
	require.False(t, entries[0].UpdatedAt.IsZero())
}

func TestListExcludesLiveSessionsAndDeduplicatesInterruptedRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for range 2 {
		tracker, err := NewTracker(root)
		require.NoError(t, err)
		tracker.Track(Entry{SessionID: "same", WorkingDir: "/project"})
		require.NoError(t, tracker.Close(false))
	}
	entries, err := List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	live, err := NewTracker(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, live.Close(true)) })
	live.Track(Entry{SessionID: "same", WorkingDir: "/project"})
	require.Eventually(t, func() bool {
		entries, err := List(root)
		return err == nil && len(entries) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestReopeningSessionDismissesItsInterruptedRecords(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, sessionID := range []string{"reopened", "reopened", "untouched"} {
		interrupted, err := NewTracker(root)
		require.NoError(t, err)
		interrupted.Track(Entry{SessionID: sessionID, WorkingDir: "/project"})
		require.NoError(t, interrupted.Close(false))
	}
	resumed, err := NewTracker(root)
	require.NoError(t, err)
	resumed.Track(Entry{SessionID: "reopened", WorkingDir: "/elsewhere"})
	require.NoError(t, resumed.Close(true))
	entries, err := List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "untouched", entries[0].SessionID)
}

func TestTrackerEmptySessionClearsPreviousSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "old"})
	tracker.Track(Entry{})
	require.NoError(t, tracker.Close(false))
	entries, err := List(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestClearPreservesLiveRecords(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	interrupted, err := NewTracker(root)
	require.NoError(t, err)
	interrupted.Track(Entry{SessionID: "interrupted"})
	require.NoError(t, interrupted.Close(false))
	live, err := NewTracker(root)
	require.NoError(t, err)
	live.Track(Entry{SessionID: "live"})
	require.NoError(t, Clear(root))
	require.NoError(t, live.Close(false))
	entries, err := List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "live", entries[0].SessionID)
}

func TestTrackerSurvivesForceKill(t *testing.T) {
	if root := os.Getenv("ANVIL_TEST_RECOVERY_ROOT"); root != "" {
		tracker, err := NewTracker(root)
		require.NoError(t, err)
		tracker.Track(Entry{SessionID: "killed", WorkingDir: "/project with spaces", Title: "Recover me"})
		require.Eventually(t, func() bool {
			files, err := filepath.Glob(filepath.Join(root, "*.json"))
			return err == nil && len(files) == 1
		}, 5*time.Second, 10*time.Millisecond)
		fmt.Println("ready")
		<-time.After(time.Minute)
		return
	}
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTrackerSurvivesForceKill$")
	command.Env = append(os.Environ(), "ANVIL_TEST_RECOVERY_ROOT="+root)
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	entries, err := List(root)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait())
	entries, err = List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "killed", entries[0].SessionID)
	require.Equal(t, "/project with spaces", entries[0].WorkingDir)
	require.Equal(t, "Recover me", entries[0].Title)
}

func TestDamagedRecordsDoNotHideValidSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "valid"})
	require.NoError(t, tracker.Close(false))
	bad := filepath.Join(root, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o600))
	entries, err := List(root)
	require.ErrorContains(t, err, "bad.json")
	require.Len(t, entries, 1)
	require.Equal(t, "valid", entries[0].SessionID)
	require.NoError(t, os.WriteFile(filepath.Join(root, "empty.json"), []byte("{}"), 0o600))
	require.NoError(t, Clear(root))
	files, err := filepath.Glob(filepath.Join(root, "*.json"))
	require.NoError(t, err)
	require.Empty(t, files)
}

func TestClearDoesNotDecodeLiveDamagedRecord(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	defer tracker.Close(true)
	require.NoError(t, os.WriteFile(tracker.path, []byte("{"), 0o600))
	require.NoError(t, Clear(root))
	require.FileExists(t, tracker.path)
}

func TestRecoveryReclaimsAbandonedArtifacts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := NewTracker(root)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "closed"})
	require.NoError(t, tracker.Close(true))
	files, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, file := range files {
		require.Equal(t, ".registry.lock", file.Name())
	}
	dead, err := NewTracker(root)
	require.NoError(t, err)
	dead.Track(Entry{SessionID: "recoverable"})
	require.NoError(t, dead.Close(false))
	temp := strings.TrimSuffix(dead.path, ".json") + ".tmp-abandoned"
	require.NoError(t, os.WriteFile(temp, []byte("partial"), 0o600))
	live, err := NewTracker(root)
	require.NoError(t, err)
	liveTemp := strings.TrimSuffix(live.path, ".json") + ".tmp-live"
	require.NoError(t, os.WriteFile(liveTemp, []byte("partial"), 0o600))
	legacy := filepath.Join(root, ".recovery-legacy")
	require.NoError(t, os.WriteFile(legacy, nil, 0o600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(legacy, old, old))
	require.NoError(t, Clear(root))
	require.NoFileExists(t, temp)
	require.NoFileExists(t, strings.TrimSuffix(dead.path, ".json")+".lock")
	require.FileExists(t, liveTemp)
	require.FileExists(t, legacy)
	require.NoError(t, live.Close(true))
	require.NoError(t, Clear(root))
	require.NoFileExists(t, liveTemp)
	require.NoFileExists(t, legacy)
}

func TestClearRetainsRecentLegacyTemporaryFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".recovery-recent")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, Clear(root))
	require.FileExists(t, path)
}

func TestRecoveryConcurrentCleanupAndTracking(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			tracker, err := NewTracker(root)
			if !assert.NoError(t, err) {
				return
			}
			defer func() { assert.NoError(t, tracker.Close(true)) }()
			for range 10 {
				tracker.Track(Entry{SessionID: "active"})
				assert.NoError(t, Clear(root))
				entries, err := List(root)
				assert.NoError(t, err)
				assert.Empty(t, entries)
			}
		})
	}
	workers.Wait()
}

func TestListMissingDirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "missing")
	entries, err := List(root)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, Clear(root))
}

func TestTrackerHeartbeatRefreshesLastSeen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := newTracker(root, 10*time.Millisecond)
	require.NoError(t, err)
	tracker.Track(Entry{SessionID: "running"})
	read := func() Entry {
		data, err := os.ReadFile(tracker.path)
		if err != nil {
			return Entry{}
		}
		var entry Entry
		_ = json.Unmarshal(data, &entry)
		return entry
	}
	require.Eventually(t, func() bool {
		entry := read()
		return entry.SessionID == "running" && entry.SeenAt.Sub(entry.UpdatedAt) > 20*time.Millisecond
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, tracker.Close(false))
	entries, err := List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].LastSeen().After(entries[0].UpdatedAt))
}

func TestGroupByInterruption(t *testing.T) {
	t.Parallel()
	restart := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	entries := []Entry{
		{SessionID: "a", SeenAt: restart},
		{SessionID: "b", SeenAt: restart.Add(-50 * time.Second)},
		{SessionID: "legacy", UpdatedAt: restart.Add(-90 * time.Second)},
		{SessionID: "old", SeenAt: restart.Add(-48 * time.Hour)},
	}
	groups := GroupByInterruption(entries)
	require.Len(t, groups, 2)
	require.Equal(t, []string{"a", "b", "legacy"}, sessionIDs(groups[0]))
	require.Equal(t, []string{"old"}, sessionIDs(groups[1]))
	require.Empty(t, GroupByInterruption(nil))
}

func sessionIDs(entries []Entry) []string {
	ids := make([]string, len(entries))
	for index, entry := range entries {
		ids[index] = entry.SessionID
	}
	return ids
}

func TestListSkipsDirectoriesForeignFilesAndRecordsWithoutSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "dir.json"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "blank.json"), []byte(`{"title":"No session"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "kept.json"), []byte(`{"session_id":"kept"}`), 0o600))

	entries, err := List(root)

	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, sessionIDs(entries))
}

func TestListReportsRecordsWhoseLockCannotBeChecked(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "stuck.json"), []byte(`{"session_id":"stuck"}`), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "stuck.lock"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "kept.json"), []byte(`{"session_id":"kept"}`), 0o600))

	entries, err := List(root)

	require.ErrorContains(t, err, "checking recovery record stuck.json: ")
	require.Equal(t, []string{"kept"}, sessionIDs(entries))

	require.ErrorContains(t, Clear(root), "checking recovery record stuck.json: ")
	require.FileExists(t, filepath.Join(root, "stuck.json"))
	require.NoFileExists(t, filepath.Join(root, "kept.json"))
	require.DirExists(t, filepath.Join(root, "stuck.lock"))
}

func TestListFailsWhenRootIsNotADirectory(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(root, nil, 0o600))

	entries, err := List(root)

	require.Error(t, err)
	require.NotNil(t, entries)
	require.Empty(t, entries)
}

func TestClearLeavesArtifactDirectoriesAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{".recovery-dir", "gone.tmp-dir", "gone.lock"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(path, 0o700))
		require.NoError(t, os.Chtimes(path, old, old))
	}

	require.NoError(t, Clear(root))

	for _, name := range []string{".recovery-dir", "gone.tmp-dir", "gone.lock"} {
		require.DirExists(t, filepath.Join(root, name))
	}
}

func TestClearKeepsLegacyFilesWhileAnyWindowIsRunning(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	live, err := NewTracker(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, live.Close(true)) })
	legacy := filepath.Join(root, ".recovery-legacy")
	require.NoError(t, os.WriteFile(legacy, nil, 0o600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(legacy, old, old))
	orphanTemp := filepath.Join(root, "gone.tmp-1")
	require.NoError(t, os.WriteFile(orphanTemp, nil, 0o600))
	orphanLock := filepath.Join(root, "gone.lock")
	require.NoError(t, os.WriteFile(orphanLock, nil, 0o600))
	keptLock := filepath.Join(root, "kept.lock")
	require.NoError(t, os.WriteFile(keptLock, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "kept.json"), []byte(`{}`), 0o600))

	require.NoError(t, Clear(root))

	require.FileExists(t, legacy)
	require.NoFileExists(t, orphanTemp)
	require.NoFileExists(t, orphanLock)
	require.NoFileExists(t, filepath.Join(root, "kept.json"))
	require.FileExists(t, filepath.Join(root, ".registry.lock"))
}

func TestSweepKeepsTemporariesOfUnreadableLocks(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for the current user")
	}
	t.Parallel()
	root := t.TempDir()
	lockPath := filepath.Join(root, "owner.lock")
	require.NoError(t, os.WriteFile(lockPath, nil, 0o600))
	require.NoError(t, os.Chmod(lockPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })
	temp := filepath.Join(root, "owner.tmp-1")
	require.NoError(t, os.WriteFile(temp, nil, 0o600))
	legacy := filepath.Join(root, ".recovery-legacy")
	require.NoError(t, os.WriteFile(legacy, nil, 0o600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(legacy, old, old))

	err := Clear(root)

	require.ErrorContains(t, err, "owner.lock")
	require.ErrorIs(t, err, os.ErrPermission)
	require.FileExists(t, temp)
	require.FileExists(t, lockPath)
	require.FileExists(t, legacy)
}

func TestListReportsUnreadableRecords(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for the current user")
	}
	t.Parallel()
	root := t.TempDir()
	unreadable := filepath.Join(root, "unreadable.json")
	require.NoError(t, os.WriteFile(unreadable, []byte(`{"session_id":"hidden"}`), 0o000))
	require.NoError(t, os.WriteFile(filepath.Join(root, "kept.json"), []byte(`{"session_id":"kept"}`), 0o600))

	entries, err := List(root)

	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, []string{"kept"}, sessionIDs(entries))
}

func TestSweepReportsArtifactsItCannotRemove(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for the current user")
	}
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{".registry.lock", "gone.lock", "gone.tmp-1"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), nil, 0o600))
	}
	require.NoError(t, os.Chmod(root, 0o500))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	err := Clear(root)

	require.ErrorIs(t, err, os.ErrPermission)
	require.ErrorContains(t, err, "gone.lock")
	require.ErrorContains(t, err, "gone.tmp-1")
	require.FileExists(t, filepath.Join(root, "gone.lock"))
	require.FileExists(t, filepath.Join(root, "gone.tmp-1"))
}
