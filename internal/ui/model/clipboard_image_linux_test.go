//go:build linux && !android

package model

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeXclip puts an xclip on PATH that runs script, with $OUT naming a
// file it may write to, and returns that file.
func fakeXclip(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "clipboard.png")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "xclip"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("OUT", out)
	return out
}

func TestWriteClipboardImageHandsThePNGToTheTool(t *testing.T) {
	out := fakeXclip(t, `cat > "$OUT"`)
	require.NoError(t, writeClipboardImage([]byte("PNG")))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	require.Equal(t, "PNG", string(got))
}

func TestWriteClipboardImageReportsAFailingTool(t *testing.T) {
	fakeXclip(t, "exit 1")
	require.EqualError(t, writeClipboardImage([]byte("PNG")), "xclip: exit status 1")
}

func TestWriteClipboardImageWithoutATool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "")
	require.ErrorIs(t, writeClipboardImage([]byte("PNG")), errNoImageClipboardTool)
}

func TestWriteClipboardImageReturnsWhileTheToolServesTheImage(t *testing.T) {
	fakeXclip(t, `cat > "$OUT"; sleep 30 &`)
	start := time.Now()
	require.NoError(t, writeClipboardImage([]byte("PNG")))
	require.Less(t, time.Since(start), clipboardToolTimeout, "the child left serving the image doesn't hold it up")
}
