package chat

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// JobEventMessageItem renders a background job notice (a job_event user
// message) as a compact, muted block: one header line per event in the
// style of the job tool headers, with completion tails shown only when
// expanded. Wake-started turns begin with this notice.
type JobEventMessageItem struct {
	*list.Versioned
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message  *message.Message
	sty      *styles.Styles
	expanded bool
}

var _ Expandable = (*JobEventMessageItem)(nil)

// NewJobEventMessageItem creates a new JobEventMessageItem.
func NewJobEventMessageItem(sty *styles.Styles, msg *message.Message) MessageItem {
	v := list.NewVersioned()
	return &JobEventMessageItem{
		Versioned:                v,
		highlightableMessageItem: defaultHighlighter(sty, v),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     newFocusableMessageItem(v),
		message:                  msg,
		sty:                      sty,
	}
}

// ID implements MessageItem.
func (j *JobEventMessageItem) ID() string {
	return j.message.ID
}

// Finished implements list.Item. Notices are immutable once persisted.
func (j *JobEventMessageItem) Finished() bool {
	return true
}

// ToggleExpanded implements Expandable.
func (j *JobEventMessageItem) ToggleExpanded() bool {
	j.expanded = !j.expanded
	j.clearCache()
	j.Bump()
	return j.expanded
}

// SelectionSource implements [list.SourceSelectable].
func (j *JobEventMessageItem) SelectionSource() string {
	return strings.Join(noticeBody(j.message.Content().Text), "\n")
}

// RawRender implements [MessageItem].
func (j *JobEventMessageItem) RawRender(width int) string {
	itemWidth := width - MessageLeftPaddingTotal
	content, height, ok := j.getCachedRender(itemWidth)
	if !ok {
		content = renderJobNotice(j.sty, j.message.Content().Text, itemWidth, j.expanded)
		height = lipgloss.Height(content)
		j.setCachedRender(content, itemWidth, height)
	}
	return j.renderHighlighted(content, itemWidth, height)
}

// Render implements MessageItem.
func (j *JobEventMessageItem) Render(width int) string {
	useCache := !j.isHighlighted()
	var key uint64
	if j.focused {
		key = 1
	}
	if useCache {
		if cached, ok := j.getCachedPrefixedRender(width, key); ok {
			return cached
		}
	}
	prefix := j.sty.Messages.ToolCallBlurred.Render()
	if j.focused {
		prefix = j.sty.Messages.ToolCallFocused.Render()
	}
	lines := strings.Split(j.RawRender(width), "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	out := strings.Join(lines, "\n")
	if useCache {
		j.setCachedPrefixedRender(out, width, key)
	}
	return out
}

var (
	noticeCompletedRe = regexp.MustCompile(`^- Job (\S+) completed, exit (-?\d+) \(([^)]*)\): (.*?)(?: Last lines:)?$`)
	noticeMatchedRe   = regexp.MustCompile(`^- Job (\S+) printed a line matching your watch \(([^)]*)\): (.*)$`)
)

// noticeBody returns the notice's lines without the reminder wrapper and
// the "Background job updates:" title.
func noticeBody(text string) []string {
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
		switch strings.TrimSpace(line) {
		case "<system_reminder>", "</system_reminder>", "Background job updates:", "":
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// renderJobNotice renders a job_event notice. Event lines become job
// headers; indented tail lines are shown only when expanded.
func renderJobNotice(sty *styles.Styles, text string, width int, expanded bool) string {
	var out []string
	for _, line := range noticeBody(text) {
		switch {
		case strings.HasPrefix(line, "- "):
			out = append(out, jobNoticeHeader(sty, line, width))
		case strings.HasPrefix(line, "  "):
			if expanded {
				out = append(out, sty.Tool.JobPID.Render(ansi.Truncate("    "+strings.TrimPrefix(line, "  "), width, "…")))
			}
		default:
			out = append(out, sty.Tool.JobPID.Render(ansi.Truncate(line, width, "…")))
		}
	}
	return strings.Join(out, "\n")
}

// jobNoticeHeader renders one event line like a compact job tool header:
// "● Job (Completed) 019 exit 1 · 9m14s label".
func jobNoticeHeader(sty *styles.Styles, line string, width int) string {
	var icon, action, jobID, detail, description string
	if m := noticeCompletedRe.FindStringSubmatch(line); m != nil {
		icon = toolIcon(sty, ToolStatusSuccess)
		if m[2] != "0" {
			icon = toolIcon(sty, ToolStatusError)
		}
		action, jobID, detail, description = "Completed", m[1], fmt.Sprintf("exit %s · %s", m[2], m[3]), strings.TrimSuffix(m[4], ".")
	} else if m := noticeMatchedRe.FindStringSubmatch(line); m != nil {
		icon = toolIcon(sty, ToolStatusRunning)
		action, jobID, detail, description = "Watch", m[1], m[2], m[3]
	} else {
		return sty.Tool.JobDescription.Render(ansi.Truncate(strings.TrimPrefix(line, "- "), width, "…"))
	}

	prefix := fmt.Sprintf("%s %s %s %s %s", icon,
		sty.Tool.JobToolName.Render("Job"),
		sty.Tool.JobAction.Render("("+action+")"),
		sty.Tool.JobPID.Render(jobID),
		sty.Tool.JobPID.Render(detail),
	)
	available := width - lipgloss.Width(prefix) - 1
	if description == "" || available < 10 {
		return ansi.Truncate(prefix, width, "…")
	}
	return prefix + " " + sty.Tool.JobDescription.Render(ansi.Truncate(description, available, "…"))
}
