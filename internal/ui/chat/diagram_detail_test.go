package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func finishedMessage(id string, parts ...message.ContentPart) *message.Message {
	parts = append(parts, message.Finish{Reason: message.FinishReasonEndTurn})
	return &message.Message{ID: id, Role: message.Assistant, Parts: parts}
}

func cmdMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	require.NotNil(t, cmd)
	return cmd()
}

// The diagram hook is package-level, so these tests run serially.
//
//nolint:paralleltest // Mutates the package-level Diagrams hook.
func TestAssistantDiagramClickOpensItsDrillIn(t *testing.T) {
	prev := Diagrams
	t.Cleanup(func() { Diagrams = prev })
	Diagrams = fakeDiagrams{"A\n": "[[A1]]\n[[A2]]", "B\n": "[[B1]]"}

	sty := styles.TokyoNight()
	text := "Intro\n\n```mermaid\nA\n```\n\nMiddle\n\n```mermaid\nB\n```\n"
	reasoning := message.ReasoningContent{Thinking: "hmm", StartedAt: testStartedAt, FinishedAt: testFinishedAt}
	item := NewAssistantMessageItem(&sty, finishedMessage("m1", reasoning, message.TextContent{Text: text})).(*AssistantMessageItem)
	lines := strings.Split(ansi.Strip(item.RawRender(80)), "\n")

	lineOf := func(marker string) int {
		for i, l := range lines {
			if strings.Contains(l, marker) {
				return i
			}
		}
		t.Fatalf("%q not rendered in %q", marker, lines)
		return -1
	}

	for marker, source := range map[string]string{"[[A1]]": "A\n", "[[A2]]": "A\n", "[[B1]]": "B\n"} {
		require.True(t, item.HandleMouseClick(ansi.MouseLeft, 0, lineOf(marker)), marker)
		require.Equal(t, DiagramOpenMsg{Source: source}, cmdMsg(t, item.ClickDrillIn()), marker)
	}

	require.False(t, item.HandleMouseClick(ansi.MouseLeft, 0, lineOf("Middle")), "text isn't clickable")

	require.True(t, item.HandleMouseClick(ansi.MouseLeft, 0, 0), "the thinking footer")
	require.IsType(t, util.ThinkingDrillInMsg{}, cmdMsg(t, item.ClickDrillIn()))
}

func TestAssistantDiagramKeys(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	text := "```mermaid\nA\n```\n\n```mermaid\nB\n```\n"
	item := NewAssistantMessageItem(&sty, finishedMessage("m1", message.TextContent{Text: text})).(*AssistantMessageItem)

	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: 'v', Text: "v"})
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n"}, cmdMsg(t, cmd), "v views the first diagram")

	handled, cmd = item.HandleKeyEvent(tea.KeyPressMsg{Code: 'o', Text: "o"})
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n", External: true}, cmdMsg(t, cmd), "o opens it in the browser")

	plain := NewAssistantMessageItem(&sty, finishedMessage("m2", message.TextContent{Text: "no diagrams"})).(*AssistantMessageItem)
	handled, _ = plain.HandleKeyEvent(tea.KeyPressMsg{Code: 'v', Text: "v"})
	require.False(t, handled)
}

func TestDiagramDetailItem(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	ready := false
	item := NewDiagramDetailItem(&sty, "A\n", func(source string, width int) (string, bool) {
		return "[[FULL " + source[:1] + "]]", ready
	})
	require.Contains(t, ansi.Strip(item.RawRender(80)), "Rendering diagram")
	ready = true
	out := ansi.Strip(item.RawRender(80))
	require.Contains(t, out, "[[FULL A]]")
	require.Contains(t, out, "open in browser")

	handled, cmd := item.HandleKeyEvent(tea.KeyPressMsg{Code: 'o', Text: "o"})
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n", External: true}, cmdMsg(t, cmd))
	handled, _ = item.HandleKeyEvent(tea.KeyPressMsg{Code: 'x', Text: "x"})
	require.False(t, handled)
}
