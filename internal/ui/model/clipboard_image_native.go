//go:build (darwin || windows) && !ios && !arm && !386

package model

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aymanbagabas/go-nativeclipboard"
)

// nativeClipboardTimeout bounds a write to a clipboard another app holds.
// go-nativeclipboard waits for it without limit on Windows, and the write
// runs on the diagram worker, so renders would wait too.
const nativeClipboardTimeout = 5 * time.Second

var (
	errClipboardBusy = fmt.Errorf("the clipboard didn't take the image within %s; try again", nativeClipboardTimeout)
	// nativeWrites is the native clipboard's write in flight.
	nativeWrites pendingWrite
)

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

// pendingWrite allows one clipboard write in flight.
type pendingWrite struct{ busy atomic.Bool }

// writeWithin runs write, giving up after timeout. A write given up on
// carries on in the background holding go-nativeclipboard's lock, and
// lands if the clipboard frees up. Until it does, writeWithin refuses
// others rather than queue them behind it, where they'd hold a PNG each
// and could land in any order.
func (p *pendingWrite) writeWithin(timeout time.Duration, write func() error) error {
	if !p.busy.CompareAndSwap(false, true) {
		return errClipboardBusy
	}
	done := make(chan error, 1)
	go func() {
		defer p.busy.Store(false)
		done <- write()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errClipboardBusy
	}
}
