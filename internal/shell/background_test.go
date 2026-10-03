package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
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
	exited, abandoned := manager.KillAll(t.Context())
	require.ElementsMatch(t, []string{shell1.ID(), shell2.ID(), shell3.ID()}, exited)
	require.Empty(t, abandoned)

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
	bs, err := manager.Start(t.Context(), workingDir, nil, "trap '' TERM INT; sleep 60", "")
	require.NoError(t, err)

	// Short timeout to test the timeout path.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	t.Cleanup(cancel)

	start := time.Now()
	exited, abandoned := manager.KillAll(ctx)

	elapsed := time.Since(start)
	if !bs.IsDone() {
		require.Equal(t, []string{bs.ID()}, abandoned)
		require.Empty(t, exited)
	}

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
	// Windows' clock is coarse enough that both reads can return the same
	// instant, so only require that the start isn't after publication.
	require.False(t, info.StartedAt.After(publishedAt))

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

// memJobLog is a [JobLog] that keeps output in memory.
type memJobLog struct {
	stdout, stderr memFile
	closed         atomic.Bool
}

func (l *memJobLog) Stdout() io.Writer { return &l.stdout }
func (l *memJobLog) Stderr() io.Writer { return &l.stderr }

func (l *memJobLog) Close() LogStats {
	l.closed.Store(true)
	return LogStats{Bytes: int64(len(l.stdout.String()) + len(l.stderr.String()))}
}

type finalizeCall struct {
	id        string
	info      JobInfo
	endReason string
	stats     LogStats
}

type transferCall struct {
	ids       []string
	toSession string
}

// fakeRecorder records calls and hands out in-memory logs.
type fakeRecorder struct {
	allocErr  error
	next      atomic.Int64
	finalized chan finalizeCall
	transfers chan transferCall

	mu   sync.Mutex
	reqs []AllocateRequest
	logs map[string]*memJobLog
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{
		finalized: make(chan finalizeCall, 16),
		transfers: make(chan transferCall, 16),
		logs:      make(map[string]*memJobLog),
	}
}

func (r *fakeRecorder) Allocate(_ context.Context, req AllocateRequest) (string, JobLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if r.allocErr != nil {
		return "", nil, r.allocErr
	}
	id := fmt.Sprintf("%03X", r.next.Add(1))
	l := &memJobLog{}
	r.logs[id] = l
	return id, l, nil
}

func (r *fakeRecorder) Finalize(_ context.Context, id string, info JobInfo, endReason string, stats LogStats) error {
	r.finalized <- finalizeCall{id: id, info: info, endReason: endReason, stats: stats}
	return nil
}

func (r *fakeRecorder) Transferred(_ context.Context, ids []string, toSession string) error {
	r.transfers <- transferCall{ids: ids, toSession: toSession}
	return nil
}

func (r *fakeRecorder) log(id string) *memJobLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.logs[id]
}

func waitFinalize(t *testing.T, r *fakeRecorder) finalizeCall {
	t.Helper()
	select {
	case call := <-r.finalized:
		return call
	case <-time.After(10 * time.Second):
		t.Fatal("Finalize was not called")
		return finalizeCall{}
	}
}

func requireNoFinalize(t *testing.T, r *fakeRecorder, within time.Duration) {
	t.Helper()
	select {
	case call := <-r.finalized:
		t.Fatalf("unexpected Finalize: %+v", call)
	case <-time.After(within):
	}
}

func blockUntilCleanup(t *testing.T) <-chan struct{} {
	t.Helper()
	ch, _ := releaseOnCleanup(t)
	return ch
}

// releaseOnCleanup returns a channel for registerBlockingShell and a
// function that closes it; it is also closed when the test ends.
func releaseOnCleanup(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	ch := make(chan struct{})
	release := sync.OnceFunc(func() { close(ch) })
	t.Cleanup(release)
	return ch, release
}

func TestPublishRecorded_LogGetsOutputInOrder(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	ch, release := releaseOnCleanup(t)
	bs := registerBlockingShell(t, manager, ch)
	_, _ = bs.stdout.Write([]byte("before\n"))
	_, _ = bs.stderr.Write([]byte("warn\n"))

	id := publishShell(t, manager, bs, "s", OriginExplicit)
	require.Equal(t, "001", id)
	require.False(t, IsFallbackID(id))

	_, _ = bs.stdout.Write([]byte("after\n"))
	release()

	call := waitFinalize(t, rec)
	require.Equal(t, id, call.id)
	require.Equal(t, EndExited, call.endReason)
	require.True(t, call.info.Done)

	l := rec.log(id)
	require.True(t, l.closed.Load())
	require.Equal(t, "before\nafter\n", l.stdout.String())
	require.Equal(t, "warn\n", l.stderr.String())

	rec.mu.Lock()
	req := rec.reqs[0]
	rec.mu.Unlock()
	require.False(t, req.PrePublishLost)
	require.Equal(t, "s", req.Info.SessionID)
	require.Equal(t, OriginExplicit, req.Info.Origin)
	require.Equal(t, "blocked", req.Info.Command)
}

func TestPublishRecorded_BufferResetAfterPublicationKeepsLog(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	bs := registerBlockingShell(t, manager, blockUntilCleanup(t))
	id := publishShell(t, manager, bs, "s", OriginAuto)

	chunk := bytes.Repeat([]byte("a"), 1024*1024)
	for range 11 {
		_, _ = bs.stdout.Write(chunk)
	}
	require.Positive(t, bs.stdout.gen, "the in-memory buffer should have reset")
	require.Len(t, rec.log(id).stdout.String(), 11*len(chunk))
}

func TestPublishRecorded_PrePublishLoss(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	bs := registerBlockingShell(t, manager, blockUntilCleanup(t))
	_, _ = bs.stdout.Write(bytes.Repeat([]byte("a"), MaxBufferSize))
	_, _ = bs.stdout.Write([]byte("tail\n"))
	require.Positive(t, bs.stdout.gen)

	id := publishShell(t, manager, bs, "s", OriginAuto)

	rec.mu.Lock()
	req := rec.reqs[0]
	rec.mu.Unlock()
	require.True(t, req.PrePublishLost)

	logged := rec.log(id).stdout.String()
	require.True(t, strings.HasPrefix(logged, prePublishLostMarker))
	require.NotContains(t, logged, truncationMarker)
	require.True(t, strings.HasSuffix(logged, "tail\n"))
	require.Empty(t, rec.log(id).stderr.String())
}

func TestPublishRecorded_AllocateFailureFallsBack(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	rec.allocErr = errors.New("db down")
	manager.SetRecorder(rec)

	bs := startShell(t, manager, "sleep 0.2; echo still running")
	id := publishShell(t, manager, bs, "s", OriginExplicit)

	require.Regexp(t, `^M[0-9a-f]{4}-\d+$`, id)
	require.True(t, IsFallbackID(id))
	_, err := strconv.ParseInt(id, 16, 64)
	require.Error(t, err)

	got, found := manager.Get(id)
	require.True(t, found)
	require.Same(t, bs, got)

	waitCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, bs.WaitContext(waitCtx))
	stdout, _, _, _ := bs.GetOutput()
	require.Equal(t, "still running\n", stdout)
	requireNoFinalize(t, rec, 100*time.Millisecond)

	// The buffers keep working without a tee.
	require.Nil(t, bs.stdout.tee)
}

func TestPublishRecorded_FinalizeKilled(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	bs := startShell(t, manager, "sleep 30")
	id := publishShell(t, manager, bs, "s", OriginExplicit)
	require.NoError(t, manager.Kill(id))

	call := waitFinalize(t, rec)
	require.Equal(t, id, call.id)
	require.Equal(t, EndKilled, call.endReason)
}

func TestPublishRecorded_FinalizeAbandonedOnce(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	manager.gracePeriod = 50 * time.Millisecond
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	ch, release := releaseOnCleanup(t)
	bs := registerBlockingShell(t, manager, ch)
	id := publishShell(t, manager, bs, "s", OriginExplicit)

	require.ErrorIs(t, manager.Kill(id), ErrKillTimeout)
	call := waitFinalize(t, rec)
	require.Equal(t, EndAbandoned, call.endReason)
	require.False(t, call.info.Done)
	require.True(t, rec.log(id).closed.Load())

	// Late output from the abandoned shell is dropped by the closed log,
	// and its eventual exit does not finalize again.
	_, _ = bs.stdout.Write([]byte("late\n"))
	release()
	bs.Wait()
	requireNoFinalize(t, rec, 100*time.Millisecond)
}

func TestPublishRecorded_TransferRecorded(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)

	running := registerBlockingShell(t, manager, blockUntilCleanup(t))
	explicitID := publishShell(t, manager, running, "child", OriginExplicit)
	auto := registerBlockingShell(t, manager, blockUntilCleanup(t))
	publishShell(t, manager, auto, "child", OriginAuto)

	rec.allocErr = errors.New("db down")
	fallback := registerBlockingShell(t, manager, blockUntilCleanup(t))
	fallbackID := publishShell(t, manager, fallback, "child", OriginExplicit)
	require.True(t, IsFallbackID(fallbackID))

	handed, toKill := manager.Transfer("child", "parent")
	require.ElementsMatch(t, []string{explicitID, fallbackID}, jobIDs(handed))
	require.Len(t, toKill, 1)

	select {
	case call := <-rec.transfers:
		require.Equal(t, []string{explicitID}, call.ids)
		require.Equal(t, "parent", call.toSession)
	case <-time.After(5 * time.Second):
		t.Fatal("Transferred was not called")
	}
}

// fencedRecorder is a fakeRecorder that fails the test if Finalize is
// called after fence.
type fencedRecorder struct {
	*fakeRecorder
	t      *testing.T
	fenced atomic.Bool
}

func (r *fencedRecorder) Finalize(ctx context.Context, id string, info JobInfo, endReason string, stats LogStats) error {
	if r.fenced.Load() {
		r.t.Errorf("Finalize(%s, %s) called after the manager was closed", id, endReason)
	}
	return r.fakeRecorder.Finalize(ctx, id, info, endReason, stats)
}

// drainFinalized returns the Finalize calls made so far.
func drainFinalized(r *fakeRecorder) []finalizeCall {
	var calls []finalizeCall
	for {
		select {
		case call := <-r.finalized:
			calls = append(calls, call)
		default:
			return calls
		}
	}
}

func TestShutdown_ExitOnSignalRecordsAnvilExit(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)
	bs := startShell(t, manager, "sleep 30")
	id := publishShell(t, manager, bs, "s", OriginExplicit)

	manager.BeginShutdown()
	_, err := manager.Publish(t.Context(), startShell(t, manager, "sleep 30").ID(), PublishOptions{SessionID: "s", Origin: OriginExplicit})
	require.Error(t, err, "publication must be refused after BeginShutdown")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	exited, abandoned := manager.KillAll(ctx)
	require.Contains(t, exited, id)
	require.Empty(t, abandoned)

	// The job's own finalization either finished before Close (which
	// waits for it) or is left to the caller; either way it is
	// anvil_exit, and exactly once.
	unfinalized := manager.Close(ctx)
	calls := drainFinalized(rec)
	require.Equal(t, 1, len(unfinalized)+len(calls))
	if len(calls) == 1 {
		require.Equal(t, id, calls[0].id)
		require.Equal(t, EndAnvilExit, calls[0].endReason)
	} else {
		require.Equal(t, id, unfinalized[0].ID)
		require.Equal(t, EndAnvilExit, unfinalized[0].EndReason)
		require.True(t, unfinalized[0].Info.Done)
	}
	require.True(t, rec.log(id).closed.Load())
}

func TestShutdown_IgnoredCancellationRecordsAbandoned(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := &fencedRecorder{fakeRecorder: newFakeRecorder(), t: t}
	manager.SetRecorder(rec)
	ch, release := releaseOnCleanup(t)
	bs := registerBlockingShell(t, manager, ch)
	id := publishShell(t, manager, bs, "s", OriginExplicit)
	_, _ = bs.stdout.Write([]byte("before\n"))

	manager.BeginShutdown()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	exited, abandoned := manager.KillAll(ctx)
	require.Empty(t, exited)
	require.Equal(t, []string{id}, abandoned)

	unfinalized := manager.Close(context.Background())
	rec.fenced.Store(true)
	require.Len(t, unfinalized, 1)
	job := unfinalized[0]
	require.Equal(t, id, job.ID)
	require.Equal(t, EndAbandoned, job.EndReason)
	require.False(t, job.Info.Done)
	require.Equal(t, int64(len("before\n")), job.Stats.Bytes)
	require.Empty(t, drainFinalized(rec.fakeRecorder))

	log := rec.log(id)
	require.True(t, log.closed.Load())
	writes := log.stdout.writes.Load()

	// The abandoned shell finishes after shutdown: its output reaches
	// neither the closed log nor the recorder.
	_, _ = bs.stdout.Write([]byte("late\n"))
	release()
	bs.Wait()
	manager.finalize(bs)
	require.Equal(t, writes, log.stdout.writes.Load())
	require.Equal(t, "before\n", log.stdout.String())
	require.Empty(t, drainFinalized(rec.fakeRecorder))
	require.Empty(t, manager.Close(context.Background()))
}

func TestShutdown_ExitBeforeKillAllRecordsAnvilExit(t *testing.T) {
	t.Parallel()

	manager := newBackgroundShellManager()
	rec := newFakeRecorder()
	manager.SetRecorder(rec)
	ch, release := releaseOnCleanup(t)
	bs := registerBlockingShell(t, manager, ch)
	id := publishShell(t, manager, bs, "s", OriginExplicit)

	manager.BeginShutdown()
	release()
	call := waitFinalize(t, rec)
	require.Equal(t, id, call.id)
	require.Equal(t, EndAnvilExit, call.endReason)
	require.True(t, call.info.Done)

	exited, abandoned := manager.KillAll(t.Context())
	require.Empty(t, abandoned)
	require.NotContains(t, exited, id, "completed jobs are cleaned up, not killed")
	require.Empty(t, manager.Close(t.Context()))
	require.Empty(t, drainFinalized(rec))
}
