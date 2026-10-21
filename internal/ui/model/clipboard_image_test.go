package model

import (
	"errors"
	"os/exec"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageCopyCommand(t *testing.T) {
	t.Parallel()

	wlCopy := []string{"wl-copy", "--type", "image/png"}
	xclip := []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-i"}
	tests := map[string]struct {
		wayland bool
		tools   []string
		want    []string
	}{
		"wayland":                 {wayland: true, tools: []string{"wl-copy", "xclip"}, want: wlCopy},
		"wayland without wl-copy": {wayland: true, tools: []string{"xclip"}, want: xclip},
		"x11":                     {tools: []string{"wl-copy", "xclip"}, want: xclip},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			getenv := func(key string) string {
				if key == "WAYLAND_DISPLAY" && tc.wayland {
					return "wayland-0"
				}
				return ""
			}
			lookPath := func(file string) (string, error) {
				if slices.Contains(tc.tools, file) {
					return "/usr/bin/" + file, nil
				}
				return "", exec.ErrNotFound
			}
			got, err := imageCopyCommand(getenv, lookPath)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestImageCopyCommandWithoutATool(t *testing.T) {
	t.Parallel()

	for name, wayland := range map[string]string{"wayland": "wayland-0", "x11": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			getenv := func(string) string { return wayland }
			lookPath := func(string) (string, error) { return "", exec.ErrNotFound }
			_, err := imageCopyCommand(getenv, lookPath)
			require.True(t, errors.Is(err, errNoImageClipboardTool))
			require.EqualError(t, err, "copying images needs wl-copy (wl-clipboard) or xclip")
		})
	}
}
