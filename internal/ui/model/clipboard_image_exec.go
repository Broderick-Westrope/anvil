//go:build unix && !darwin && !android

package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// clipboardToolTimeout bounds a clipboard tool that never takes the image.
const clipboardToolTimeout = 5 * time.Second

var (
	errNoImageClipboardTool = errors.New("copying images needs wl-copy (wl-clipboard) or xclip")
	errNoDesktopSession     = errors.New("copying images needs a desktop session (no DISPLAY or WAYLAND_DISPLAY)")
)

// writeClipboardImage hands the PNG to wl-copy under Wayland or xclip under
// X11. They stay behind to serve the image and do large transfers
// properly, which go-nativeclipboard's X11 clipboard doesn't.
func writeClipboardImage(png []byte) error {
	args, err := imageCopyCommand()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardToolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// Stdout and stderr stay unset: the child left serving the image would
	// inherit pipes, and Run would wait on them until something else is
	// copied.
	cmd.Stdin = bytes.NewReader(png)
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s didn't take the image within %s", args[0], clipboardToolTimeout)
		}
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}

func imageCopyCommand() ([]string, error) {
	wayland, x11 := os.Getenv("WAYLAND_DISPLAY") != "", os.Getenv("DISPLAY") != ""
	if !wayland && !x11 {
		return nil, errNoDesktopSession
	}
	if _, err := exec.LookPath("wl-copy"); err == nil && wayland {
		return []string{"wl-copy", "--type", "image/png"}, nil
	}
	if _, err := exec.LookPath("xclip"); err == nil && x11 {
		return []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-i"}, nil
	}
	return nil, errNoImageClipboardTool
}
