package shell

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

const (
	// MaxLogBytes caps each stream of a job log (50 MB).
	MaxLogBytes = 50 * 1024 * 1024

	logFlushInterval  = 2 * time.Second
	logFlushThreshold = 1024 * 1024
	logPendingLimit   = 8 * 1024 * 1024

	logCapMarker         = "[log truncated at 50MB]\n"
	prePublishLostMarker = "[output before publication lost: exceeded 10MB buffer cap]\n"
	publishLostMarker    = "[output during publication lost: exceeded 10MB buffer cap]\n"
)

// JobLog receives a published job's output. Writes only touch memory
// (see [NewJobLog]). Close flushes and returns final stats.
type JobLog interface {
	Stdout() io.Writer
	Stderr() io.Writer
	Close() LogStats
}

// LogStats summarises a closed job log.
type LogStats struct {
	Bytes      int64
	Truncated  bool   // Hit the 50MB cap or dropped data because the disk fell behind.
	WriteError string // First disk error, if any; writing stops after it.
}

// snapshotWriter is implemented by log streams that accept the
// retained buffer at publication without applying the slow-disk limit.
type snapshotWriter interface {
	WriteSnapshot(p []byte)
}

// writeSnapshot writes publication-time output to a log stream.
func writeSnapshot(w io.Writer, p []byte) {
	if sw, ok := w.(snapshotWriter); ok {
		sw.WriteSnapshot(p)
		return
	}
	_, _ = w.Write(p)
}

type logLimits struct {
	maxBytes       int64
	flushInterval  time.Duration
	flushThreshold int
	pendingLimit   int
}

var defaultLogLimits = logLimits{
	maxBytes:       MaxLogBytes,
	flushInterval:  logFlushInterval,
	flushThreshold: logFlushThreshold,
	pendingLimit:   logPendingLimit,
}

// fileLog is a [JobLog] whose writes are buffered in memory and written
// to disk by a single flusher goroutine, so a slow disk never blocks
// the job.
type fileLog struct {
	streams [2]*logStream // 0 = stdout, 1 = stderr.
	files   [2]io.Closer
	limits  logLimits

	kick chan struct{}
	stop chan struct{}
	done chan struct{}

	// Only the flusher goroutine, and Close after it exits, touch the
	// disk fields below.
	writeErr error

	closeOnce sync.Once
	stats     LogStats
}

type logStream struct {
	log *fileLog
	w   io.Writer

	mu        sync.Mutex
	pending   []byte
	exempt    int   // Leading pending bytes from the publication snapshot.
	accepted  int64 // Bytes accepted toward the cap.
	capped    bool
	truncated bool
	closed    bool

	written int64 // Disk side; see fileLog.
}

// NewJobLog creates (or truncates) the stdout and stderr log files.
// If the stderr file can't be opened, the stdout file is closed and
// removed.
func NewJobLog(stdoutPath, stderrPath string) (JobLog, error) {
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening job stdout log: %w", err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		stdout.Close()
		os.Remove(stdoutPath)
		return nil, fmt.Errorf("opening job stderr log: %w", err)
	}
	return newFileLog(stdout, stderr, defaultLogLimits), nil
}

func newFileLog(stdout, stderr io.WriteCloser, limits logLimits) *fileLog {
	l := &fileLog{
		files:  [2]io.Closer{stdout, stderr},
		limits: limits,
		kick:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	l.streams[0] = &logStream{log: l, w: stdout}
	l.streams[1] = &logStream{log: l, w: stderr}
	go l.flusher()
	return l
}

func (l *fileLog) Stdout() io.Writer { return l.streams[0] }
func (l *fileLog) Stderr() io.Writer { return l.streams[1] }

func (l *fileLog) flusher() {
	defer close(l.done)
	ticker := time.NewTicker(l.limits.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
		case <-l.kick:
		}
		l.flush()
	}
}

func (l *fileLog) flush() {
	for _, s := range l.streams {
		s.flush()
	}
}

// Close stops the flusher, writes what is pending, closes the files,
// and returns the final stats. Later writes are dropped.
func (l *fileLog) Close() LogStats {
	l.closeOnce.Do(func() {
		for _, s := range l.streams {
			s.mu.Lock()
			s.closed = true
			s.mu.Unlock()
		}
		close(l.stop)
		<-l.done
		l.flush()

		var closeErr error
		for _, f := range l.files {
			closeErr = errors.Join(closeErr, f.Close())
		}
		if l.writeErr == nil && closeErr != nil {
			l.recordError(closeErr)
		}

		for _, s := range l.streams {
			l.stats.Bytes += s.written
			l.stats.Truncated = l.stats.Truncated || s.capped || s.truncated
		}
		if l.writeErr != nil {
			l.stats.WriteError = l.writeErr.Error()
		}
	})
	return l.stats
}

func (l *fileLog) recordError(err error) {
	l.writeErr = err
	slog.Warn("Failed to write background job log; further output will not be saved", "error", err)
}

// Write buffers p in memory and returns immediately. It never fails.
func (s *logStream) Write(p []byte) (int, error) {
	s.append(p, false)
	return len(p), nil
}

// WriteSnapshot buffers publication-time output without applying the
// pending limit, so retained output is never dropped on the way in.
func (s *logStream) WriteSnapshot(p []byte) {
	s.append(p, true)
}

func (s *logStream) append(p []byte, snapshot bool) {
	limits := s.log.limits

	s.mu.Lock()
	if s.closed || s.capped || len(p) == 0 {
		s.mu.Unlock()
		return
	}
	if !snapshot && len(s.pending)-s.exempt+len(p) > limits.pendingLimit {
		s.truncated = true
		s.mu.Unlock()
		return
	}

	hitCap := false
	if remaining := limits.maxBytes - s.accepted; int64(len(p)) > remaining {
		p = p[:remaining]
		hitCap = true
	}
	s.pending = append(s.pending, p...)
	s.accepted += int64(len(p))
	if snapshot {
		s.exempt += len(p)
	}
	if hitCap {
		s.pending = append(s.pending, logCapMarker...)
		s.capped = true
	}
	kick := len(s.pending) >= limits.flushThreshold
	s.mu.Unlock()

	if kick {
		select {
		case s.log.kick <- struct{}{}:
		default:
		}
	}
}

// flush writes pending data to disk. Only the flusher goroutine, or
// Close after the flusher has exited, calls it.
func (s *logStream) flush() {
	s.mu.Lock()
	data := s.pending
	s.pending = nil
	s.exempt = 0
	s.mu.Unlock()

	if len(data) == 0 || s.log.writeErr != nil {
		return
	}
	n, err := s.w.Write(data)
	s.written += int64(n)
	if err == nil && n < len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.log.recordError(err)
	}
}
