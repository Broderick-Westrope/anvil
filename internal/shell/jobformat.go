package shell

import (
	"fmt"
	"strings"
	"time"
)

// FormatRuntime renders a duration compactly: 12s, 4m12s, 2h03m.
func FormatRuntime(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// JobLabel is the description if set, otherwise the command,
// collapsed to one line and truncated to maxLen runes.
func JobLabel(info JobInfo, maxLen int) string {
	label := info.Description
	if strings.TrimSpace(label) == "" {
		label = info.Command
	}
	label = strings.Join(strings.Fields(label), " ")

	runes := []rune(label)
	if maxLen <= 0 || len(runes) <= maxLen {
		return label
	}
	if maxLen == 1 {
		return string(runes[:1])
	}
	return string(runes[:maxLen-1]) + "…"
}

// JobRuntime is now-StartedAt for running jobs and
// CompletedAt-StartedAt for finished ones.
func JobRuntime(info JobInfo, now time.Time) time.Duration {
	if info.Done {
		return info.CompletedAt.Sub(info.StartedAt)
	}
	return now.Sub(info.StartedAt)
}

// LastLines returns at most n trailing lines of s.
func LastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" || n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
