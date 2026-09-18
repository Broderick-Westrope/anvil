//go:build windows

package tools

import "os"

// openRegularFile opens path for reading and returns it only when the
// opened descriptor is a regular file. Windows has no filesystem FIFO
// (named pipes live in the \\.\pipe\ namespace and are not reachable as a
// component of a skills directory path), so a plain read-only open has no
// blocking-open equivalent to defend against.
func openRegularFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}

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
