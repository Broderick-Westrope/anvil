package shell

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/csync"
)

const (
	// MaxBackgroundJobs is the maximum number of concurrent background jobs allowed.
	MaxBackgroundJobs = 50
	// CompletedJobRetentionMinutes is how long to keep completed jobs
	// before auto-cleanup (30 minutes).
	CompletedJobRetentionMinutes = 30
	// MaxBufferSize is the maximum size in bytes for a syncBuffer
	// (10 MB). Writes that would exceed this cap cause the buffer to be
	// reset and only the tail of the new data is retained.
	MaxBufferSize = 10 * 1024 * 1024
	// KillGracePeriod bounds how long Kill waits for a cancelled shell's
	// goroutine to exit. With the process-group exec handler in place,
	// the goroutine should unwind almost immediately on cancellation;
	// this is a safety net for the pathological cases (children in
	// uninterruptible sleep, a wedge inside the interpreter) so a tool
	// call can never block indefinitely on job_kill.
	KillGracePeriod = 5 * time.Second
)

// ErrKillTimeout is returned by Kill when the shell did not exit within
// the grace period and was abandoned.
var ErrKillTimeout = errors.New("background shell did not exit within grace period")

// JobOrigin records how a published job was started.
type JobOrigin string

const (
	// OriginExplicit is a job started with run_in_background=true.
	OriginExplicit JobOrigin = "explicit"
	// OriginAuto is a foreground command moved to the background after
	// the auto-background threshold.
	OriginAuto JobOrigin = "auto"
)

// syncBuffer is a thread-safe wrapper around bytes.Buffer.
type syncBuffer struct {
	buf     bytes.Buffer
	mu      sync.RWMutex
	gen     uint64        // Incremented each time the cap resets the buffer.
	changed chan struct{} // Closed and replaced on every write.
	// onWrite, if set, is called after every write outside the lock.
	onWrite func()
}

const truncationMarker = "[output truncated — exceeded 10MB buffer cap]\n"

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	n, err = sb.write(p)
	if sb.onWrite != nil {
		sb.onWrite()
	}
	return n, err
}

func (sb *syncBuffer) write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	defer sb.signalLocked()

	if sb.buf.Len()+len(p) <= MaxBufferSize {
		return sb.buf.Write(p)
	}

	// Cap exceeded — reset and keep only the tail. Report all bytes
	// as consumed so callers (the shell interpreter) never retry.
	inputLen := len(p)
	sb.gen++
	sb.buf.Reset()
	sb.buf.WriteString(truncationMarker)

	available := MaxBufferSize - len(truncationMarker)
	if len(p) > available {
		p = p[len(p)-available:]
	}
	sb.buf.Write(p)
	return inputLen, nil
}

// signalLocked wakes everyone waiting on the current change channel.
// The caller must hold sb.mu for writing.
func (sb *syncBuffer) signalLocked() {
	if sb.changed != nil {
		close(sb.changed)
		sb.changed = make(chan struct{})
	}
}

// WriteString delegates to Write so the buffer cap is enforced.
func (sb *syncBuffer) WriteString(s string) (n int, err error) {
	return sb.Write([]byte(s))
}

// Len returns the current size of the buffer.
func (sb *syncBuffer) Len() int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.buf.Len()
}

func (sb *syncBuffer) String() string {
	sb.mu.RLock()
	defer sb.mu.RUnlock()
	return sb.buf.String()
}

// BackgroundShell represents a shell running in the background.
type BackgroundShell struct {
	Command     string
	Description string
	Shell       *Shell
	WorkingDir  string

	mu        sync.Mutex
	id        string
	sessionID string
	origin    JobOrigin
	published bool

	startedAt    time.Time
	lastOutputAt atomic.Int64 // Unix nanoseconds; 0 if nothing written.

	// readMu guards the incremental read cursor.
	readMu    sync.Mutex
	stdoutPos streamPos
	stderrPos streamPos

	ctx         context.Context
	cancel      context.CancelFunc
	stdout      *syncBuffer
	stderr      *syncBuffer
	done        chan struct{}
	exitErr     error
	completedAt atomic.Int64 // Unix nanoseconds; 0 while running.
}

// ID returns the shell's current key in the manager. Unpublished
// executions have an internal key; published jobs have a job ID.
func (bs *BackgroundShell) ID() string {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.id
}

// JobInfo is a point-in-time snapshot of a published background job.
type JobInfo struct {
	ID           string
	SessionID    string
	Origin       JobOrigin
	Command      string
	Description  string
	WorkingDir   string
	StartedAt    time.Time
	CompletedAt  time.Time // Zero while running.
	LastOutputAt time.Time // Zero if the job has printed nothing.
	Done         bool
	ExitCode     int // Only meaningful when Done.
}

// Info returns a snapshot of the shell's metadata and state.
func (bs *BackgroundShell) Info() JobInfo {
	bs.mu.Lock()
	info := JobInfo{
		ID:          bs.id,
		SessionID:   bs.sessionID,
		Origin:      bs.origin,
		Command:     bs.Command,
		Description: bs.Description,
		WorkingDir:  bs.WorkingDir,
		StartedAt:   bs.startedAt,
	}
	bs.mu.Unlock()

	if n := bs.lastOutputAt.Load(); n > 0 {
		info.LastOutputAt = time.Unix(0, n)
	}
	if bs.IsDone() {
		info.Done = true
		info.ExitCode = ExitCode(bs.exitErr)
		info.CompletedAt = time.Unix(0, bs.completedAt.Load())
	}
	return info
}

func (bs *BackgroundShell) isPublished() bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.published
}

// IDAllocator issues job IDs for published background jobs.
type IDAllocator interface {
	NextID(ctx context.Context) (string, error)
}

type counterAllocator struct{ next atomic.Uint64 }

func (c *counterAllocator) NextID(context.Context) (string, error) {
	return fmt.Sprintf("%03X", c.next.Add(1)), nil
}

// BackgroundShellManager manages background shell instances.
type BackgroundShellManager struct {
	// mu serialises compound operations on the shell map: adding,
	// removing, re-keying, and snapshotting entries.
	mu        sync.Mutex
	shells    *csync.Map[string, *BackgroundShell]
	aliases   map[string]string
	allocator IDAllocator

	gracePeriod time.Duration
}

var (
	backgroundManager     *BackgroundShellManager
	backgroundManagerOnce sync.Once
	runCounter            atomic.Uint64
)

// newBackgroundShellManager creates a new BackgroundShellManager instance.
func newBackgroundShellManager() *BackgroundShellManager {
	return &BackgroundShellManager{
		shells:      csync.NewMap[string, *BackgroundShell](),
		aliases:     make(map[string]string),
		allocator:   &counterAllocator{},
		gracePeriod: KillGracePeriod,
	}
}

// GetBackgroundShellManager returns the singleton background shell manager.
func GetBackgroundShellManager() *BackgroundShellManager {
	backgroundManagerOnce.Do(func() {
		backgroundManager = newBackgroundShellManager()
	})
	return backgroundManager
}

// SetIDAllocator replaces the allocator used to issue job IDs on
// publication.
func (m *BackgroundShellManager) SetIDAllocator(a IDAllocator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allocator = a
}

// Start creates and starts a new background shell with the given command.
// The shell is keyed by an internal key until it is published.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, command string, description string) (*BackgroundShell, error) {
	// Check job limit
	if m.shells.Len() >= MaxBackgroundJobs {
		return nil, fmt.Errorf("maximum number of background jobs (%d) reached. Please terminate or wait for some jobs to complete", MaxBackgroundJobs)
	}

	key := fmt.Sprintf("run-%d", runCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		BlockFuncs: blockFuncs,
	})

	shellCtx, cancel := context.WithCancel(ctx)

	bgShell := &BackgroundShell{
		id:          key,
		Command:     command,
		Description: description,
		WorkingDir:  workingDir,
		Shell:       shell,
		startedAt:   time.Now(),
		ctx:         shellCtx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}
	onWrite := func() { bgShell.lastOutputAt.Store(time.Now().UnixNano()) }
	bgShell.stdout = &syncBuffer{onWrite: onWrite}
	bgShell.stderr = &syncBuffer{onWrite: onWrite}

	m.mu.Lock()
	m.shells.Set(key, bgShell)
	m.mu.Unlock()

	go func() {
		defer close(bgShell.done)

		err := shell.ExecStream(shellCtx, command, bgShell.stdout, bgShell.stderr)

		bgShell.exitErr = err
		bgShell.completedAt.Store(time.Now().UnixNano())
	}()

	return bgShell, nil
}

// PublishOptions describes a shell being promoted to a background job.
type PublishOptions struct {
	SessionID string
	Origin    JobOrigin
}

// Publish promotes a running execution to a background job: it
// allocates a job ID, re-keys the shell under it, and records the
// owner and origin. It returns the new job ID.
func (m *BackgroundShellManager) Publish(ctx context.Context, key string, opts PublishOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key = m.resolveLocked(key)
	bs, ok := m.shells.Get(key)
	if !ok {
		return "", fmt.Errorf("background shell not found: %s", key)
	}
	if bs.isPublished() {
		return bs.ID(), nil
	}

	newID, err := m.allocator.NextID(ctx)
	if err != nil {
		return "", fmt.Errorf("allocating job ID: %w", err)
	}

	m.shells.Take(key)
	bs.mu.Lock()
	bs.id = newID
	bs.sessionID = opts.SessionID
	bs.origin = opts.Origin
	bs.published = true
	bs.mu.Unlock()
	m.shells.Set(newID, bs)
	m.aliases[key] = newID

	return newID, nil
}

// resolveLocked maps an internal key to its published job ID, if any.
// The caller must hold m.mu.
func (m *BackgroundShellManager) resolveLocked(key string) string {
	if id, ok := m.aliases[key]; ok {
		return id
	}
	return key
}

// takeLocked removes a shell and any aliases pointing at it. The caller
// must hold m.mu.
func (m *BackgroundShellManager) takeLocked(key string) (*BackgroundShell, bool) {
	key = m.resolveLocked(key)
	bs, ok := m.shells.Take(key)
	if !ok {
		return nil, false
	}
	for alias, target := range m.aliases {
		if target == key {
			delete(m.aliases, alias)
		}
	}
	return bs, true
}

// Get retrieves a background shell by ID or internal key.
func (m *BackgroundShellManager) Get(id string) (*BackgroundShell, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.shells.Get(m.resolveLocked(id))
}

// Remove removes a background shell from the manager without terminating it.
// This is useful when a shell has already completed and you just want to clean up tracking.
func (m *BackgroundShellManager) Remove(id string) error {
	m.mu.Lock()
	_, ok := m.takeLocked(id)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("background shell not found: %s", id)
	}
	return nil
}

// Kill terminates a background shell by ID. It cancels the shell's
// context and waits up to the grace period for the goroutine to unwind.
// In the steady state — where every external command runs in its own
// process group via the standard exec handler — cancellation reaches the
// underlying process(es) immediately and the wait returns in
// milliseconds. The grace-period fallback exists for the long tail:
// children stuck in uninterruptible kernel sleeps, a wedge somewhere in
// mvdan's interpreter, or a misbehaving builtin. In those cases we
// abandon the tracking entry and return [ErrKillTimeout] so callers
// (job_kill, KillAll) never block indefinitely; the orphan goroutine will
// exit on its own when its blocking syscall finally returns.
func (m *BackgroundShellManager) Kill(id string) error {
	m.mu.Lock()
	shell, ok := m.takeLocked(id)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("background shell not found: %s", id)
	}

	shell.cancel()
	select {
	case <-shell.done:
		return nil
	case <-time.After(m.gracePeriod):
		slog.Warn(
			"Background shell did not exit within grace period; abandoning",
			"id", id,
			"command", shell.Command,
			"grace_period", m.gracePeriod,
		)
		return ErrKillTimeout
	}
}

// List returns all background shell keys, published or not.
func (m *BackgroundShellManager) List() []string {
	ids := make([]string, 0, m.shells.Len())
	for id := range m.shells.Seq2() {
		ids = append(ids, id)
	}
	return ids
}

// ListBySession returns the published jobs owned by sessionID, running
// jobs first (oldest first), then finished jobs (newest first).
func (m *BackgroundShellManager) ListBySession(sessionID string) []JobInfo {
	return m.listJobs(func(info JobInfo) bool { return info.SessionID == sessionID })
}

// ListAll returns every published job, ordered as in [ListBySession].
func (m *BackgroundShellManager) ListAll() []JobInfo {
	return m.listJobs(func(JobInfo) bool { return true })
}

func (m *BackgroundShellManager) listJobs(keep func(JobInfo) bool) []JobInfo {
	m.mu.Lock()
	shells := slices.Collect(m.shells.Seq())
	m.mu.Unlock()

	var jobs []JobInfo
	for _, bs := range shells {
		if !bs.isPublished() {
			continue
		}
		info := bs.Info()
		if keep(info) {
			jobs = append(jobs, info)
		}
	}
	sortJobs(jobs)
	return jobs
}

func sortJobs(jobs []JobInfo) {
	slices.SortFunc(jobs, func(a, b JobInfo) int {
		switch {
		case !a.Done && b.Done:
			return -1
		case a.Done && !b.Done:
			return 1
		case !a.Done:
			return cmp.Or(a.StartedAt.Compare(b.StartedAt), cmp.Compare(a.ID, b.ID))
		default:
			return cmp.Or(b.CompletedAt.Compare(a.CompletedAt), cmp.Compare(a.ID, b.ID))
		}
	})
}

// Transfer moves ownership of fromSession's published jobs to
// toSession, except running auto jobs, whose IDs are returned in
// toKill for the caller to kill outside the manager lock.
func (m *BackgroundShellManager) Transfer(fromSession, toSession string) (handed []JobInfo, toKill []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, bs := range m.shells.Seq2() {
		bs.mu.Lock()
		owned := bs.published && bs.sessionID == fromSession
		kill := owned && bs.origin == OriginAuto && !bs.IsDone()
		if owned && !kill {
			bs.sessionID = toSession
		}
		bs.mu.Unlock()

		if !owned {
			continue
		}
		if kill {
			toKill = append(toKill, id)
			continue
		}
		handed = append(handed, bs.Info())
	}
	sortJobs(handed)
	slices.Sort(toKill)
	return handed, toKill
}

// Cleanup removes completed jobs that have been finished for more than the retention period
func (m *BackgroundShellManager) Cleanup() int {
	now := time.Now().UnixNano()
	retention := int64(CompletedJobRetentionMinutes * time.Minute)
	return m.removeWhere(func(bs *BackgroundShell) bool {
		completedAt := bs.completedAt.Load()
		return completedAt > 0 && now-completedAt > retention
	})
}

// CleanupCompleted removes all completed jobs regardless of age.
func (m *BackgroundShellManager) CleanupCompleted() int {
	return m.removeWhere(func(bs *BackgroundShell) bool {
		return bs.completedAt.Load() > 0
	})
}

func (m *BackgroundShellManager) removeWhere(match func(*BackgroundShell) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	var toRemove []string
	for key, bs := range m.shells.Seq2() {
		if match(bs) {
			toRemove = append(toRemove, key)
		}
	}
	for _, key := range toRemove {
		m.takeLocked(key)
	}
	return len(toRemove)
}

// KillAll terminates all background shells. The provided context bounds how
// long the function waits for each shell to exit.
func (m *BackgroundShellManager) KillAll(ctx context.Context) {
	m.CleanupCompleted()
	m.mu.Lock()
	shells := slices.Collect(m.shells.Seq())
	m.shells.Reset(map[string]*BackgroundShell{})
	clear(m.aliases)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, shell := range shells {
		wg.Go(func() {
			shell.cancel()
			select {
			case <-shell.done:
			case <-ctx.Done():
			}
		})
	}
	wg.Wait()
}

// GetOutput returns the current output of a background shell.
func (bs *BackgroundShell) GetOutput() (stdout string, stderr string, done bool, err error) {
	select {
	case <-bs.done:
		return bs.stdout.String(), bs.stderr.String(), true, bs.exitErr
	default:
		return bs.stdout.String(), bs.stderr.String(), false, nil
	}
}

// IsDone checks if the background shell has finished execution.
func (bs *BackgroundShell) IsDone() bool {
	select {
	case <-bs.done:
		return true
	default:
		return false
	}
}

// Wait blocks until the background shell completes.
func (bs *BackgroundShell) Wait() {
	<-bs.done
}

func (bs *BackgroundShell) WaitContext(ctx context.Context) bool {
	select {
	case <-bs.done:
		return true
	case <-ctx.Done():
		return false
	}
}
