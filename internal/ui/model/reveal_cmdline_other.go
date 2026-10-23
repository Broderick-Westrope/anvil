//go:build !windows

package model

import "os/exec"

// setRawCmdLine does nothing: only Windows passes a command line as one
// string, so elsewhere cmd's arguments are what the program gets.
func setRawCmdLine(*exec.Cmd, string) {}
