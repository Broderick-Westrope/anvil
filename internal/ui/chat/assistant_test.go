package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestAssistantMessageItemHandleMouseClick ensures only left clicks on the
// thinking footer are handled, since that's the drill-in target.
func TestAssistantMessageItemHandleMouseClick(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{ID: "m2", Role: message.Assistant}
	item := NewAssistantMessageItem(&sty, msg).(*AssistantMessageItem)
	item.thinkingFooterHeight = 1

	// A click on the thinking footer is handled.
	require.True(t, item.HandleMouseClick(ansi.MouseLeft, 0, 0))

	// A click below the footer is ignored.
	require.False(t, item.HandleMouseClick(ansi.MouseLeft, 0, 1))

	// Non-left button is ignored.
	require.False(t, item.HandleMouseClick(ansi.MouseRight, 0, 0))
}
