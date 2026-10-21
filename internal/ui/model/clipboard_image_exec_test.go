//go:build unix && !darwin && !android

package model

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// desktop is the session a clipboard tool runs in.
type desktop struct {
	wayland, x11 string
	// tools are the clipboard tools installed, each a shell script.
	tools map[string]string
}

// enter sets up d as the session, with only its tools and what their
// scripts need on PATH, since the real PATH may have a real wl-copy or
// xclip. $OUT names a file a script may write to; enter returns its path.
func (d desktop) enter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, script := range d.tools {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	}
	bin := t.TempDir()
	for _, tool := range []string{"sh", "cat", "sleep"} {
		path, err := exec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+bin)
	t.Setenv("WAYLAND_DISPLAY", d.wayland)
	t.Setenv("DISPLAY", d.x11)
	out := filepath.Join(dir, "clipboard")
	t.Setenv("OUT", out)
	return out
}

const recordClipboard = `{ echo "${0##*/} $*"; cat; } > "$OUT"`

func TestWriteClipboardImagePicksTheSessionsTool(t *testing.T) {
	both := map[string]string{"wl-copy": recordClipboard, "xclip": recordClipboard}
	tests := map[string]struct {
		session desktop
		want    string
	}{
		"wayland":                 {session: desktop{wayland: "wayland-0", x11: ":0", tools: both}, want: "wl-copy --type image/png"},
		"wayland without wl-copy": {session: desktop{wayland: "wayland-0", x11: ":0", tools: map[string]string{"xclip": recordClipboard}}, want: "xclip -selection clipboard -t image/png -i"},
		"x11":                     {session: desktop{x11: ":0", tools: both}, want: "xclip -selection clipboard -t image/png -i"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out := tc.session.enter(t)
			require.NoError(t, imageClipboardReady())
			require.NoError(t, writeClipboardImage([]byte("PNG")))
			got, err := os.ReadFile(out)
			require.NoError(t, err)
			require.Equal(t, tc.want+"\nPNG", string(got))
		})
	}
}

func TestWriteClipboardImageExplainsWhyItCant(t *testing.T) {
	tests := map[string]struct {
		session desktop
		want    string
		wantIs  error
	}{
		"no tool": {
			session: desktop{x11: ":0"},
			want:    "no clipboard tool for images; install wl-clipboard or xclip", wantIs: errNoImageClipboardTool,
		},
		"wayland, no tool": {
			session: desktop{wayland: "wayland-0"},
			want:    "no clipboard tool for images; install wl-clipboard or xclip", wantIs: errNoImageClipboardTool,
		},
		"x11 with only wl-copy": {
			session: desktop{x11: ":0", tools: map[string]string{"wl-copy": recordClipboard}},
			want:    "no clipboard tool for images; install wl-clipboard or xclip", wantIs: errNoImageClipboardTool,
		},
		"wayland with only xclip": {
			session: desktop{wayland: "wayland-0", tools: map[string]string{"xclip": recordClipboard}},
			want:    "no clipboard tool for images; install wl-clipboard or xclip", wantIs: errNoImageClipboardTool,
		},
		"no desktop": {
			session: desktop{tools: map[string]string{"wl-copy": recordClipboard, "xclip": recordClipboard}},
			want:    "no desktop session (DISPLAY and WAYLAND_DISPLAY are unset)", wantIs: errNoDesktopSession,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.session.enter(t)
			require.EqualError(t, imageClipboardReady(), tc.want)
			err := writeClipboardImage([]byte("PNG"))
			require.EqualError(t, err, tc.want)
			require.ErrorIs(t, err, tc.wantIs)
		})
	}
}

func TestWriteClipboardImageReportsWhatTheToolSaid(t *testing.T) {
	tests := map[string]struct {
		script, want string
	}{
		"says why":       {script: "echo \"Error: Can't open display: :0\" >&2; echo more >&2; exit 1", want: "xclip: Error: Can't open display: :0"},
		"says nothing":   {script: "exit 1", want: "xclip: exit status 1"},
		"blank first":    {script: "echo >&2; echo ' late reason ' >&2; exit 2", want: "xclip: late reason"},
		"succeeds noisy": {script: "echo warning >&2; cat > /dev/null"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			desktop{x11: ":0", tools: map[string]string{"xclip": tc.script}}.enter(t)
			err := writeClipboardImage([]byte("PNG"))
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tc.want)
		})
	}
}

func TestWriteClipboardImageReturnsWhileTheToolServesTheImage(t *testing.T) {
	out := desktop{x11: ":0", tools: map[string]string{"xclip": `cat > "$OUT"; sleep 8 &`}}.enter(t)
	start := time.Now()
	require.NoError(t, writeClipboardImage([]byte("PNG")))
	require.Less(t, time.Since(start), clipboardToolTimeout, "the child left serving the image doesn't hold it up")
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "PNG", string(got))
}

func TestWriteClipboardImageGivesUpOnAToolThatHangs(t *testing.T) {
	desktop{x11: ":0", tools: map[string]string{"xclip": "exec sleep 30"}}.enter(t)
	start := time.Now()
	require.EqualError(t, writeClipboardImage([]byte("PNG")), "xclip didn't take the image within 5s")
	require.Less(t, time.Since(start), clipboardToolTimeout+2*time.Second)
}
