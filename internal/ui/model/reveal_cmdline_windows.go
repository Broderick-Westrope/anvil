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
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
}
