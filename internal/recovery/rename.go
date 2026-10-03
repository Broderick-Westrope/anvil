package recovery

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"time"
)

// errSharingViolation is ERROR_SHARING_VIOLATION, which Windows returns
// when another handle has the file open in a conflicting mode.
const errSharingViolation = syscall.Errno(32)

// windowsTransient reports whether err is a short-lived Windows conflict
// with another process touching the same recovery record. Windows refuses
// to read, replace, or delete a file while another handle has it open for
// a conflicting operation, and returns access denied for a file whose
// deletion is still pending. Both clear once the other handle closes.
func windowsTransient(err error) bool {
	return runtime.GOOS == "windows" &&
		(errors.Is(err, errSharingViolation) || errors.Is(err, os.ErrPermission))
}

// retryTransient runs op until it succeeds, fails with an error transient
// does not accept, or two seconds of backoff have passed.
func retryTransient(op func() error, transient func(error) bool) error {
	var slept time.Duration
	delay := time.Millisecond
	for {
		err := op()
		if err == nil || !transient(err) || slept >= 2*time.Second {
			return err
		}
		time.Sleep(delay)
		slept += delay
		delay = min(delay*2, 50*time.Millisecond)
	}
}

func retryRename(rename func() error) error {
	return retryTransient(rename, func(err error) bool {
		return errors.Is(err, os.ErrPermission) || windowsTransient(err)
	})
}

// readRecord reads a recovery file, retrying while another process holds
// it open on Windows.
func readRecord(path string) ([]byte, error) {
	var data []byte
	err := retryTransient(func() error {
		var err error
		data, err = os.ReadFile(path)
		return err
	}, windowsTransient)
	return data, err
}

// removeRecord deletes a recovery file, retrying while another process
// holds it open on Windows. A file that is already gone is not an error.
func removeRecord(path string) error {
	err := retryTransient(func() error { return os.Remove(path) }, windowsTransient)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
