//go:build unix && !darwin && !android

package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clipboardSession sets up a desktop session whose only programs are the
// fake tools given, each running its script. $OUT names a file a script
// may write to; the session returns its path.
func clipboardSession(t *testing.T, wayland, x11 bool, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, script := range tools {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	}
	// The scripts need a shell and cat, but nothing else from the real
	// PATH, which may have a real wl-copy or xclip.
	bin := t.TempDir()
	for _, tool := range []string{"sh", "cat", "sleep"} {
		path, err := lookPathIn(tool, os.Getenv("PATH"))
		require.NoError(t, err)
		require.NoError(t, os.Symlink(path, filepath.Join(bin, tool)))
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+bin)
	t.Setenv("WAYLAND_DISPLAY", map[bool]string{true: "wayland-0"}[wayland])
	t.Setenv("DISPLAY", map[bool]string{true: ":0"}[x11])
	out := filepath.Join(dir, "clipboard")
	t.Setenv("OUT", out)
	return out
}

func lookPathIn(file, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		candidate := filepath.Join(dir, file)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

const recordClipboard = `{ echo "${0##*/} $*"; cat; } > "$OUT"`

func TestWriteClipboardImagePicksTheSessionsTool(t *testing.T) {
	both := map[string]string{"wl-copy": recordClipboard, "xclip": recordClipboard}
	tests := map[string]struct {
		wayland, x11 bool
		tools        map[string]string
		want         string
	}{
		"wayland":                 {wayland: true, x11: true, tools: both, want: "wl-copy --type image/png"},
		"wayland without wl-copy": {wayland: true, x11: true, tools: map[string]string{"xclip": recordClipboard}, want: "xclip -selection clipboard -t image/png -i"},
		"x11":                     {x11: true, tools: both, want: "xclip -selection clipboard -t image/png -i"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			out := clipboardSession(t, tc.wayland, tc.x11, tc.tools)
			require.NoError(t, writeClipboardImage([]byte("PNG")))
			got, err := os.ReadFile(out)
			require.NoError(t, err)
			require.Equal(t, tc.want+"\nPNG", string(got))
		})
	}
}

func TestWriteClipboardImageExplainsWhyItCant(t *testing.T) {
	tests := map[string]struct {
		wayland, x11 bool
		tools        map[string]string
		want         string
	}{
		"no tool":           {x11: true, want: "copying images needs wl-copy (wl-clipboard) or xclip"},
		"wayland, no tool":  {wayland: true, want: "copying images needs wl-copy (wl-clipboard) or xclip"},
		"no desktop":        {tools: map[string]string{"wl-copy": recordClipboard, "xclip": recordClipboard}, want: "copying images needs a desktop session (no DISPLAY or WAYLAND_DISPLAY)"},
		"tool fails":        {x11: true, tools: map[string]string{"xclip": "exit 1"}, want: "xclip: exit status 1"},
		"wayland tool only": {x11: true, tools: map[string]string{"wl-copy": recordClipboard}, want: "copying images needs wl-copy (wl-clipboard) or xclip"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			clipboardSession(t, tc.wayland, tc.x11, tc.tools)
			require.EqualError(t, writeClipboardImage([]byte("PNG")), tc.want)
		})
	}
}

func TestWriteClipboardImageNamesATool(t *testing.T) {
	clipboardSession(t, false, true, nil)
	require.ErrorIs(t, writeClipboardImage([]byte("PNG")), errNoImageClipboardTool)
}

func TestWriteClipboardImageReturnsWhileTheToolServesTheImage(t *testing.T) {
	out := clipboardSession(t, false, true, map[string]string{"xclip": `cat > "$OUT"; sleep 8 &`})
	start := time.Now()
	require.NoError(t, writeClipboardImage([]byte("PNG")))
	require.Less(t, time.Since(start), clipboardToolTimeout, "the child left serving the image doesn't hold it up")
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "PNG", string(got))
}

func TestWriteClipboardImageGivesUpOnAToolThatHangs(t *testing.T) {
	clipboardSession(t, false, true, map[string]string{"xclip": "exec sleep 30"})
	start := time.Now()
	err := writeClipboardImage([]byte("PNG"))
	require.EqualError(t, err, "xclip didn't take the image within 5s")
	require.Less(t, time.Since(start), clipboardToolTimeout+2*time.Second)
}
