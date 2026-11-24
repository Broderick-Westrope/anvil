package model

import (
	"strings"
	"testing"
	"time"

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

// TestChatHighlightContent_DoubleClickCopiesShownText verifies that a
// double-clicked URL, issue ID or word is copied as shown, without the
// markdown around it, so it pastes straight into a browser or tracker.
func TestChatHighlightContent_DoubleClickCopiesShownText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		markdown string
		target   string
	}{
		"bold url in list": {
			markdown: "Links:\n\n- **https://example.com/issues/42**\n- other",
			target:   "https://example.com/issues/42",
		},
		"first line bold issue id": {
			markdown: "**ENG-1234** is fixed.\n\nMore text here.",
			target:   "ENG-1234",
		},
		"inline code url": {
			markdown: "See `https://example.com/x?y=1` for details.",
			target:   "https://example.com/x?y=1",
		},
		"bold word": {
			markdown: "This is **important** stuff.",
			target:   "important",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			com := common.DefaultCommon(nil)
			msg := &message.Message{
				ID:   "a1",
				Role: message.Assistant,
				Parts: []message.ContentPart{
					message.TextContent{Text: tc.markdown},
					message.Finish{Reason: message.FinishReasonEndTurn},
				},
			}
			item := chat.NewAssistantMessageItem(com.Styles, msg)

			c := NewChat(com)
			c.SetSize(80, 20)
			c.SetMessages(item)
			c.list.Render()

			lines := strings.Split(ansi.Strip(item.RawRender(c.list.Width())), "\n")
			lineIdx, col := -1, -1
			for i, line := range lines {
				if idx := strings.Index(line, tc.target); idx >= 0 {
					lineIdx, col = i, ansi.StringWidth(line[:idx])
					break
				}
			}
			require.GreaterOrEqual(t, lineIdx, 0, "rendered output missing %q", tc.target)

			x := col + chat.MessageLeftPaddingTotal + 1
			_, y := c.list.ItemIndexAtPosition(x, lineIdx)
			require.Equal(t, lineIdx, y)
			c.HandleMouseDown(x, lineIdx)
			c.HandleMouseDown(x, lineIdx)
			c.HandleMouseUp(x, lineIdx)
			c.list.Render()

			require.Equal(t, tc.target, c.HighlightContent())
		})
	}
}

// TestChatHighlightContent_DragAfterDoubleClickKeepsMarkdown verifies that
// a selection made by dragging, after a double-click or after a fresh
// click, still copies the markdown source.
func TestChatHighlightContent_DragAfterDoubleClickKeepsMarkdown(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		freshClick bool
		dragDown   bool
		want       string
	}{
		"extend double-click sideways": {want: "Alpha has **bold** words"},
		"extend double-click down":     {dragDown: true, want: "Alpha has **bold** words and more text after it."},
		"new click":                    {freshClick: true, want: "Alpha has **bold** words"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			com := common.DefaultCommon(nil)
			msg := &message.Message{
				ID:   "a1",
				Role: message.Assistant,
				Parts: []message.ContentPart{
					message.TextContent{Text: "Alpha has **bold** words and more text after it.\n\nBeta line."},
					message.Finish{Reason: message.FinishReasonEndTurn},
				},
			}
			item := chat.NewAssistantMessageItem(com.Styles, msg)

			c := NewChat(com)
			c.SetSize(80, 20)
			c.SetMessages(item)
			c.list.Render()

			lines := strings.Split(ansi.Strip(item.RawRender(c.list.Width())), "\n")
			alphaLine, betaLine, line := -1, -1, ""
			for i, l := range lines {
				if strings.Contains(l, "Alpha") {
					alphaLine, line = i, l
				}
				if strings.Contains(l, "Beta") {
					betaLine = i
				}
			}
			require.GreaterOrEqual(t, alphaLine, 0)
			require.Greater(t, betaLine, alphaLine)
			pad := chat.MessageLeftPaddingTotal
			alphaX := strings.Index(line, "Alpha") + pad
			require.Equal(t, alphaX, strings.Index(lines[betaLine], "Beta")+pad)
			dragX, dragY := strings.Index(line, "words")+len("words")+pad, alphaLine
			if tc.dragDown {
				dragX, dragY = alphaX, betaLine
			}

			c.HandleMouseDown(alphaX, alphaLine)
			c.HandleMouseDown(alphaX, alphaLine)
			if tc.freshClick {
				c.HandleMouseUp(alphaX, alphaLine)
				c.lastClickTime = c.lastClickTime.Add(-time.Second)
				c.HandleMouseDown(alphaX, alphaLine)
			}
			c.HandleMouseDrag(dragX, dragY)
			c.HandleMouseUp(dragX, dragY)
			c.list.Render()

			require.Equal(t, tc.want, c.HighlightContent())
		})
	}
}
