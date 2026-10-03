package shell

import (
	"bytes"
	"context"
	"regexp"
	"slices"
	"time"
)

// waitCh returns a channel that is closed by the next write.
func (sb *syncBuffer) waitCh() <-chan struct{} {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.changed == nil {
		sb.changed = make(chan struct{})
	}
	return sb.changed
}

// readFrom returns the bytes after offset in generation gen. If the
// buffer has been reset since (gen differs or offset > Len), it
// returns the whole current buffer with reset=true.
func (sb *syncBuffer) readFrom(offset int, gen uint64) (data []byte, end int, curGen uint64, reset bool) {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	b := sb.buf.Bytes()
	if gen != sb.gen || offset > len(b) {
		return slices.Clone(b), len(b), sb.gen, true
	}
	return slices.Clone(b[offset:]), len(b), sb.gen, false
}

// lineStart returns the offset of the start of the line containing
// offset in generation gen, or 0 if the buffer has been reset since.
func (sb *syncBuffer) lineStart(offset int, gen uint64) int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	b := sb.buf.Bytes()
	if gen != sb.gen || offset > len(b) {
		return 0
	}
	return bytes.LastIndexByte(b[:offset], '\n') + 1
}

type streamPos struct {
	offset int
	gen    uint64
}

// ReadResult is the outcome of an incremental read.
type ReadResult struct {
	Stdout      string
	Stderr      string
	BufferReset bool // A cap reset dropped output since the last read.
	HadPrevious bool // Output was consumed by an earlier read.
	Done        bool
	ExitErr     error
}

// ReadIncremental returns output written since the previous
// ReadIncremental call and advances the cursor to the end. With full,
// it returns the whole current buffer. GetOutput never moves the
// cursor.
func (bs *BackgroundShell) ReadIncremental(full bool) ReadResult {
	bs.readMu.Lock()
	defer bs.readMu.Unlock()

	// Checking done first guarantees a read that reports done has seen
	// every write, since the shell finishes writing before closing done.
	var res ReadResult
	if bs.IsDone() {
		res.Done = true
		res.ExitErr = bs.exitErr
	}
	res.HadPrevious = bs.stdoutPos.offset != 0 || bs.stderrPos.offset != 0

	read := func(sb *syncBuffer, pos *streamPos) string {
		offset := pos.offset
		if full {
			offset = 0
		}
		data, end, gen, reset := sb.readFrom(offset, pos.gen)
		if reset {
			res.BufferReset = true
		}
		*pos = streamPos{offset: end, gen: gen}
		return string(data)
	}
	res.Stdout = read(bs.stdout, &bs.stdoutPos)
	res.Stderr = read(bs.stderr, &bs.stderrPos)
	return res
}

// LineMatcher finds the first output line matching a regex. Each
// stream starts at the beginning of the line containing the shell's
// read cursor (the byte after the last newline before it), so output
// already returned by job_output is not re-matched, a line whose first
// half was already read is still matched whole, and output that
// arrived before the matcher was created is matched.
type LineMatcher struct {
	re      *regexp.Regexp
	streams [2]matchStream // 0 = stdout, 1 = stderr.
}

type matchStream struct {
	pos     streamPos
	partial []byte
}

// NewLineMatcher creates a matcher positioned at the start of the line
// containing each stream's read cursor.
func (bs *BackgroundShell) NewLineMatcher(re *regexp.Regexp) *LineMatcher {
	bs.readMu.Lock()
	defer bs.readMu.Unlock()

	lm := &LineMatcher{re: re}
	for i, s := range []struct {
		sb  *syncBuffer
		pos streamPos
	}{{bs.stdout, bs.stdoutPos}, {bs.stderr, bs.stderrPos}} {
		lm.streams[i].pos = streamPos{
			offset: s.sb.lineStart(s.pos.offset, s.pos.gen),
			gen:    s.pos.gen,
		}
	}
	return lm
}

// Scan consumes new output from both streams and returns the first
// complete line that matches. With atEOF, a trailing partial line is
// matched too. On a buffer reset, the stream restarts at offset 0 of
// the new generation and its partial line is discarded.
func (lm *LineMatcher) Scan(bs *BackgroundShell, atEOF bool) (line string, ok bool) {
	for i, sb := range []*syncBuffer{bs.stdout, bs.stderr} {
		if line, ok := lm.streams[i].scan(lm.re, sb, atEOF); ok {
			return line, true
		}
	}
	return "", false
}

func (ms *matchStream) scan(re *regexp.Regexp, sb *syncBuffer, atEOF bool) (string, bool) {
	data, end, gen, reset := sb.readFrom(ms.pos.offset, ms.pos.gen)
	if reset {
		ms.partial = nil
	}
	ms.pos = streamPos{offset: end, gen: gen}
	ms.partial = append(ms.partial, data...)

	for {
		idx := bytes.IndexByte(ms.partial, '\n')
		if idx < 0 {
			break
		}
		line := bytes.TrimSuffix(ms.partial[:idx], []byte("\r"))
		ms.partial = ms.partial[idx+1:]
		if re.Match(line) {
			return string(line), true
		}
	}
	if atEOF && len(ms.partial) > 0 {
		line := bytes.TrimSuffix(ms.partial, []byte("\r"))
		ms.partial = nil
		if re.Match(line) {
			return string(line), true
		}
	}
	return "", false
}

// WaitReason says why WaitFor returned.
type WaitReason string

const (
	WaitCompleted WaitReason = "completed"
	WaitMatched   WaitReason = "matched"
	WaitTimedOut  WaitReason = "timed_out"
	WaitCanceled  WaitReason = "canceled"
)

// WaitFor blocks until the job completes, matcher (if non-nil) finds
// a line, timeout elapses, or ctx is cancelled.
func (bs *BackgroundShell) WaitFor(ctx context.Context, timeout time.Duration, matcher *LineMatcher) (WaitReason, string) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		// Taking the channels before scanning means a write landing
		// during the scan still wakes the loop.
		var stdoutCh, stderrCh <-chan struct{}
		if matcher != nil {
			stdoutCh = bs.stdout.waitCh()
			stderrCh = bs.stderr.waitCh()
			if line, ok := matcher.Scan(bs, false); ok {
				return WaitMatched, line
			}
		}

		select {
		case <-bs.done:
			if matcher != nil {
				if line, ok := matcher.Scan(bs, true); ok {
					return WaitMatched, line
				}
			}
			return WaitCompleted, ""
		case <-stdoutCh:
		case <-stderrCh:
		case <-timer.C:
			return WaitTimedOut, ""
		case <-ctx.Done():
			return WaitCanceled, ""
		}
	}
}
