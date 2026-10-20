package model

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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

// switchDiagrams answers every request with view, which a test can change.
type switchDiagrams struct{ view chat.DiagramView }

func (s *switchDiagrams) Diagram(string, int) chat.DiagramView     { return s.view }
func (s *switchDiagrams) FullDiagram(string, int) chat.DiagramView { return s.view }
func (*switchDiagrams) Prefetch(string, int)                       {}

// diagramChat is a 10-row chat holding a text message, then a diagram whose
// code block is far taller than the image it turns into.
func diagramChat(t *testing.T) (*Chat, *switchDiagrams) {
	t.Helper()
	com := common.DefaultCommon(nil)
	src := strings.Repeat("  A --> B\n", 30)
	diagrams := &switchDiagrams{}
	msg := &message.Message{ID: "m", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "Intro"}, message.Finish{Reason: message.FinishReasonEndTurn},
	}}
	c := NewChat(com)
	c.SetSize(80, 10)
	c.SetMessages(chat.NewAssistantMessageItem(com.Styles, msg), chat.NewDiagramItem(com.Styles, msg, 1, src, diagrams))
	return c, diagrams
}

func TestChatFollowsTheBottomWhenADiagramShrinks(t *testing.T) {
	t.Parallel()

	c, diagrams := diagramChat(t)
	c.ScrollToBottom()
	require.True(t, c.AtBottom())

	diagrams.view = chat.DiagramView{State: chat.DiagramReady, Image: "IMG1\nIMG2"}
	c.InvalidateDiagrams()
	require.Contains(t, ansi.Strip(c.list.Render()), "IMG2")
	require.True(t, c.AtBottom())
}

func TestChatKeepsAShrunkDiagramInView(t *testing.T) {
	t.Parallel()

	c, diagrams := diagramChat(t)
	c.ScrollToBottom()
	c.ScrollBy(-3)
	require.False(t, c.Follow())

	diagrams.view = chat.DiagramView{State: chat.DiagramReady, Image: "IMG1\nIMG2"}
	c.InvalidateDiagrams()
	require.Contains(t, ansi.Strip(c.list.Render()), "IMG", "the offset no longer points past the diagram")
}

func TestChatOpenedAtTheTopStaysThereWhenADiagramGrows(t *testing.T) {
	t.Parallel()

	com := common.DefaultCommon(nil)
	diagrams := &switchDiagrams{}
	c := NewChat(com)
	c.SetSize(80, 10)
	c.SetMessages(chat.NewDiagramDetailItem(com.Styles, "A\n", diagrams))
	c.SetFollow(false)
	c.ScrollToTop()
	require.True(t, c.AtBottom(), "the pending view fits on screen")

	diagrams.view = chat.DiagramView{State: chat.DiagramReady, Image: strings.TrimSuffix(strings.Repeat("ROW\n", 30), "\n")}
	c.InvalidateDiagrams()
	require.Zero(t, c.list.Offset(), "the drill-in still starts at the top of the diagram")
}

// recordingDiagrams records which sources the chat asked for.
type recordingDiagrams struct{ asked []string }

func (r *recordingDiagrams) Diagram(source string, _ int) chat.DiagramView {
	r.asked = append(r.asked, source)
	return chat.DiagramView{}
}

func (r *recordingDiagrams) FullDiagram(string, int) chat.DiagramView { return chat.DiagramView{} }
func (r *recordingDiagrams) Prefetch(source string, width int)        { r.Diagram(source, width) }

func TestChatDrawAsksForDiagramsWithinAScreenOfTheView(t *testing.T) {
	t.Parallel()

	com := common.DefaultCommon(nil)
	diagrams := &recordingDiagrams{}
	msg := &message.Message{ID: "m", Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "Intro"}, message.Finish{Reason: message.FinishReasonEndTurn},
	}}
	filler := func(id string) chat.MessageItem {
		return chat.NewAssistantMessageItem(com.Styles, &message.Message{ID: id, Role: message.Assistant, Parts: []message.ContentPart{
			message.TextContent{Text: strings.Repeat("filler\n\n", 3)}, message.Finish{Reason: message.FinishReasonEndTurn},
		}})
	}
	near := chat.NewDiagramItem(com.Styles, msg, 1, "near\n", diagrams)
	far := chat.NewDiagramItem(com.Styles, msg, 2, "far\n", diagrams)
	c := NewChat(com)
	c.SetSize(80, 10)
	c.SetPrefetch(true)
	c.SetMessages(filler("a"), filler("b"), near, filler("c"), filler("d"), filler("e"), far)
	c.ScrollToTop()
	// Sizing measured the items at the bottom; only count what Draw asks.
	diagrams.asked = nil

	scr := uv.NewScreenBuffer(80, 10)
	c.Draw(scr, scr.Bounds())
	require.Contains(t, diagrams.asked, "near\n", "within a screen below the view")
	require.NotContains(t, diagrams.asked, "far\n", "further away")
}

// countingDiagrams counts the diagrams asked for.
type countingDiagrams struct{ asked int }

func (c *countingDiagrams) Diagram(string, int) chat.DiagramView {
	c.asked++
	return chat.DiagramView{State: chat.DiagramPending}
}
func (c *countingDiagrams) FullDiagram(string, int) chat.DiagramView { return c.Diagram("", 0) }
func (c *countingDiagrams) Prefetch(string, int)                     { c.asked++ }

func TestChatPrefetchesDiagramsBelowTheViewOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	for name, on := range map[string]bool{"on": true, "off": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			com := common.DefaultCommon(nil)
			diagrams := &countingDiagrams{}
			msg := &message.Message{ID: "m", Role: message.Assistant}
			c := NewChat(com)
			c.SetSize(80, 5)
			c.SetPrefetch(on)
			c.SetMessages(
				chat.NewAssistantMessageItem(com.Styles, finishedText("m", strings.Repeat("line\n\n", 5))),
				chat.NewDiagramItem(com.Styles, msg, 1, "A\n", diagrams),
			)
			c.ScrollToTop()
			diagrams.asked = 0 // Only count what prefetching asks.
			c.prefetchNearby()
			require.Equal(t, on, diagrams.asked > 0)
		})
	}
}

func finishedText(id, text string) *message.Message {
	return &message.Message{ID: id, Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: text}, message.Finish{Reason: message.FinishReasonEndTurn},
	}}
}

func TestInvalidateDiagramsLeavesOtherItemsCached(t *testing.T) {
	t.Parallel()

	c, _ := diagramChat(t)
	text := c.MessageItems()[0]
	diagram := c.MessageItems()[1]
	textVersion, diagramVersion := text.Version(), diagram.Version()

	c.InvalidateDiagrams()
	require.Equal(t, textVersion, text.Version(), "text keeps its render")
	require.NotEqual(t, diagramVersion, diagram.Version(), "the diagram renders again")
}

func TestNewRendersDiagramsOnlyWhenTheyAreOn(t *testing.T) {
	t.Parallel()

	for name, off := range map[string]bool{"on": false, "off": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws := newWorkflowTestWorkspace()
			ws.diagramsOff = off
			u := New(common.DefaultCommon(ws), "", false)
			require.Equal(t, off, u.diagrams == nil)
		})
	}
}

func TestUIRoutesADiagramOpenToADrillIn(t *testing.T) {
	t.Parallel()

	u := New(common.DefaultCommon(newWorkflowTestWorkspace()), "", false)
	u.Update(chat.DiagramOpenMsg{Source: "flowchart LR\n Routed --> DrillIn\n"})
	require.True(t, u.isDrilledIn())
	entry := u.drillStack[len(u.drillStack)-1]
	require.Equal(t, "Diagram", entry.label)
	item, ok := entry.chat.MessageItems()[0].(*chat.DiagramDetailItem)
	require.True(t, ok)
	require.Contains(t, ansi.Strip(item.RawRender(80)), "Routed --> DrillIn", "without kitty, the code block")
	require.Equal(t, uiFocusMain, u.focus)
}

func TestSidebarCountsASplitMessageAsOneTurn(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"text, diagram, text, diagram": twoDiagrams,
		"headed by a diagram":          "```mermaid\nA\n```\n",
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := newSplitTestUI()
			m.appendSessionMessage(assistantWithTool(text, message.FinishReasonToolUse))
			turns, toolCalls := m.viewedSessionStats()
			require.Equal(t, 1, turns)
			require.Equal(t, 1, toolCalls)
		})
	}
}

func TestHelpHidesTheBrowserKeyOnAFailedDiagram(t *testing.T) {
	t.Parallel()

	m := newSplitTestUI()
	m.keyMap = DefaultKeyMap()
	sty := m.com.Styles.Attachments
	m.attachments = attachments.New(attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove), attachments.Keymap{})
	m.state = uiChat
	m.session = &session.Session{ID: "s"}
	m.focus = uiFocusMain
	diagrams := &switchDiagrams{view: chat.DiagramView{State: chat.DiagramFailed, Err: "bad"}}
	m.diagrams = diagrams
	m.chat.SetSize(80, 20)
	m.appendSessionMessage(assistantWithTool(twoDiagrams, message.FinishReasonToolUse))
	m.chat.SetSelected(1)
	m.chat.list.Render()
	require.NotContains(t, helpTexts(m.ShortHelp()), "o open in browser")
}

func TestSelectingAcrossADiagramCopiesItsSource(t *testing.T) {
	t.Parallel()

	for name, view := range map[string]chat.DiagramView{
		"code block": {State: chat.DiagramPending},
		"image":      {State: chat.DiagramReady, Image: "IMG1\nIMG2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			com := common.DefaultCommon(nil)
			msg := finishedText("m", "Intro\n\n```mermaid\nA --> B\n```\n\nOutro\n")
			c := NewChat(com)
			c.SetSize(80, 30)
			c.SetMessages(chat.NewAssistantMessageItems(com.Styles, msg, &switchDiagrams{view: view})...)
			require.Len(t, c.MessageItems(), 3)

			last := strings.Split(ansi.Strip(c.MessageItems()[2].(list.RawRenderable).RawRender(c.list.Width())), "\n")
			c.mouseDown = true
			c.mouseDownItem, c.mouseDownY, c.mouseDownX = 0, 0, chat.MessageLeftPaddingTotal
			c.mouseDragItem, c.mouseDragY, c.mouseDragX = 2, len(last)-1, 80
			c.list.Render()

			got := c.HighlightContent()
			require.Contains(t, got, "Intro")
			require.Contains(t, got, "```mermaid\nA --> B\n```")
			require.Contains(t, got, "Outro")
			require.NotContains(t, got, "IMG", "never the image's placeholders")
		})
	}
}

func TestChatPrefetchesOnlyWhenTheViewMoves(t *testing.T) {
	t.Parallel()

	com := common.DefaultCommon(nil)
	diagrams := &countingDiagrams{}
	msg := &message.Message{ID: "m", Role: message.Assistant}
	c := NewChat(com)
	c.SetSize(80, 5)
	c.SetPrefetch(true)
	c.SetMessages(
		chat.NewAssistantMessageItem(com.Styles, finishedText("m", strings.Repeat("line\n\n", 5))),
		chat.NewDiagramItem(com.Styles, msg, 1, "A\n", diagrams),
		chat.NewAssistantMessageItem(com.Styles, finishedText("n", strings.Repeat("more\n\n", 5))),
	)
	c.ScrollToTop()
	diagrams.asked = 0
	c.prefetchNearby()
	first := diagrams.asked
	require.Positive(t, first)

	c.prefetchNearby()
	require.Equal(t, first, diagrams.asked, "an unchanged view isn't walked again")

	c.ScrollBy(1)
	c.prefetchNearby()
	require.Greater(t, diagrams.asked, first, "scrolling looks again")
}
