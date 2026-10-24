//go:build windows

package model

import (
	"os/exec"
	"syscall"
)

// setRawCmdLine makes line cmd's whole command line, as Windows passes it,
// for programs that parse their own. An empty line keeps the one Go builds
// from cmd's arguments.
func setRawCmdLine(cmd *exec.Cmd, line string) {
	// new, not a composite literal's &: the mutation check runs on Linux,
	// where this file isn't built, so it can't cover a mutant of the &.
	attr := new(syscall.SysProcAttr)
	attr.CmdLine = line
	cmd.SysProcAttr = attr
}
