//go:build !windows

package tools

import (
	"os"

	"golang.org/x/sys/unix"
)

// openRegularFile opens path for reading and returns it only when the
// opened descriptor is a regular file. It never blocks on a non-regular
// source, so a FIFO in a skills directory cannot stall the agent.
//
// O_NONBLOCK makes opening a FIFO or a slow character device return
// immediately instead of waiting for a peer, and it is a no-op for regular
// files, so no flag has to be cleared afterwards.
func openRegularFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegularSource
	}
	return f, nil
}
