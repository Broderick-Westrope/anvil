package model

import (
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/stretchr/testify/require"
)

// newSplitTestUI is newTestUI with a workspace, which message updates read
// config from, and diagrams turned on.
func newSplitTestUI() *UI {
	m := newTestUI()
	m.com.Workspace = &testWorkspace{cfg: &config.Config{}}
	m.diagrams = newMermaidFeature(uiHost{m}, &m.caps, m.com)
	return m
}

func chatIDs(c *Chat) []string {
	ids := make([]string, 0, c.Len())
	for _, item := range c.MessageItems() {
		ids = append(ids, item.ID())
	}
	return ids
}

// requireIndexed checks every item is found by its own ID at its index.
func requireIndexed(t *testing.T, c *Chat) {
	t.Helper()
	for i, item := range c.MessageItems() {
		require.Equal(t, i, c.idInxMap[item.ID()], item.ID())
		require.Same(t, item, c.MessageItem(item.ID()))
	}
}

func assistantWithTool(text string, finish message.FinishReason) message.Message {
	parts := []message.ContentPart{
		message.TextContent{Text: text},
		message.ToolCall{ID: "tool-1", Name: "view", Input: `{"file_path":"a.go"}`, Finished: true},
	}
	if finish != "" {
		parts = append(parts, message.Finish{Reason: finish})
	}
	return message.Message{ID: "m", Role: message.Assistant, Parts: parts}
}

const twoDiagrams = "Intro\n\n```mermaid\nA\n```\n\nMiddle\n\n```mermaid\nB\n```\n"

func TestUpdateSplitsAFinishedMessageAroundItsDiagrams(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	streaming := assistantWithTool(twoDiagrams, "")
	m.appendSessionMessage(streaming)
	require.Equal(t, []string{"m", "tool-1"}, chatIDs(m.chat), "one node while streaming")

	m.updateSessionMessage(streaming)
	require.Equal(t, []string{"m", "tool-1"}, chatIDs(m.chat), "still one node")

	finished := assistantWithTool(twoDiagrams, message.FinishReasonToolUse)
	m.updateSessionMessage(finished)
	require.Equal(t, []string{"m", "m#1", "m#2", "m#3", "tool-1"}, chatIDs(m.chat),
		"the diagrams sit where their fences were, before the tool call")
	requireIndexed(t, m.chat)
	diagram, ok := m.chat.MessageItem("m#1").(*chat.DiagramItem)
	require.True(t, ok)
	require.Equal(t, "A\n", diagram.Source())

	// A repeat update of the finished message keeps the same nodes.
	before := m.chat.MessageItems()
	m.updateSessionMessage(finished)
	require.Equal(t, before, m.chat.MessageItems())
}

func TestUpdateRebuildsTheSplitWhenTheTextChanges(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.appendSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "m#1", "m#2", "m#3", "tool-1"}, chatIDs(m.chat), "history loads split")

	m.updateSessionMessage(assistantWithTool("Only one\n\n```mermaid\nC\n```\n", message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "m#1", "tool-1"}, chatIDs(m.chat))
	requireIndexed(t, m.chat)

	m.updateSessionMessage(assistantWithTool("No diagrams now", message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "tool-1"}, chatIDs(m.chat), "back to one node")
	requireIndexed(t, m.chat)
}

func TestSplitKeepsTheSelectionOnItsItem(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.appendSessionMessage(assistantWithTool(twoDiagrams, ""))
	m.chat.SetSelected(1)
	selected := m.chat.SelectedItem()

	m.updateSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))
	require.Same(t, selected, m.chat.SelectedItem(), "the tool call stays selected")
}

func TestRemoveMessageRemovesItsNodes(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.appendSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))
	m.chat.RemoveMessage("m")
	require.Equal(t, []string{"tool-1"}, chatIDs(m.chat))
	requireIndexed(t, m.chat)
}

func TestADiagramHeadReturnsToOneNode(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.appendSessionMessage(assistantWithTool("```mermaid\nA\n```\n\nAfter\n", message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "m#1", "tool-1"}, chatIDs(m.chat))
	_, ok := m.chat.MessageItem("m").(*chat.DiagramItem)
	require.True(t, ok, "the opening diagram is the head, with no empty node above it")

	m.updateSessionMessage(assistantWithTool("No diagrams now", message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "tool-1"}, chatIDs(m.chat))
	_, ok = m.chat.MessageItem("m").(*chat.AssistantMessageItem)
	require.True(t, ok)
	requireIndexed(t, m.chat)
}

func TestDiagramsOffKeepsAFinishedMessageInOneNode(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.diagrams = nil
	m.appendSessionMessage(assistantWithTool(twoDiagrams, ""))
	m.updateSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))
	require.Equal(t, []string{"m", "tool-1"}, chatIDs(m.chat))

	m.appendSessionMessage(message.Message{ID: "h", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: twoDiagrams}, message.Finish{Reason: message.FinishReasonEndTurn},
	}})
	require.Equal(t, []string{"m", "tool-1", "h", "h:assistant-info"}, chatIDs(m.chat), "history loads unsplit")
}

func helpTexts(binds []key.Binding) []string {
	var out []string
	for _, b := range binds {
		out = append(out, b.Help().Key+" "+b.Help().Desc)
	}
	return out
}

func TestHelpShowsDiagramKeysOnADiagramNode(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.keyMap = DefaultKeyMap()
	sty := m.com.Styles.Attachments
	m.attachments = attachments.New(attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove), attachments.Keymap{})
	m.state = uiChat
	m.session = &session.Session{ID: "s"}
	m.focus = uiFocusMain
	m.appendSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))

	m.chat.SetSelected(0)
	require.NotContains(t, helpTexts(m.ShortHelp()), "o open in browser", "a text node")

	m.chat.SetSelected(1)
	require.Contains(t, helpTexts(m.ShortHelp()), "o open in browser")
	require.Contains(t, helpTexts(m.ShortHelp()), "c/y copy")
	var full []key.Binding
	for _, group := range m.FullHelp() {
		full = append(full, group...)
	}
	require.Contains(t, helpTexts(full), "o open in browser")

	m.focus = uiFocusEditor
	require.NotContains(t, helpTexts(m.ShortHelp()), "o open in browser", "the editor has focus")
}
