//go:build !windows

package cmd

import "syscall"

// execAnvil replaces the current process with exe, keeping the process ID
// and terminal.
var execAnvil = func(exe string, args, env []string) error {
	return syscall.Exec(exe, append([]string{exe}, args...), env)
}
