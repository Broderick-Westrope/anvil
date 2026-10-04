package shell

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"strings"
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
	// tee, if set, receives every write in full, before the cap is
	// applied. It must only touch memory: it is called under mu.
	tee io.Writer
	// capture, if set, holds the output a job log has not seen yet
	// while the job is being published.
	capture *publishCapture
}

// publishCapture is a buffer's retained output at the publication
// snapshot plus every write since, so the job log can be handed exactly
// the output it has not seen once it exists.
type publishCapture struct {
	snapshot  []byte // Retained output, without the truncation marker.
	lost      bool   // The cap reset the buffer before the snapshot.
	since     bytes.Buffer
	sinceLost bool // since hit the cap and stopped recording.
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

	if sb.tee != nil {
		_, _ = sb.tee.Write(p)
	}
	if c := sb.capture; c != nil && !c.sinceLost {
		if c.since.Len()+len(p) <= MaxBufferSize {
			c.since.Write(p)
		} else {
			c.sinceLost = true
		}
	}

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

// beginCapture snapshots the retained output and records every later
// write until [syncBuffer.attachLog] or [syncBuffer.endCapture]. It
// reports whether output was already lost to the cap.
func (sb *syncBuffer) beginCapture() bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	c := &publishCapture{lost: sb.gen > 0}
	retained := sb.buf.Bytes()
	if c.lost {
		retained = bytes.TrimPrefix(retained, []byte(truncationMarker))
	}
	c.snapshot = bytes.Clone(retained)
	sb.capture = c
	return c.lost
}

// attachLog writes the captured output to w and tees every later write
// into it, so w sees each write exactly once. w must only touch memory.
func (sb *syncBuffer) attachLog(w io.Writer) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	c := sb.capture
	sb.capture = nil
	if c != nil {
		if c.lost {
			writeSnapshot(w, []byte(prePublishLostMarker))
		}
		writeSnapshot(w, c.snapshot)
		writeSnapshot(w, c.since.Bytes())
		if c.sinceLost {
			writeSnapshot(w, []byte(publishLostMarker))
		}
	}
	sb.tee = w
}

// endCapture discards a capture that will not be attached to a log.
func (sb *syncBuffer) endCapture() {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.capture = nil
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
	// publishing is closed when an in-flight publication finishes; nil
	// when none is in flight.
	publishing chan struct{}
	events     *atomic.Pointer[sinkHolder] // Set on publication.
	endReason  string
	// persist is set on publication when the job was recorded, and is
	// immutable afterwards.
	persist *jobPersistence

	// Pattern watch state, guarded by mu. watchFired is the generation
	// of the last watch that emitted its match.
	watchGen    uint64
	watchFired  uint64
	watchCancel context.CancelFunc

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
	ExitCode     int // Only meaningful when [JobInfo.ExitedOnItsOwn].
	// EndReason is why the job ended, one of the End* constants, or ""
	// while running or when the job exited before a reason was recorded.
	EndReason string
}

// ExitedOnItsOwn reports whether a finished job ended without Anvil
// stopping it, so its exit code came from the command. Killed jobs
// report the interpreter's cancellation status instead, which says
// nothing about the command.
func (info JobInfo) ExitedOnItsOwn() bool {
	return info.Done && ExitCodeMeaningful(info.EndReason)
}

// ExitCodeMeaningful reports whether a job that ended for endReason has
// an exit code worth showing or recording.
func ExitCodeMeaningful(endReason string) bool {
	return endReason == "" || endReason == EndExited
}

// Info returns a snapshot of the shell's metadata and state.
func (bs *BackgroundShell) Info() JobInfo {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.infoLocked()
}

// infoLocked is [BackgroundShell.Info] for callers holding bs.mu.
func (bs *BackgroundShell) infoLocked() JobInfo {
	info := JobInfo{
		ID:          bs.id,
		SessionID:   bs.sessionID,
		Origin:      bs.origin,
		Command:     bs.Command,
		Description: bs.Description,
		WorkingDir:  bs.WorkingDir,
		StartedAt:   bs.startedAt,
		EndReason:   bs.endReason,
	}

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

// setEndReason records why the job ended if the current reason is
// from, so whoever decides first wins.
func (bs *BackgroundShell) setEndReason(from, to string) bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.endReason != from {
		return false
	}
	bs.endReason = to
	return true
}

func (bs *BackgroundShell) currentEndReason() string {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.endReason
}

func (bs *BackgroundShell) isPublished() bool {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.published
}

// eventSinkLocked returns the sink for a published job, or nil. The
// caller must hold bs.mu.
func (bs *BackgroundShell) eventSinkLocked() EventSink {
	if !bs.published || bs.events == nil {
		return nil
	}
	if h := bs.events.Load(); h != nil {
		return h.sink
	}
	return nil
}

// SetWatch replaces the job's pattern watch with matcher (nil clears
// it). The sink learns the new generation before the watch can fire,
// and a match is only emitted while its generation is still current.
// The watch fires at most once and ends when the job completes.
func (bs *BackgroundShell) SetWatch(matcher *LineMatcher) uint64 {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.watchGen++
	gen := bs.watchGen
	if bs.watchCancel != nil {
		bs.watchCancel()
		bs.watchCancel = nil
	}
	if sink := bs.eventSinkLocked(); sink != nil {
		sink.WatchReplaced(bs.id, gen)
	}
	if matcher == nil {
		return gen
	}

	watchCtx, cancel := context.WithCancel(bs.ctx)
	bs.watchCancel = cancel
	go func() {
		defer cancel()
		reason, line := bs.WaitFor(watchCtx, time.Duration(math.MaxInt64), matcher)
		if reason != WaitMatched {
			return
		}

		bs.mu.Lock()
		defer bs.mu.Unlock()
		if bs.watchGen != gen || bs.IsDone() {
			return
		}
		if sink := bs.eventSinkLocked(); sink != nil {
			bs.watchFired = gen
			sink.PatternMatched(bs.infoLocked(), gen, line)
		}
	}()
	return gen
}

// FiredWatchGen returns the current watch's generation if it has
// already emitted its match, and 0 otherwise.
func (bs *BackgroundShell) FiredWatchGen() uint64 {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	if bs.watchGen != 0 && bs.watchFired == bs.watchGen {
		return bs.watchGen
	}
	return 0
}

// EventSink receives events for published jobs. Implementations must
// only update memory and return immediately: they may be called with
// a BackgroundShell's mutex held.
type EventSink interface {
	JobCompleted(info JobInfo, tail string)
	WatchReplaced(jobID string, gen uint64)
	PatternMatched(info JobInfo, gen uint64, line string)
}

type sinkHolder struct{ sink EventSink }

// IDAllocator issues job IDs for published background jobs.
type IDAllocator interface {
	NextID(ctx context.Context) (string, error)
}

// End reasons recorded for persisted jobs.
const (
	EndExited      = "exited"
	EndKilled      = "killed"
	EndAbandoned   = "abandoned"
	EndAnvilExit   = "anvil_exit"
	EndInterrupted = "interrupted"
)

// AllocateRequest describes a job being published.
type AllocateRequest struct {
	Info           JobInfo
	PrePublishLost bool // The 10MB buffer reset before publication.
}

// JobRecorder persists published jobs. With a recorder set, Publish
// calls Allocate instead of the IDAllocator. Finalize records the end
// state; the DB guard makes repeat calls no-ops.
type JobRecorder interface {
	Allocate(ctx context.Context, req AllocateRequest) (id string, log JobLog, err error)
	Finalize(ctx context.Context, id string, info JobInfo, endReason string, stats LogStats) error
	Transferred(ctx context.Context, jobIDs []string, toSession string) error
}

// recorderTimeout bounds recorder calls made outside a caller's
// context.
const recorderTimeout = 5 * time.Second

// FallbackWarning is appended to bash responses for jobs that could not
// be persisted.
const FallbackWarning = "Warning: this job could not be saved and will not survive a restart."

var (
	instanceShort   = randomHex(2)
	fallbackCounter atomic.Uint64
)

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// fallbackID issues an in-memory-only job ID. The M prefix and dash
// mean it never parses as a hex job ID.
func fallbackID() string {
	return fmt.Sprintf("M%s-%d", instanceShort, fallbackCounter.Add(1))
}

// IsFallbackID reports whether id was issued because the job could not
// be persisted.
func IsFallbackID(id string) bool {
	return strings.HasPrefix(id, "M") && strings.Contains(id, "-")
}

// jobPersistence links a published job to its recorder and log.
type jobPersistence struct {
	recorder JobRecorder
	log      JobLog
	// claimed is set by whoever records the end state: the job's own
	// finalization while recording is open, or shutdown after it closed.
	claimed  atomic.Bool
	logOnce  sync.Once
	logStats LogStats
}

// closeLog closes the job's log once and returns its final stats.
func (p *jobPersistence) closeLog() LogStats {
	p.logOnce.Do(func() { p.logStats = p.log.Close() })
	return p.logStats
}

// closeLog stops teeing bs's output into p's log, then closes the log,
// so a closed log never receives another write.
func (bs *BackgroundShell) closeLog(p *jobPersistence) LogStats {
	for _, sb := range []*syncBuffer{bs.stdout, bs.stderr} {
		sb.mu.Lock()
		sb.tee = nil
		sb.mu.Unlock()
	}
	return p.closeLog()
}

// UnfinalizedJob is a persisted job whose end state was not recorded
// before [BackgroundShellManager.Close]. Its log is already closed.
type UnfinalizedJob struct {
	ID        string
	Info      JobInfo
	EndReason string // The reason decided so far; "" if none.
	Stats     LogStats
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
	sink      atomic.Pointer[sinkHolder]

	// recMu guards the recorder, admission of recorder calls (tracked by
	// recCalls so closing can wait for them), and the set of persisted
	// jobs whose end state is not yet recorded.
	recMu       sync.Mutex
	recorder    JobRecorder
	recClosed   bool
	recCalls    sync.WaitGroup
	unfinalized map[*jobPersistence]*BackgroundShell

	// shuttingDown refuses new publications; guarded by mu.
	shuttingDown bool
	// eventsClosed stops completion events from jobs that finish after
	// shutdown began.
	eventsClosed atomic.Bool

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
		unfinalized: make(map[*jobPersistence]*BackgroundShell),
		allocator:   &counterAllocator{},
		gracePeriod: KillGracePeriod,
	}
}

// NewBackgroundShellManager returns a manager independent of the one
// returned by [GetBackgroundShellManager], so tests in other packages
// can exercise shutdown without affecting the process-wide manager.
func NewBackgroundShellManager() *BackgroundShellManager {
	return newBackgroundShellManager()
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

// SetRecorder sets the recorder that persists published jobs. A nil
// recorder falls back to the IDAllocator.
func (m *BackgroundShellManager) SetRecorder(r JobRecorder) {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	m.recorder = r
}

// beginRecorderCall admits a recorder call unless recording is closed.
// Callers must call m.recCalls.Done when it returns true.
func (m *BackgroundShellManager) beginRecorderCall() bool {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	if m.recClosed {
		return false
	}
	m.recCalls.Add(1)
	return true
}

// acquireRecorder returns the current recorder with an admitted call,
// or nil. Callers must call m.recCalls.Done for a non-nil result.
func (m *BackgroundShellManager) acquireRecorder() JobRecorder {
	m.recMu.Lock()
	defer m.recMu.Unlock()
	if m.recClosed || m.recorder == nil {
		return nil
	}
	m.recCalls.Add(1)
	return m.recorder
}

// SetEventSink sets the sink that receives events for published jobs.
// A nil sink disables events.
func (m *BackgroundShellManager) SetEventSink(sink EventSink) {
	if sink == nil {
		m.sink.Store(nil)
		return
	}
	m.sink.Store(&sinkHolder{sink: sink})
}

// Start creates and starts a new background shell with the given command.
// The shell is keyed by an internal key until it is published. Extra env
// entries are appended to the process environment.
func (m *BackgroundShellManager) Start(ctx context.Context, workingDir string, blockFuncs []BlockFunc, command string, description string, env ...string) (*BackgroundShell, error) {
	// Check job limit
	if m.shells.Len() >= MaxBackgroundJobs {
		return nil, fmt.Errorf("maximum number of background jobs (%d) reached. Please terminate or wait for some jobs to complete", MaxBackgroundJobs)
	}

	key := fmt.Sprintf("run-%d", runCounter.Add(1))

	shell := NewShell(&Options{
		WorkingDir: workingDir,
		Env:        append(os.Environ(), env...),
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
// owner and origin. It returns the new job ID. With a recorder set,
// the job is persisted and its output streamed to a log; if that
// fails, the job runs in memory only under a fallback ID (see
// [IsFallbackID]). Allocation runs without the manager lock, so other
// jobs and the job's own output are never blocked on it.
func (m *BackgroundShellManager) Publish(ctx context.Context, key string, opts PublishOptions) (string, error) {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		return "", errors.New("anvil is shutting down")
	}
	key = m.resolveLocked(key)
	bs, ok := m.shells.Get(key)
	if !ok {
		m.mu.Unlock()
		return "", fmt.Errorf("background shell not found: %s", key)
	}
	bs.mu.Lock()
	published, id, inFlight := bs.published, bs.id, bs.publishing
	if !published && inFlight == nil {
		bs.publishing = make(chan struct{})
	}
	bs.mu.Unlock()
	allocator := m.allocator
	m.mu.Unlock()

	switch {
	case published:
		return id, nil
	case inFlight != nil:
		select {
		case <-inFlight:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		bs.mu.Lock()
		published, id = bs.published, bs.id
		bs.mu.Unlock()
		if published {
			return id, nil
		}
		return m.Publish(ctx, key, opts)
	}

	rec := m.acquireRecorder()
	if rec == nil {
		newID, err := allocator.NextID(ctx)
		if err != nil {
			bs.mu.Lock()
			close(bs.publishing)
			bs.publishing = nil
			bs.mu.Unlock()
			return "", fmt.Errorf("allocating job ID: %w", err)
		}
		return m.install(key, bs, newID, nil, opts)
	}
	// The recorder call stays admitted until the job is registered as
	// unfinalized, so Close either waits for it or never sees it start.
	defer m.recCalls.Done()
	newID, persist, err := m.allocateRecorded(ctx, rec, bs, opts)
	if err != nil {
		newID = fallbackID()
		slog.Warn("Failed to persist background job; it will not survive a restart", "id", newID, "error", err)
	}
	return m.install(key, bs, newID, persist, opts)
}

// allocateRecorded persists bs and tees its output into a job log. The
// buffers capture output from the snapshot until the log is attached,
// so no write is lost or duplicated while the recorder runs unlocked.
func (m *BackgroundShellManager) allocateRecorded(ctx context.Context, rec JobRecorder, bs *BackgroundShell, opts PublishOptions) (string, *jobPersistence, error) {
	stdoutLost := bs.stdout.beginCapture()
	stderrLost := bs.stderr.beginCapture()
	req := AllocateRequest{
		Info: JobInfo{
			SessionID:   opts.SessionID,
			Origin:      opts.Origin,
			Command:     bs.Command,
			Description: bs.Description,
			WorkingDir:  bs.WorkingDir,
			StartedAt:   bs.startedAt,
		},
		PrePublishLost: stdoutLost || stderrLost,
	}

	ctx, cancel := context.WithTimeout(ctx, recorderTimeout)
	defer cancel()
	id, log, err := rec.Allocate(ctx, req)
	if err != nil {
		bs.stdout.endCapture()
		bs.stderr.endCapture()
		return "", nil, err
	}
	bs.stdout.attachLog(log.Stdout())
	bs.stderr.attachLog(log.Stderr())
	return id, &jobPersistence{recorder: rec, log: log}, nil
}

// install re-keys bs under newID once allocation has finished. If bs
// was killed or removed meanwhile it stays out of the manager and
// Publish fails, but a persisted job is still finalized so its record
// never stays running.
func (m *BackgroundShellManager) install(key string, bs *BackgroundShell, newID string, persist *jobPersistence, opts PublishOptions) (string, error) {
	m.mu.Lock()
	cur, ok := m.shells.Get(key)
	tracked := ok && cur == bs
	if tracked {
		m.shells.Take(key)
		m.shells.Set(newID, bs)
		m.aliases[key] = newID
	}

	bs.mu.Lock()
	if tracked || persist != nil {
		bs.id = newID
		bs.sessionID = opts.SessionID
		bs.origin = opts.Origin
		bs.published = true
		bs.events = &m.sink
		bs.persist = persist
	}
	close(bs.publishing)
	bs.publishing = nil
	bs.mu.Unlock()

	// Mirror BeginShutdown and KillAll for a job they saw unpublished.
	if m.shuttingDown && (!tracked || !bs.IsDone()) {
		bs.setEndReason("", EndAnvilExit)
	}
	if persist != nil {
		m.recMu.Lock()
		m.unfinalized[persist] = bs
		m.recMu.Unlock()
	}
	m.mu.Unlock()

	if tracked && m.sink.Load() != nil {
		go func() {
			<-bs.done
			if m.eventsClosed.Load() {
				return
			}
			if h := m.sink.Load(); h != nil {
				stdout, stderr, _, _ := bs.GetOutput()
				h.sink.JobCompleted(bs.Info(), jobTail(stdout, stderr))
			}
		}()
	}
	if persist != nil {
		// A Kill that gave up on bs before persist was installed could
		// not finalize it.
		if bs.currentEndReason() == EndAbandoned {
			m.finalize(bs)
		}
		go func() {
			<-bs.done
			bs.setEndReason("", EndExited)
			m.finalize(bs)
		}()
	}

	if !tracked {
		return "", fmt.Errorf("background job %s was stopped while being published", newID)
	}
	return newID, nil
}

// finalize closes a persisted job's log and records its end state,
// once. While recording is closed, shutdown finalizes instead.
func (m *BackgroundShellManager) finalize(bs *BackgroundShell) {
	bs.mu.Lock()
	p, id := bs.persist, bs.id
	bs.mu.Unlock()
	if p == nil {
		return
	}
	if !m.beginRecorderCall() {
		return
	}
	defer m.recCalls.Done()
	if !m.claimFinalization(p) {
		return
	}

	stats := bs.closeLog(p)
	ctx, cancel := context.WithTimeout(context.Background(), recorderTimeout)
	defer cancel()
	if err := p.recorder.Finalize(ctx, id, bs.Info(), bs.currentEndReason(), stats); err != nil {
		slog.Warn("Failed to record background job end state", "id", id, "error", err)
	}
}

// claimFinalization reports whether the caller is the one to record p's
// end state.
func (m *BackgroundShellManager) claimFinalization(p *jobPersistence) bool {
	if !p.claimed.CompareAndSwap(false, true) {
		return false
	}
	m.recMu.Lock()
	delete(m.unfinalized, p)
	m.recMu.Unlock()
	return true
}

// jobTail returns the last ten lines of stdout followed by stderr.
func jobTail(stdout, stderr string) string {
	var parts []string
	for _, part := range []string{stdout, stderr} {
		if part = strings.TrimRight(part, "\n"); part != "" {
			parts = append(parts, part)
		}
	}
	return LastLines(strings.Join(parts, "\n"), 10)
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

	shell.setEndReason("", EndKilled)
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
		if shell.setEndReason(EndKilled, EndAbandoned) {
			m.finalize(shell)
		}
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
	slices.SortFunc(jobs, CompareJobs)
}

// CompareJobs orders jobs as [BackgroundShellManager.ListBySession]
// does: running jobs first (oldest first), then finished jobs (newest
// first).
func CompareJobs(a, b JobInfo) int {
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
}

// Transfer moves ownership of fromSession's published jobs to
// toSession, except running auto jobs, whose IDs are returned in
// toKill for the caller to kill outside the manager lock.
func (m *BackgroundShellManager) Transfer(fromSession, toSession string) (handed []JobInfo, toKill []string) {
	var persisted []string
	m.mu.Lock()
	for id, bs := range m.shells.Seq2() {
		bs.mu.Lock()
		owned := bs.published && bs.sessionID == fromSession
		kill := owned && bs.origin == OriginAuto && !bs.IsDone()
		if owned && !kill {
			bs.sessionID = toSession
			if bs.persist != nil {
				persisted = append(persisted, id)
			}
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
	m.mu.Unlock()

	sortJobs(handed)
	slices.Sort(toKill)
	m.recordTransfer(persisted, toSession)
	return handed, toKill
}

func (m *BackgroundShellManager) recordTransfer(jobIDs []string, toSession string) {
	if len(jobIDs) == 0 {
		return
	}
	rec := m.acquireRecorder()
	if rec == nil {
		return
	}
	defer m.recCalls.Done()

	slices.Sort(jobIDs)
	ctx, cancel := context.WithTimeout(context.Background(), recorderTimeout)
	defer cancel()
	if err := rec.Transferred(ctx, jobIDs, toSession); err != nil {
		slog.Warn("Failed to record background job transfer", "ids", jobIDs, "session", toSession, "error", err)
	}
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

// BeginShutdown stops new publications and event emission, and sets
// every running published job's end reason to anvil_exit so an exit
// racing shutdown is recorded truthfully.
func (m *BackgroundShellManager) BeginShutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	shells := slices.Collect(m.shells.Seq())
	m.mu.Unlock()

	m.eventsClosed.Store(true)
	m.SetEventSink(nil)
	for _, bs := range shells {
		if bs.isPublished() && !bs.IsDone() {
			bs.setEndReason("", EndAnvilExit)
		}
	}
}

// KillAll terminates all background shells and reports, by ID, which
// exited before ctx expired and which were abandoned. Published jobs
// that had no end reason yet get anvil_exit, and abandoned ones
// abandoned.
func (m *BackgroundShellManager) KillAll(ctx context.Context) (exited, abandoned []string) {
	m.CleanupCompleted()
	m.mu.Lock()
	shells := slices.Collect(m.shells.Seq())
	m.shells.Reset(map[string]*BackgroundShell{})
	clear(m.aliases)
	m.mu.Unlock()

	var (
		wg      sync.WaitGroup
		resMu   sync.Mutex
		results = make(map[string]bool, len(shells))
	)
	for _, shell := range shells {
		wg.Go(func() {
			if shell.isPublished() {
				shell.setEndReason("", EndAnvilExit)
			}
			shell.cancel()
			ok := true
			select {
			case <-shell.done:
			case <-ctx.Done():
				ok = shell.IsDone()
			}
			if !ok && shell.isPublished() {
				if !shell.setEndReason(EndAnvilExit, EndAbandoned) {
					shell.setEndReason("", EndAbandoned)
				}
			}
			resMu.Lock()
			results[shell.ID()] = ok
			resMu.Unlock()
		})
	}
	wg.Wait()

	for id, ok := range results {
		if ok {
			exited = append(exited, id)
		} else {
			abandoned = append(abandoned, id)
		}
	}
	slices.Sort(exited)
	slices.Sort(abandoned)
	return exited, abandoned
}

// Close closes every job log and stops calling the recorder and event
// sink. Goroutines of abandoned shells that finish later drop their
// output and skip Finalize. It waits, bounded by ctx, for recorder calls
// already in flight, and returns the persisted jobs whose end state
// was not recorded, for the caller to finalize.
func (m *BackgroundShellManager) Close(ctx context.Context) []UnfinalizedJob {
	m.mu.Lock()
	m.shuttingDown = true
	m.mu.Unlock()
	m.eventsClosed.Store(true)
	m.SetEventSink(nil)

	m.recMu.Lock()
	m.recClosed = true
	m.recMu.Unlock()

	idle := make(chan struct{})
	go func() {
		m.recCalls.Wait()
		close(idle)
	}()
	select {
	case <-idle:
	case <-ctx.Done():
		slog.Warn("Timed out waiting for background job recording to finish")
	}

	m.recMu.Lock()
	pending := make(map[*jobPersistence]*BackgroundShell, len(m.unfinalized))
	maps.Copy(pending, m.unfinalized)
	m.recMu.Unlock()

	var jobs []UnfinalizedJob
	for p, bs := range pending {
		if !m.claimFinalization(p) {
			continue
		}
		jobs = append(jobs, UnfinalizedJob{
			ID:        bs.ID(),
			Info:      bs.Info(),
			EndReason: bs.currentEndReason(),
			Stats:     bs.closeLog(p),
		})
	}
	slices.SortFunc(jobs, func(a, b UnfinalizedJob) int { return cmp.Compare(a.ID, b.ID) })
	return jobs
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
