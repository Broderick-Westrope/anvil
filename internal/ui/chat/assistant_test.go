package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestAssistantMessageItemExpandable guards the Expandable contract on
// AssistantMessageItem along the keyboard-driven expand path. The earlier
// implementation returned no value, which meant the type silently did
// not satisfy chat.Expandable and the keyboard-driven expand path in
// model/chat.go skipped thinking blocks.
//
// We exercise the contract through the bare Expandable interface (the
// same dispatch site model.Chat.ToggleExpandedSelectedItem uses), which
// proves both that AssistantMessageItem still satisfies the interface
// and that the bool return reports the right semantic state at every
// point in the cycle.
func TestAssistantMessageItemExpandable(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	// Short thinking: under the tail-window cap, so the cycle is
	// collapsed -> full -> collapsed (tail-window is skipped).
	msg := thinkingMessage("m1", "step one\nstep two\nstep three", "")
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	exp, ok := any(item).(Expandable)
	require.True(t, ok, "AssistantMessageItem must satisfy Expandable")

	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"new items must start in the collapsed view-mode")
	require.True(t, exp.ToggleExpanded(),
		"first toggle of a non-empty thinking block must report expanded")
	require.Equal(t, thinkingFullExpanded, item.thinkingViewMode,
		"short blocks must skip tail-window and land in full expansion")
	require.False(t, exp.ToggleExpanded(),
		"second toggle must report collapsed (cycle closed)")
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}

// TestAssistantMessageItemExpandableEmptyThinkingNoOp guards the B2
// fix: a message with no thinking text must treat ToggleExpanded as a
// no-op. Mutating the view mode in that case would thrash the
// thinking-section cache key for no visible benefit and would surprise
// the caller (model.Chat.ToggleExpandedSelectedItem would treat a
// "now collapsed" return as a real state change and re-scroll on it).
func TestAssistantMessageItemExpandableEmptyThinkingNoOp(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{ID: "m1-empty", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)

	exp, ok := any(item).(Expandable)
	require.True(t, ok, "AssistantMessageItem must satisfy Expandable")

	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
	require.False(t, exp.ToggleExpanded(),
		"empty thinking must report current (collapsed) state without flipping")
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"empty-thinking toggle must not mutate thinkingViewMode")

	// Whitespace-only thinking is still effectively empty.
	item.message.Parts = []message.ContentPart{
		message.ReasoningContent{Thinking: "  \n\n\t  ", StartedAt: testStartedAt},
	}
	require.False(t, exp.ToggleExpanded())
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}

// TestAssistantMessageItemTailWindowBoundary guards the B1 fix: the
// tail-window heuristic must compare logical line counts (1 +
// newline count) against the cap, not raw newline counts. The tail
// window shows one line over the cap in full rather than hiding a
// single line, so a source with up to N+1 logical lines must NOT trip
// the tail-window step (it would render the same as full), while N+2
// lines must trip it.
func TestAssistantMessageItemTailWindowBoundary(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()

	for _, tc := range []struct {
		lines int
		want  bool
	}{
		{maxExpandedThinkingTailLines, false},
		{maxExpandedThinkingTailLines + 1, false},
		{maxExpandedThinkingTailLines + 2, true},
	} {
		item := NewAssistantMessageItem(&sty, thinkingMessage("boundary", buildLines(tc.lines), "")).(*AssistantMessageItem)
		require.Equal(t, tc.want, item.tailWindowWouldTruncate(), "lines=%d", tc.lines)
	}
}

// buildLines returns a string of n logical lines (n-1 newlines). Each
// line is a unique short token so callers can distinguish head from
// tail in rendered output if they need to.
func buildLines(n int) string {
	if n <= 0 {
		return ""
	}
	var b []byte
	for i := 1; i <= n; i++ {
		if i > 1 {
			b = append(b, '\n')
		}
		b = append(b, 'l', 'n')
		b = append(b, []byte(itoa(i))...)
	}
	return string(b)
}

// TestAssistantMessageItemHandleMouseClick ensures HandleMouseClick does not
// toggle expansion on its own. The generic Expandable path in
// model/chat.go does the toggle; doing it here too would double-toggle and
// net to no change.
func TestAssistantMessageItemHandleMouseClick(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{ID: "m2", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.thinkingBoxHeight = 5

	// Click inside the thinking box signals handled but must not mutate
	// the view-mode state.
	require.True(t, item.HandleMouseClick(ansi.MouseLeft, 0, 2))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode,
		"HandleMouseClick must not toggle expansion on its own")

	// Click outside the thinking box is ignored entirely.
	require.False(t, item.HandleMouseClick(ansi.MouseLeft, 0, 10))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)

	// Non-left button is ignored.
	require.False(t, item.HandleMouseClick(ansi.MouseRight, 0, 2))
	require.Equal(t, thinkingCollapsed, item.thinkingViewMode)
}
