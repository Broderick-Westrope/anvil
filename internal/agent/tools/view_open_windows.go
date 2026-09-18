//go:build windows

package tools

import "os"

func openRegularFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if info, statErr := os.Stat(path); statErr == nil && !info.Mode().IsRegular() {
			return nil, errNotRegularSource
		}
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
