package herdr

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// Status is the pane state Anvil reports to Herdr.
type Status string

const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked"
)

// State is one snapshot of the whole process, taken on the UI goroutine.
type State struct {
	Status       Status
	Message      string // Why the pane is blocked; only sent when blocked.
	SessionID    string // Displayed session; empty before one exists.
	SessionTitle string // Displayed session title; empty when untitled.
}

const (
	debounceDelay  = 150 * time.Millisecond
	retryDelay     = 2 * time.Second
	releaseTimeout = 2 * time.Second
)

// Reporter sends debounced state snapshots to Herdr from one goroutine,
// so at most one herdr child process runs at a time and the caller never
// blocks.
type Reporter struct {
	cfg      Config
	run      runner
	debounce time.Duration
	retry    time.Duration

	mu      sync.Mutex
	latest  State
	has     bool
	sawBusy bool // A working/blocked snapshot arrived since the last flush.

	wake   chan struct{} // Capacity 1.
	stop   chan struct{}
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once

	// Owned by the loop goroutine; Close touches them only after <-done.
	seq     seqGen
	sent    reportKey
	hasSent bool
}

type reportKey struct {
	status    Status
	message   string
	sessionID string
}

// Start begins reporting for cfg. startupEnv seeds the child env.
func Start(cfg Config, startupEnv []string) *Reporter {
	return start(cfg, newCLIRunner(cfg, startupEnv), time.Now, debounceDelay, retryDelay)
}

func start(cfg Config, run runner, now func() time.Time, debounce, retry time.Duration) *Reporter {
	ctx, cancel := context.WithCancel(context.Background())
	r := &Reporter{
		cfg:      cfg,
		run:      run,
		debounce: debounce,
		retry:    retry,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
		seq:      seqGen{now: now},
	}
	go r.loop()
	return r
}

// Update records the latest snapshot. It never blocks, and wakes the
// sender only when the snapshot changed, so frequent identical calls let
// the debounce settle.
func (r *Reporter) Update(s State) {
	r.mu.Lock()
	if s.Status != StatusIdle {
		r.sawBusy = true
	}
	if r.has && s == r.latest {
		r.mu.Unlock()
		return
	}
	r.latest = s
	r.has = true
	r.mu.Unlock()

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Close stops the sender, kills any in-flight report and releases agent
// authority. It is safe to call more than once.
func (r *Reporter) Close() {
	r.once.Do(func() {
		close(r.stop)
		r.cancel()
		<-r.done

		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()
		_, err := r.run.run(ctx, "pane", "release-agent", r.cfg.PaneID,
			"--source", Source, "--agent", Agent,
			"--seq", strconv.FormatInt(r.seq.next(), 10))
		if err != nil {
			slog.Debug("Herdr agent release failed", "error", err)
		}
	})
}

func (r *Reporter) loop() {
	defer close(r.done)

	var timer *time.Timer
	var fire <-chan time.Time
	arm := func(d time.Duration) {
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d)
		}
		fire = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	first := true
	for {
		select {
		case <-r.stop:
			return
		case <-r.wake:
			if !first {
				arm(r.debounce)
				continue
			}
			first = false
			if !r.flush() {
				arm(r.retry)
			}
		case <-fire:
			fire = nil
			if !r.flush() {
				arm(r.retry)
			}
		}
	}
}

// flush sends the latest snapshot if it differs from the last one sent.
// It returns false when a report failed and should be retried.
func (r *Reporter) flush() bool {
	if r.ctx.Err() != nil {
		return true
	}

	r.mu.Lock()
	s := r.latest
	sawBusy := r.sawBusy
	r.sawBusy = false
	r.mu.Unlock()

	fail := func() bool {
		if sawBusy {
			r.mu.Lock()
			r.sawBusy = true
			r.mu.Unlock()
		}
		return false
	}

	// Herdr marks a pane done only on an observed working to idle edge,
	// so a run that started and ended inside one debounce window still
	// reports working first.
	if s.Status == StatusIdle && sawBusy && r.hasSent && r.sent.status == StatusIdle {
		busy := s
		busy.Status = StatusWorking
		if !r.report(busy) {
			return fail()
		}
	}
	if !r.report(s) {
		return fail()
	}
	return true
}

// report sends s unless it matches the last report sent.
func (r *Reporter) report(s State) bool {
	key := reportKey{status: s.Status, sessionID: s.SessionID}
	if s.Status == StatusBlocked {
		key.message = s.Message
	}
	if r.hasSent && key == r.sent {
		return true
	}

	args := []string{
		"pane", "report-agent", r.cfg.PaneID,
		"--source", Source, "--agent", Agent,
		"--state", string(s.Status),
		"--seq", strconv.FormatInt(r.seq.next(), 10),
	}
	if key.message != "" {
		args = append(args, "--message", key.message)
	}
	if s.SessionID != "" {
		args = append(args, "--agent-session-id", s.SessionID)
	}
	if _, err := r.run.run(r.ctx, args...); err != nil {
		slog.Debug("Herdr state report failed", "error", err)
		return false
	}
	r.sent = key
	r.hasSent = true
	return true
}
