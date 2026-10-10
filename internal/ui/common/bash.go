package common

import "strings"

// StripBashDisplayPrefix removes a leading "cd <workingDir> && " from a
// command for display, but only when the cd target is exactly workingDir.
// The target may be bare or wrapped in single or double quotes, since paths
// with spaces have to be quoted.
func StripBashDisplayPrefix(cmd, workingDir string) string {
	if workingDir == "" {
		return cmd
	}
	for _, target := range []string{workingDir, `"` + workingDir + `"`, "'" + workingDir + "'"} {
		if rest, ok := strings.CutPrefix(cmd, "cd "+target+" && "); ok {
			return rest
		}
	}
	return cmd
}
