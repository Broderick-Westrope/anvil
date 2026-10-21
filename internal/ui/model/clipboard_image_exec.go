//go:build unix && !darwin && !android

package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// clipboardToolTimeout bounds a clipboard tool that never takes the image.
// The tool runs on the diagram worker, so renders wait behind it.
const clipboardToolTimeout = 5 * time.Second

var (
	errNoImageClipboardTool = errors.New("no clipboard tool for images; install wl-clipboard or xclip")
	errNoDesktopSession     = errors.New("no desktop session (DISPLAY and WAYLAND_DISPLAY are unset)")
)

// imageClipboardReady says why there's no clipboard tool to use, if there
// isn't one.
func imageClipboardReady() error {
	_, err := imageCopyCommand()
	return err
}

// writeClipboardImage hands the PNG to wl-copy under Wayland or xclip under
// X11. They stay behind to serve the image and do large transfers
// properly, which go-nativeclipboard's X11 clipboard doesn't.
func writeClipboardImage(png []byte) error {
	args, err := imageCopyCommand()
	if err != nil {
		return err
	}
	// The child left serving the image inherits stdout and stderr, so a
	// pipe would keep Run waiting until something else is copied. A file
	// doesn't.
	stderr, err := os.CreateTemp("", "anvil-clipboard-*")
	if err != nil {
		return fmt.Errorf("create file for %s's errors: %w", args[0], err)
	}
	defer os.Remove(stderr.Name())
	defer stderr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), clipboardToolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = bytes.NewReader(png)
	cmd.Stderr = stderr
	// Bounds Run after a kill, should a child still hold stdin open.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s didn't take the image within %s", args[0], clipboardToolTimeout)
		}
		said, _ := os.ReadFile(stderr.Name())
		for line := range strings.Lines(string(said)) {
			if line = strings.TrimSpace(line); line != "" {
				return fmt.Errorf("%s: %s", args[0], line)
			}
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
	if wayland {
		if _, err := exec.LookPath("wl-copy"); err == nil {
			return []string{"wl-copy", "--type", "image/png"}, nil
		}
	}
	if x11 {
		if _, err := exec.LookPath("xclip"); err == nil {
			return []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-i"}, nil
		}
	}
	return nil, errNoImageClipboardTool
}
