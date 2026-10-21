//go:build linux && !android

package model

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// clipboardToolTimeout bounds a clipboard tool that never takes the image.
const clipboardToolTimeout = 5 * time.Second

func writeClipboardImage(png []byte) error {
	args, err := imageCopyCommand(os.Getenv, exec.LookPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), clipboardToolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// Stdout and stderr stay unset: the tool leaves a child behind to serve
	// the image, and pipes it inherited would keep Run waiting until
	// something else is copied.
	cmd.Stdin = bytes.NewReader(png)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}
