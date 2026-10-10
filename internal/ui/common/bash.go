package common

import "strings"

// StripBashDisplayPrefix removes a leading "cd <workingDir> && " from a
// command for display, but only when the cd target is exactly workingDir.
func StripBashDisplayPrefix(cmd, workingDir string) string {
	if workingDir == "" {
		return cmd
	}
	return strings.TrimPrefix(cmd, "cd "+workingDir+" && ")
}
