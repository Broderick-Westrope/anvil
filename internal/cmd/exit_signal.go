package cmd

import (
	"os"
	"os/signal"
	"syscall"
)

// terminationWatch records whether the process was asked to terminate.
// Bubble Tea turns SIGTERM into an ordinary quit, which would otherwise
// look like the user closing the window and discard the recovery record
// a restart or logout is meant to leave behind.
type terminationWatch struct {
	signals  chan os.Signal
	received bool
}

func watchTermination() *terminationWatch {
	watch := &terminationWatch{signals: make(chan os.Signal, 1)}
	signal.Notify(watch.signals, syscall.SIGTERM)
	return watch
}

// Received reports whether a termination signal has arrived. It stays true
// once it has returned true. It is not safe for concurrent use.
func (watch *terminationWatch) Received() bool {
	if !watch.received {
		select {
		case <-watch.signals:
			watch.received = true
		default:
		}
	}
	return watch.received
}

func (watch *terminationWatch) Stop() {
	signal.Stop(watch.signals)
}
