package shell

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newReadTestShell(t *testing.T) *BackgroundShell {
	t.Helper()
	return startShell(t, newBackgroundShellManager(), "sleep 30")
}

func write(t *testing.T, sb *syncBuffer, s string) {
	t.Helper()
	_, err := sb.WriteString(s)
	require.NoError(t, err)
}

func TestBackgroundShell_ReadIncremental(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	res := bs.ReadIncremental(false)
	require.Equal(t, "a\n", res.Stdout)
	require.False(t, res.HadPrevious)
	require.False(t, res.Done)

	res = bs.ReadIncremental(false)
	require.Empty(t, res.Stdout)
	require.Empty(t, res.Stderr)
	require.True(t, res.HadPrevious)

	write(t, bs.stdout, "b\n")
	write(t, bs.stderr, "e\n")
	res = bs.ReadIncremental(false)
	require.Equal(t, "b\n", res.Stdout)
	require.Equal(t, "e\n", res.Stderr)
	require.True(t, res.HadPrevious)
	require.False(t, res.BufferReset)
}

func TestBackgroundShell_GetOutputDoesNotMoveCursor(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	stdout, _, _, _ := bs.GetOutput()
	require.Equal(t, "a\n", stdout)

	res := bs.ReadIncremental(false)
	require.Equal(t, "a\n", res.Stdout)

	write(t, bs.stdout, "b\n")
	stdout, _, _, _ = bs.GetOutput()
	require.Equal(t, "a\nb\n", stdout)

	res = bs.ReadIncremental(false)
	require.Equal(t, "b\n", res.Stdout)
}

func TestBackgroundShell_ReadIncrementalFull(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "a\n")
	require.Equal(t, "a\n", bs.ReadIncremental(false).Stdout)

	write(t, bs.stdout, "b\n")
	res := bs.ReadIncremental(true)
	require.Equal(t, "a\nb\n", res.Stdout)
	require.True(t, res.HadPrevious)

	write(t, bs.stdout, "c\n")
	require.Equal(t, "c\n", bs.ReadIncremental(false).Stdout)
}

func TestBackgroundShell_ReadIncrementalBufferReset(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	write(t, bs.stdout, "first\n")
	require.Equal(t, "first\n", bs.ReadIncremental(false).Stdout)

	half := strings.Repeat("x", MaxBufferSize/2+1)
	write(t, bs.stdout, half)
	write(t, bs.stdout, half)

	var res ReadResult
	require.NotPanics(t, func() { res = bs.ReadIncremental(false) })
	require.True(t, res.BufferReset)
	require.Equal(t, bs.stdout.String(), res.Stdout)
	require.True(t, strings.HasPrefix(res.Stdout, truncationMarker))

	write(t, bs.stdout, "after\n")
	res = bs.ReadIncremental(false)
	require.False(t, res.BufferReset)
	require.Equal(t, "after\n", res.Stdout)
}

func TestLineMatcher_Scan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher)
	}{
		{
			name: "line already present",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "booting\nserver ready on :80\n")
				line, ok := lm().Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "server ready on :80", line)
			},
		},
		{
			name: "line split across two writes",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				m := lm()
				write(t, bs.stdout, "server rea")
				_, ok := m.Scan(bs, false)
				require.False(t, ok)
				write(t, bs.stdout, "dy\n")
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "server ready", line)
			},
		},
		{
			name: "final unterminated line only at EOF",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				m := lm()
				write(t, bs.stdout, "ready")
				_, ok := m.Scan(bs, false)
				require.False(t, ok)
				line, ok := m.Scan(bs, true)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
		{
			name: "match on stderr",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "nothing\n")
				write(t, bs.stderr, "warn: ready\n")
				line, ok := lm().Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "warn: ready", line)
			},
		},
		{
			name: "incremental read consumed half the line",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "old\nrea")
				require.Equal(t, "old\nrea", bs.ReadIncremental(false).Stdout)
				m := lm()
				write(t, bs.stdout, "dy\n")
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
		{
			name: "already read lines are not rematched",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "ready\n")
				bs.ReadIncremental(false)
				m := lm()
				_, ok := m.Scan(bs, true)
				require.False(t, ok)
			},
		},
		{
			name: "crlf endings",
			run: func(t *testing.T, bs *BackgroundShell, lm func() *LineMatcher) {
				write(t, bs.stdout, "ready\r\n")
				m := &LineMatcher{re: regexp.MustCompile(`^ready$`)}
				line, ok := m.Scan(bs, false)
				require.True(t, ok)
				require.Equal(t, "ready", line)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bs := newReadTestShell(t)
			re := regexp.MustCompile("ready")
			tt.run(t, bs, func() *LineMatcher { return bs.NewLineMatcher(re) })
		})
	}
}

func TestBackgroundShell_WaitForTimeout(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	start := time.Now()
	reason, _ := bs.WaitFor(t.Context(), 300*time.Millisecond, nil)
	elapsed := time.Since(start)

	require.Equal(t, WaitTimedOut, reason)
	require.GreaterOrEqual(t, elapsed, 300*time.Millisecond)
	require.Less(t, elapsed, 5*time.Second)
}

func TestBackgroundShell_WaitForCanceled(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reason, _ := bs.WaitFor(ctx, time.Minute, nil)
	require.Equal(t, WaitCanceled, reason)
}

func TestBackgroundShell_WaitForCompleted(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bs := registerBlockingShell(t, newBackgroundShellManager(), release)
	close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, _ := bs.WaitFor(ctx, time.Minute, bs.NewLineMatcher(regexp.MustCompile("ready")))
	require.Equal(t, WaitCompleted, reason)
}

func TestBackgroundShell_WaitForMatched(t *testing.T) {
	t.Parallel()

	bs := newReadTestShell(t)
	matcher := bs.NewLineMatcher(regexp.MustCompile("ready"))

	go func() {
		_, _ = bs.stdout.WriteString("starting\n")
		_, _ = bs.stdout.WriteString("listening, ready\n")
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, line := bs.WaitFor(ctx, time.Minute, matcher)
	require.Equal(t, WaitMatched, reason)
	require.Equal(t, "listening, ready", line)
}

func TestBackgroundShell_WaitForMatchedAtEOF(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	bs := registerBlockingShell(t, newBackgroundShellManager(), release)
	matcher := bs.NewLineMatcher(regexp.MustCompile("ready"))
	write(t, bs.stdout, "ready")
	close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	reason, line := bs.WaitFor(ctx, time.Minute, matcher)
	require.Equal(t, WaitMatched, reason)
	require.Equal(t, "ready", line)
}
