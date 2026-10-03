package model

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestChatHighlightContent_PreservesMarkdown verifies that copying a
// selection from an assistant message returns the raw markdown source
// rather than the glamour-rendered text.
func TestChatHighlightContent_PreservesMarkdown(t *testing.T) {
	t.Parallel()

	com := common.DefaultCommon(nil)
	msg := &message.Message{
		ID:   "a1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "Run `go test ./...` and check **all** results.\n\nThen rest."},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	}
	item := chat.NewAssistantMessageItem(com.Styles, msg)

	c := NewChat(com)
	c.SetSize(80, 20)
	c.SetMessages(item)

	width := c.list.Width()
	lines := strings.Split(ansi.Strip(item.RawRender(width)), "\n")
	lineIdx, col := -1, -1
	for i, line := range lines {
		if idx := strings.Index(line, "Run"); idx >= 0 {
			lineIdx, col = i, idx
			break
		}
	}
	require.GreaterOrEqual(t, lineIdx, 0, "rendered output missing first line")
	endCol := strings.Index(lines[lineIdx], "results.") + len("results.")

	c.mouseDownItem, c.mouseDragItem = 0, 0
	c.mouseDownY, c.mouseDownX = lineIdx, col
	c.mouseDragY, c.mouseDragX = lineIdx, endCol
	item.(interface {
		SetHighlight(startLine, startCol, endLine, endCol int)
	}).SetHighlight(lineIdx, col+chat.MessageLeftPaddingTotal, lineIdx, endCol+chat.MessageLeftPaddingTotal)

	require.Equal(t, "Run `go test ./...` and check **all** results.", c.HighlightContent())
}
