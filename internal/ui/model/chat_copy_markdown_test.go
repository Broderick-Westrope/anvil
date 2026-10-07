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

// TestChatHighlightContent_JoinsSoftWrappedLines verifies that a selection
// spanning lines that only exist because the renderer wrapped a long
// paragraph is copied without the wrap line breaks.
func TestChatHighlightContent_JoinsSoftWrappedLines(t *testing.T) {
	t.Parallel()

	paragraph := "This paragraph is deliberately long so that the markdown renderer has to wrap it across several lines in a narrow chat."
	com := common.DefaultCommon(nil)
	msg := &message.Message{
		ID:   "a1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: paragraph + "\n\nSecond paragraph."},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	}
	item := chat.NewAssistantMessageItem(com.Styles, msg)

	c := NewChat(com)
	c.SetSize(40, 20)
	c.SetMessages(item)

	lines := strings.Split(ansi.Strip(item.RawRender(c.list.Width())), "\n")
	startLine, startCol, endLine, endCol := -1, -1, -1, -1
	for i, line := range lines {
		if idx := strings.Index(line, "This"); idx >= 0 && startLine < 0 {
			startLine, startCol = i, idx
		}
		if idx := strings.Index(line, "chat."); idx >= 0 {
			endLine, endCol = i, idx+len("chat.")
		}
	}
	require.GreaterOrEqual(t, startLine, 0)
	require.Greater(t, endLine, startLine, "paragraph must wrap for this test to be meaningful")

	c.mouseDownItem, c.mouseDragItem = 0, 0
	c.mouseDownY, c.mouseDownX = startLine, startCol
	c.mouseDragY, c.mouseDragX = endLine, endCol
	item.(interface {
		SetHighlight(startLine, startCol, endLine, endCol int)
	}).SetHighlight(startLine, startCol+chat.MessageLeftPaddingTotal, endLine, endCol+chat.MessageLeftPaddingTotal)

	require.Equal(t, paragraph, c.HighlightContent())
}
