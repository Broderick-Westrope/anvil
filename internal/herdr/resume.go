package herdr

import (
	"strings"
	"unicode"
)

const (
	// resumeCommand is the command Herdr runs in the pane's shell after a
	// server restart. Herdr requires a plain command name, not a path.
	resumeCommand = "anvil"
	// maxResumeBytes is Herdr's limit on the whole resume command.
	maxResumeBytes = 8 << 10
)

// resumeArgs is the command that reopens sessionID after a Herdr restart,
// matching the hint printed on exit. It is nil when there is no session
// or when Herdr would reject the command, because a rejected command
// fails the whole state report.
func resumeArgs(sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	args := []string{resumeCommand, "--session", sessionID, "--there"}
	size := 0
	for _, arg := range args {
		if strings.ContainsRune(arg, '\'') || strings.ContainsFunc(arg, unicode.IsControl) {
			return nil
		}
		size += len(arg)
	}
	if size > maxResumeBytes {
		return nil
	}
	return args
}
