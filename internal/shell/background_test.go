package shell

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackgroundShellManager_Start(t *testing.T) {
	t.Skip("Skipping this until I figure out why its flaky")
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'hello world'", "")
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	if bgShell.ID() == "" {
		t.Error("expected shell ID to be non-empty")
	}

	// Wait for the command to complete
	bgShell.Wait()

	stdout, stderr, done, err := bgShell.GetOutput()
	if !done {
		t.Error("expected shell to be done")
	}

	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	if !strings.Contains(stdout, "hello world") {
		t.Errorf("expected stdout to contain 'hello world', got: %s", stdout)
	}

	if stderr != "" {
		t.Errorf("expected empty stderr, got: %s", stderr)
	}
}

func TestBackgroundShellManager_Get(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'test'", "")
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Retrieve the shell
	retrieved, ok := manager.Get(bgShell.ID())
	if !ok {
		t.Error("expected to find the background shell")
	}

	if retrieved.ID() != bgShell.ID() {
		t.Errorf("expected shell ID %s, got %s", bgShell.ID(), retrieved.ID())
	}

	// Clean up
	manager.Kill(bgShell.ID())
}

func TestBackgroundShellManager_Kill(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start a long-running command
	bgShell, err := manager.Start(ctx, workingDir, nil, "sleep 10", "")
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Kill it
	err = manager.Kill(bgShell.ID())
	if err != nil {
		t.Errorf("failed to kill background shell: %v", err)
	}

	// Verify it's no longer in the manager
	_, ok := manager.Get(bgShell.ID())
	if ok {
		t.Error("expected shell to be removed after kill")
	}

	// Verify the shell is done
	if !bgShell.IsDone() {
		t.Error("expected shell to be done after kill")
	}
}

func TestBackgroundShellManager_KillNonExistent(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()

	err := manager.Kill("non-existent-id")
	if err == nil {
		t.Error("expected error when killing non-existent shell")
	}
}

// TestBackgroundShellManager_Kill_Timeout asserts that Kill returns in
// bounded time. With process-group cancellation in place the normal-case
// return is well under KillGracePeriod; this test exists as a regression
// guard against the prior unbounded `<-shell.done` wait, so the upper
// bound is "KillGracePeriod plus a small wall-clock fudge" rather than
// the 60s sleep the command would otherwise run for.
func TestBackgroundShellManager_Kill_Timeout(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(t.Context(), workingDir, nil, "trap '' TERM INT; sleep 60", "")
	require.NoError(t, err)

	// Give the trap builtin time to register before we cancel.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	err = manager.Kill(bgShell.ID())
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Less(t, elapsed, KillGracePeriod+2*time.Second)
}

func TestBackgroundShell_IsDone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	bgShell, err := manager.Start(ctx, workingDir, nil, "echo 'quick'", "")
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Wait a bit for the command to complete
	time.Sleep(100 * time.Millisecond)

	if !bgShell.IsDone() {
		t.Error("expected shell to be done")
	}

	// Clean up
	manager.Kill(bgShell.ID())
}

func TestBackgroundShell_WithBlockFuncs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	blockFuncs := []BlockFunc{
		CommandsBlocker([]string{"curl", "wget"}),
	}

	bgShell, err := manager.Start(ctx, workingDir, blockFuncs, "curl example.com", "")
	if err != nil {
		t.Fatalf("failed to start background shell: %v", err)
	}

	// Wait for the command to complete
	bgShell.Wait()

	stdout, stderr, done, execErr := bgShell.GetOutput()
	if !done {
		t.Error("expected shell to be done")
	}

	// The command should have been blocked
	output := stdout + stderr
	if !strings.Contains(output, "not allowed") && execErr == nil {
		t.Errorf("expected command to be blocked, got stdout: %s, stderr: %s, err: %v", stdout, stderr, execErr)
	}

	// Clean up
	manager.Kill(bgShell.ID())
}

func TestBackgroundShellManager_List(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping flacky test on windows")
	}

	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start two shells
	bgShell1, err := manager.Start(ctx, workingDir, nil, "sleep 1", "")
	if err != nil {
		t.Fatalf("failed to start first background shell: %v", err)
	}

	bgShell2, err := manager.Start(ctx, workingDir, nil, "sleep 1", "")
	if err != nil {
		t.Fatalf("failed to start second background shell: %v", err)
	}

	ids := manager.List()

	// Check that both shells are in the list
	found1 := false
	found2 := false
	for _, id := range ids {
		if id == bgShell1.ID() {
			found1 = true
		}
		if id == bgShell2.ID() {
			found2 = true
		}
	}

	if !found1 {
		t.Errorf("expected to find shell %s in list", bgShell1.ID())
	}
	if !found2 {
		t.Errorf("expected to find shell %s in list", bgShell2.ID())
	}

	// Clean up
	manager.Kill(bgShell1.ID())
	manager.Kill(bgShell2.ID())
}

func TestBackgroundShellManager_KillAll(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start multiple long-running shells
	shell1, err := manager.Start(ctx, workingDir, nil, "sleep 10", "")
	if err != nil {
		t.Fatalf("failed to start shell 1: %v", err)
	}

	shell2, err := manager.Start(ctx, workingDir, nil, "sleep 10", "")
	if err != nil {
		t.Fatalf("failed to start shell 2: %v", err)
	}

	shell3, err := manager.Start(ctx, workingDir, nil, "sleep 10", "")
	if err != nil {
		t.Fatalf("failed to start shell 3: %v", err)
	}

	// Verify shells are running
	if shell1.IsDone() || shell2.IsDone() || shell3.IsDone() {
		t.Error("shells should not be done yet")
	}

	// Kill all shells
	manager.KillAll(t.Context())

	// Verify all shells are done
	if !shell1.IsDone() {
		t.Error("shell1 should be done after KillAll")
	}
	if !shell2.IsDone() {
		t.Error("shell2 should be done after KillAll")
	}
	if !shell3.IsDone() {
		t.Error("shell3 should be done after KillAll")
	}

	// Verify they're removed from the manager
	if _, ok := manager.Get(shell1.ID()); ok {
		t.Error("shell1 should be removed from manager")
	}
	if _, ok := manager.Get(shell2.ID()); ok {
		t.Error("shell2 should be removed from manager")
	}
	if _, ok := manager.Get(shell3.ID()); ok {
		t.Error("shell3 should be removed from manager")
	}

	// Verify list is empty (or doesn't contain our shells)
	ids := manager.List()
	for _, id := range ids {
		if id == shell1.ID() || id == shell2.ID() || id == shell3.ID() {
			t.Errorf("shell %s should not be in list after KillAll", id)
		}
	}
}

func TestBackgroundShellManager_KillAll_Timeout(t *testing.T) {
	t.Parallel()

	// XXX: can't use synctest here - causes --race to trip.

	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start a shell that traps signals and ignores cancellation.
	_, err := manager.Start(t.Context(), workingDir, nil, "trap '' TERM INT; sleep 60", "")
	require.NoError(t, err)

	// Short timeout to test the timeout path.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	t.Cleanup(cancel)

	start := time.Now()
	manager.KillAll(ctx)

	elapsed := time.Since(start)

	// Must return promptly after timeout, not hang for 60 seconds.
	require.Less(t, elapsed, 2*time.Second)
}

func TestSyncBuffer_Cap(t *testing.T) {
	t.Parallel()

	var sb syncBuffer

	// Write multiple chunks that together exceed MaxBufferSize.
	chunk := make([]byte, MaxBufferSize/2+1)
	for i := range chunk {
		chunk[i] = 'A'
	}

	sb.Write(chunk)
	sb.Write(chunk)

	require.LessOrEqual(t, sb.Len(), MaxBufferSize)
	require.Contains(t, sb.String(), truncationMarker)
}

func TestSyncBuffer_CapSingleLargeWrite(t *testing.T) {
	t.Parallel()

	var sb syncBuffer

	// Single write larger than MaxBufferSize.
	data := make([]byte, MaxBufferSize+1024)
	for i := range data {
		data[i] = 'B'
	}

	// io.Writer contract: n must equal len(p) even when truncating.
	n, err := sb.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	require.LessOrEqual(t, sb.Len(), MaxBufferSize)
	require.Contains(t, sb.String(), truncationMarker)

	// Only the tail should be retained after the marker.
	content := sb.String()
	afterMarker := content[len(truncationMarker):]
	for _, b := range []byte(afterMarker) {
		require.Equal(t, byte('B'), b)
	}
}

func TestSyncBuffer_WriteStringDelegatesToWrite(t *testing.T) {
	t.Parallel()

	var sb syncBuffer

	// WriteString with data exceeding MaxBufferSize must also be capped.
	data := strings.Repeat("C", MaxBufferSize+512)
	sb.WriteString(data)

	require.LessOrEqual(t, sb.Len(), MaxBufferSize)
	require.Contains(t, sb.String(), truncationMarker)
}

func TestCleanupCompleted(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	manager := newBackgroundShellManager()

	// Start a long-running job.
	running, err := manager.Start(t.Context(), workingDir, nil, "sleep 60", "")
	require.NoError(t, err)

	// Start a job that completes quickly.
	completed, err := manager.Start(t.Context(), workingDir, nil, "echo done", "")
	require.NoError(t, err)

	completed.Wait()

	removed := manager.CleanupCompleted()
	require.Equal(t, 1, removed)

	// The completed job should be gone.
	_, ok := manager.Get(completed.ID())
	require.False(t, ok)

	// The running job should still be present.
	_, ok = manager.Get(running.ID())
	require.True(t, ok)

	// Clean up.
	manager.Kill(running.ID())
}

func TestBackgroundShell_WaitContext_Completed(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	close(done)

	bgShell := &BackgroundShell{done: done}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	require.True(t, bgShell.WaitContext(ctx))
}

func TestBackgroundShell_WaitContext_Canceled(t *testing.T) {
	t.Parallel()

	bgShell := &BackgroundShell{done: make(chan struct{})}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.False(t, bgShell.WaitContext(ctx))
}

type countingAllocator struct {
	calls atomic.Int64
}

func (c *countingAllocator) NextID(context.Context) (string, error) {
	return fmt.Sprintf("JOB-%d", c.calls.Add(1)), nil
}

// registerBlockingShell adds a shell whose goroutine ignores cancellation
// and exits only when release is closed.
func registerBlockingShell(t *testing.T, m *BackgroundShellManager, release <-chan struct{}) *BackgroundShell {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	key := fmt.Sprintf("run-%d", runCounter.Add(1))
	bs := &BackgroundShell{
		id:        key,
		Command:   "blocked",
		startedAt: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
		stdout:    &syncBuffer{},
		stderr:    &syncBuffer{},
		done:      make(chan struct{}),
	}
	go func() {
		defer close(bs.done)
		<-release
		bs.completedAt.Store(time.Now().UnixNano())
	}()

	m.mu.Lock()
	m.shells.Set(key, bs)
	m.mu.Unlock()
	return bs
}

func startShell(t *testing.T, m *BackgroundShellManager, command string) *BackgroundShell {
	t.Helper()
	bs, err := m.Start(t.Context(), t.TempDir(), nil, command, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Kill(bs.ID()) })
	return bs
}

func publishShell(t *testing.T, m *BackgroundShellManager, bs *BackgroundShell, sessionID string, origin JobOrigin) string {
	t.Helper()
	id, err := m.Publish(t.Context(), bs.ID(), PublishOptions{SessionID: sessionID, Origin: origin})
	require.NoError(t, err)
	return id
}

func jobIDs(jobs []JobInfo) []string {
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	return ids
}

func TestBackgroundShellManager_StartWithoutPublish(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	alloc := &countingAllocator{}
	manager.SetIDAllocator(alloc)

	bs := startShell(t, manager, "echo hi")
	bs.Wait()

	require.True(t, strings.HasPrefix(bs.ID(), "run-"))
	require.Empty(t, manager.ListAll())
	require.Zero(t, alloc.calls.Load())
}

func TestBackgroundShellManager_Publish(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	alloc := &countingAllocator{}
	manager.SetIDAllocator(alloc)

	bs := startShell(t, manager, "sleep 30")
	oldKey := bs.ID()
	publishedAt := time.Now()

	newID := publishShell(t, manager, bs, "session-a", OriginExplicit)
	require.Equal(t, "JOB-1", newID)
	require.Equal(t, newID, bs.ID())

	got, ok := manager.Get(newID)
	require.True(t, ok)
	require.Same(t, bs, got)

	_, ok = manager.shells.Get(oldKey)
	require.False(t, ok, "old key must not be a map entry after publish")

	info := bs.Info()
	require.Equal(t, "session-a", info.SessionID)
	require.Equal(t, OriginExplicit, info.Origin)
	require.False(t, info.StartedAt.IsZero())
	require.True(t, info.StartedAt.Before(publishedAt))

	again, err := manager.Publish(t.Context(), newID, PublishOptions{SessionID: "other", Origin: OriginAuto})
	require.NoError(t, err)
	require.Equal(t, newID, again)
	require.Equal(t, int64(1), alloc.calls.Load())

	_, err = manager.Publish(t.Context(), "missing", PublishOptions{})
	require.Error(t, err)
}

func TestBackgroundShellManager_ListAfterPublish(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	bs := startShell(t, manager, "sleep 30")
	oldKey := bs.ID()
	newID := publishShell(t, manager, bs, "s", OriginAuto)

	require.Contains(t, manager.List(), newID)
	require.NotContains(t, manager.List(), oldKey)
}

func TestBackgroundShellManager_ListBySession(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()

	r1 := startShell(t, manager, "sleep 30")
	r2 := startShell(t, manager, "sleep 30")
	var finished []*BackgroundShell
	for range 3 {
		bs := startShell(t, manager, "echo done")
		bs.Wait()
		finished = append(finished, bs)
	}
	other := startShell(t, manager, "sleep 30")
	startShell(t, manager, "sleep 30")

	// Publish out of order so ordering cannot come from publication.
	f1 := publishShell(t, manager, finished[1], "s", OriginExplicit)
	id2 := publishShell(t, manager, r2, "s", OriginExplicit)
	f0 := publishShell(t, manager, finished[0], "s", OriginAuto)
	id1 := publishShell(t, manager, r1, "s", OriginAuto)
	f2 := publishShell(t, manager, finished[2], "s", OriginExplicit)
	otherID := publishShell(t, manager, other, "other", OriginExplicit)

	jobs := manager.ListBySession("s")
	require.Equal(t, []string{id1, id2, f2, f1, f0}, jobIDs(jobs))
	require.False(t, jobs[0].Done)
	require.False(t, jobs[1].Done)
	for _, j := range jobs[2:] {
		require.True(t, j.Done)
		require.Zero(t, j.ExitCode)
		require.False(t, j.CompletedAt.IsZero())
	}

	require.Equal(t, []string{otherID}, jobIDs(manager.ListBySession("other")))
	require.Len(t, manager.ListAll(), 6)
}

func TestBackgroundShellManager_Transfer(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()

	autoRunning := startShell(t, manager, "sleep 30")
	explicitRunning := startShell(t, manager, "sleep 30")
	explicitDone := startShell(t, manager, "echo done")
	explicitDone.Wait()
	unrelated := startShell(t, manager, "sleep 30")

	autoID := publishShell(t, manager, autoRunning, "child", OriginAuto)
	runID := publishShell(t, manager, explicitRunning, "child", OriginExplicit)
	doneID := publishShell(t, manager, explicitDone, "child", OriginExplicit)
	publishShell(t, manager, unrelated, "elsewhere", OriginAuto)

	handed, toKill := manager.Transfer("child", "parent")

	require.Equal(t, []string{autoID}, toKill)
	require.Equal(t, "child", autoRunning.Info().SessionID)
	require.ElementsMatch(t, []string{runID, doneID}, jobIDs(handed))
	for _, j := range handed {
		require.Equal(t, "parent", j.SessionID)
	}
	require.Equal(t, "parent", explicitRunning.Info().SessionID)
	require.Equal(t, "parent", explicitDone.Info().SessionID)
	require.Equal(t, "elsewhere", unrelated.Info().SessionID)
}

func TestBackgroundShellManager_KillReturnsTimeout(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	manager.gracePeriod = 50 * time.Millisecond

	release := make(chan struct{})
	bs := registerBlockingShell(t, manager, release)

	err := manager.Kill(bs.ID())
	require.ErrorIs(t, err, ErrKillTimeout)

	_, ok := manager.Get(bs.ID())
	require.False(t, ok)

	close(release)
	bs.Wait()
}

func TestBackgroundShellManager_PublishRacingKill(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()

	for range 50 {
		bs, err := manager.Start(t.Context(), t.TempDir(), nil, "sleep 30", "")
		require.NoError(t, err)
		oldKey := bs.ID()

		var (
			wg       sync.WaitGroup
			newID    string
			pubErr   error
			startSig = make(chan struct{})
		)
		wg.Go(func() {
			<-startSig
			newID, pubErr = manager.Publish(t.Context(), oldKey, PublishOptions{SessionID: "s", Origin: OriginExplicit})
		})
		wg.Go(func() {
			<-startSig
			_ = manager.Kill(oldKey)
		})
		close(startSig)
		wg.Wait()

		waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		require.True(t, bs.WaitContext(waitCtx), "shell must be done after kill")
		cancel()

		_, ok := manager.Get(oldKey)
		require.False(t, ok)
		if pubErr == nil {
			_, ok = manager.Get(newID)
			require.False(t, ok)
		}
	}
	require.Empty(t, manager.List())
}

func TestBackgroundShellManager_KillOldKeyAfterPublish(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	bs, err := manager.Start(t.Context(), t.TempDir(), nil, "sleep 30", "")
	require.NoError(t, err)
	oldKey := bs.ID()
	newID := publishShell(t, manager, bs, "s", OriginAuto)

	got, ok := manager.Get(oldKey)
	require.True(t, ok)
	require.Same(t, bs, got)

	require.NoError(t, manager.Kill(oldKey))
	require.True(t, bs.IsDone())
	_, ok = manager.Get(newID)
	require.False(t, ok)
	_, ok = manager.Get(oldKey)
	require.False(t, ok)
	require.Empty(t, manager.aliases)
}

func TestBackgroundShell_LastOutputAt(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	bs := startShell(t, manager, "sleep 0.3; echo hi")

	require.True(t, bs.Info().LastOutputAt.IsZero())

	bs.Wait()
	info := bs.Info()
	require.False(t, info.LastOutputAt.IsZero())
	require.False(t, info.LastOutputAt.Before(info.StartedAt))
}
