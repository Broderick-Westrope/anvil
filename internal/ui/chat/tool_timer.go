package chat

import (
	"time"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// toolTimerThreshold is how long a tool call must run before its header
// shows a duration. Most calls finish well inside it, so normal turns stay
// quiet.
const toolTimerThreshold = 10 * time.Second

// toolTimerDuration returns the duration a tool call's header shows, or
// zero for none: the final duration once the call has a result, the time
// so far while it runs, and nothing below the threshold or when a time is
// unknown. The threshold compares unrounded durations.
func toolTimerDuration(call message.ToolCall, result *message.ToolResult, running bool, now time.Time) time.Duration {
	if call.StartedAt == 0 {
		return 0
	}
	end := now
	switch {
	case result != nil:
		if result.FinishedAt == 0 {
			return 0
		}
		end = time.UnixMilli(result.FinishedAt)
	case !running:
		return 0
	}
	elapsed := end.Sub(time.UnixMilli(call.StartedAt))
	if elapsed < toolTimerThreshold {
		return 0
	}
	return elapsed
}

// fitToolTimerSuffix returns the header suffix showing d and the width
// left for the rest of a header of the given width whose fixed part (icon,
// name) takes fixed columns. The suffix is "" when d is zero or the header
// has no room for it, so it never pushes the header further off screen.
func fitToolTimerSuffix(sty *styles.Styles, d time.Duration, width, fixed int) (suffix string, remaining int) {
	remaining = width - fixed
	if d <= 0 {
		return "", remaining
	}
	suffix = sty.Tool.StatsSep.Render(" · ") + sty.Tool.StatsLine.Render(common.FormatDuration(d))
	// Leave the params at least one column: an expanded header can't wrap
	// them into none.
	if withSuffix := remaining - lipgloss.Width(suffix); withSuffix > 0 {
		return suffix, withSuffix
	}
	return "", remaining
}
