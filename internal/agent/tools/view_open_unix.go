//go:build !windows

package tools

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRegularFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.Mode().IsRegular() {
			return nil, errNotRegularSource
		}
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
