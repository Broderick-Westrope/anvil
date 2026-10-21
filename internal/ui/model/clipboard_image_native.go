//go:build (darwin || windows) && !ios && !arm && !386

package model

import (
	"errors"
	"time"

	"github.com/aymanbagabas/go-nativeclipboard"
)

// nativeClipboardTimeout bounds a write to a clipboard another app holds.
// go-nativeclipboard waits for it without limit on Windows, and the write
// runs on the diagram worker, so renders would wait too.
const nativeClipboardTimeout = 5 * time.Second

var errClipboardBusy = errors.New("another app is holding the clipboard; try again")

// imageClipboardReady is nil: the native clipboard is always there.
func imageClipboardReady() error { return nil }

func writeClipboardImage(png []byte) error {
	return writeWithin(nativeClipboardTimeout, func() error {
		// The channel Write returns reports a later copy replacing this
		// one, which doesn't matter here.
		_, err := nativeclipboard.Image.Write(png)
		return err
	})
}

// writeWithin runs write, giving up after timeout. A write given up on
// carries on in the background, and lands if the clipboard frees up.
func writeWithin(timeout time.Duration, write func() error) error {
	done := make(chan error, 1)
	go func() { done <- write() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errClipboardBusy
	}
}
