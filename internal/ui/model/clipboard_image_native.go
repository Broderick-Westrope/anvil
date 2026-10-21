//go:build (darwin || windows) && !ios && !arm && !386

package model

import (
	"time"

	"github.com/aymanbagabas/go-nativeclipboard"
)

// nativeClipboardTimeout bounds a write to a clipboard another app holds.
// On Windows, go-nativeclipboard retries OpenClipboard in a loop with no
// sleep until the other app lets go, spinning a core; the write runs on
// the diagram worker, so renders would wait too.
const nativeClipboardTimeout = 5 * time.Second

// nativeWrites is the native clipboard's write in flight.
var nativeWrites pendingWrite

// imageClipboardReady is nil: the native clipboard is always there.
func imageClipboardReady() error { return nil }

func writeClipboardImage(png []byte) error {
	return nativeWrites.writeWithin(nativeClipboardTimeout, func() error {
		// The channel Write returns reports a later copy replacing this
		// one, which doesn't matter here.
		_, err := nativeclipboard.Image.Write(png)
		return err
	})
}
