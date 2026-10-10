package cmd

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestRunTerminalShowSpinner(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stderr  bool
		quiet   bool
		verbose bool
		want    bool
	}{
		"stderr terminal":        {stderr: true, want: true},
		"stderr not a terminal":  {stderr: false, want: false},
		"quiet":                  {stderr: true, quiet: true, want: false},
		"verbose":                {stderr: true, verbose: true, want: false},
		"quiet and verbose":      {stderr: true, quiet: true, verbose: true, want: false},
		"quiet without terminal": {stderr: false, quiet: true, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := runTerminal{stdin: true, stdout: true, stderr: tc.stderr}
			require.Equal(t, tc.want, rt.showSpinner(tc.quiet, tc.verbose))
		})
	}
}

func TestRunTerminalShowProgress(t *testing.T) {
	t.Parallel()

	on, off := true, false
	tests := map[string]struct {
		stderr   bool
		progress *bool
		want     bool
	}{
		"unset defaults on":           {stderr: true, progress: nil, want: true},
		"enabled":                     {stderr: true, progress: &on, want: true},
		"disabled":                    {stderr: true, progress: &off, want: false},
		"stderr not a terminal":       {stderr: false, progress: nil, want: false},
		"enabled without terminal":    {stderr: false, progress: &on, want: false},
		"disabled without a terminal": {stderr: false, progress: &off, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := runTerminal{stdin: true, stdout: true, stderr: tc.stderr}
			require.Equal(t, tc.want, rt.showProgress(tc.progress))
		})
	}
}

func TestRunTerminalCanDetectBackground(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stdin  bool
		stdout bool
		want   bool
	}{
		"both terminals": {stdin: true, stdout: true, want: true},
		"stdin only":     {stdin: true, stdout: false, want: false},
		"stdout only":    {stdin: false, stdout: true, want: false},
		"neither":        {stdin: false, stdout: false, want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rt := runTerminal{stdin: tc.stdin, stdout: tc.stdout, stderr: true}
			require.Equal(t, tc.want, rt.canDetectBackground())
		})
	}
}

// lockedBuffer is a strings.Builder safe for the progress goroutine and
// the test to share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStartProgressBar(t *testing.T) {
	t.Parallel()

	var out lockedBuffer
	stop := startProgressBar(&out)
	require.Equal(t, ansi.SetIndeterminateProgressBar, out.String())

	require.Eventually(t, func() bool {
		return strings.Count(out.String(), ansi.SetIndeterminateProgressBar) >= 2
	}, 5*progressRefresh, 50*time.Millisecond, "progress bar is redrawn while running")

	stop()
	got := out.String()
	require.True(t, strings.HasSuffix(got, ansi.ResetProgressBar), "stop resets the progress bar")
	require.NotContains(t, strings.TrimSuffix(got, ansi.ResetProgressBar), ansi.ResetProgressBar)
}
