package model

import (
	"errors"
	"sync/atomic"
	"time"
)

var (
	errClipboardBusy    = errors.New("the clipboard didn't take the image in time; try again")
	errClipboardPending = errors.New("an earlier copy is still waiting for the clipboard; try again")
)

// pendingWrite allows one clipboard write in flight. Only the native
// clipboard uses it, but it builds on every platform so its tests run on
// all of them.
type pendingWrite struct{ busy atomic.Bool }

// writeWithin runs write, giving up after timeout. A write given up on
// carries on in the background holding go-nativeclipboard's lock, and
// lands if the clipboard frees up. Until it does, writeWithin refuses
// others rather than queue them behind it, where they'd hold a PNG each
// and could land in any order.
func (p *pendingWrite) writeWithin(timeout time.Duration, write func() error) error {
	if !p.busy.CompareAndSwap(false, true) {
		return errClipboardPending
	}
	done := make(chan error, 1)
	go func() {
		err := write()
		// Free the clipboard before reporting, so the next write can start
		// as soon as this one returns.
		p.busy.Store(false)
		done <- err
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
