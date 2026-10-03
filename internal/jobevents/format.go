package jobevents

import (
	"fmt"
	"strings"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/shell"
)

const (
	noticeLabelLength = 80
	noticeLineLength  = 200
	noticeTailLines   = 10
)

// FormatNotice renders claimed events as one reminder for the model.
func FormatNotice(events []Event, remaining int, now time.Time) string {
	var b strings.Builder
	b.WriteString("<system_reminder>\nBackground job updates:\n")
	for _, e := range events {
		label := strings.TrimRight(shell.JobLabel(e.Info, noticeLabelLength), ".")
		switch e.Kind {
		case KindCompleted:
			runtime := shell.FormatRuntime(shell.JobRuntime(e.Info, now))
			fmt.Fprintf(&b, "- Job %s completed, exit %d (%s): %s.", e.JobID, e.Info.ExitCode, runtime, label)
			tail := shell.LastLines(e.Tail, noticeTailLines)
			if tail == "" {
				b.WriteString("\n")
				continue
			}
			b.WriteString(" Last lines:\n")
			for line := range strings.SplitSeq(tail, "\n") {
				fmt.Fprintf(&b, "  %s\n", truncate(line, noticeLineLength))
			}
		case KindMatched:
			at := e.CreatedAt
			if at.IsZero() {
				at = now
			}
			runtime := shell.FormatRuntime(shell.JobRuntime(e.Info, at))
			fmt.Fprintf(&b, "- Job %s printed a line matching your watch (%s): %s\n", e.JobID, runtime, truncate(e.Line, noticeLineLength))
		}
	}
	if remaining > 0 {
		fmt.Fprintf(&b, "(+%d more pending; they arrive at the next step, or use job_list)\n", remaining)
	}
	b.WriteString("</system_reminder>")
	return b.String()
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}
