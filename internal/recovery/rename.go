package recovery

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

func retryRename(rename func() error) error {
	var slept time.Duration
	delay := time.Millisecond
	for {
		err := rename()
		transient := errors.Is(err, os.ErrPermission) ||
			(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(32)))
		if err == nil || !transient || slept >= 2*time.Second {
			return err
		}
		time.Sleep(delay)
		slept += delay
		delay = min(delay*2, 50*time.Millisecond)
	}
}
