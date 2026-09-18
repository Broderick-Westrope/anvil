package recovery

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
