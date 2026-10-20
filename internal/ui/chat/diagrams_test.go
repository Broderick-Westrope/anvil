package chat

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestSplitMermaid(t *testing.T) {
	t.Parallel()

	content := "Intro\n\n```mermaid\nflowchart LR\n  A --> B\n```\n\n```go\n```mermaid inside go\n```\n\nOutro\n\n```mermaid\nunclosed"
	segs := splitMermaid(content)
	require.Equal(t, []markdownSegment{
		{text: "Intro\n\n"},
		{text: "flowchart LR\n  A --> B\n", mermaid: true},
		{text: "\n```go\n```mermaid inside go\n```\n\nOutro\n\n```mermaid\nunclosed"},
	}, segs)
}

func TestSplitMermaidLeavesNestedFences(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"list item":  "1. Step\n\n   ```mermaid\n   A --> B\n   ```\n\n2. Next\n",
		"blockquote": "> ```mermaid\n> A --> B\n> ```\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, []markdownSegment{{text: content}}, splitMermaid(content))
		})
	}
}

type fakeDiagrams map[string]string

func (f fakeDiagrams) Diagram(source string, _ int) (string, bool) {
	out, ok := f[source]
	return out, ok
}

func finishedMessage(id string, parts ...message.ContentPart) *message.Message {
	parts = append(parts, message.Finish{Reason: message.FinishReasonEndTurn})
	return &message.Message{ID: id, Role: message.Assistant, Parts: parts}
}

func cmdMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	require.NotNil(t, cmd)
	return cmd()
}

// nodeSummary describes each node as "<id> text:<text>" or
// "<id> diagram:<source>".
func nodeSummary(items []MessageItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		key := ""
		if k, ok := item.(SegmentKeyed); ok {
			key = k.SegmentKey()
		}
		kind, value, _ := strings.Cut(key, ":")
		out[i] = item.ID() + " " + kind + ":" + strings.TrimSpace(value)
	}
	return out
}

func TestNewAssistantMessageItemsSplitsInDocumentOrder(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	text := "Intro\n\n```mermaid\nA\n```\n\nMiddle\n\n```mermaid\nB\n```\n\n```mermaid\nC\n```\n\nOutro\n"
	items := NewAssistantMessageItems(&sty, finishedMessage("m", message.TextContent{Text: text}))
	require.Equal(t, []string{
		"m text:Intro",
		"m#1 diagram:A",
		"m#2 text:Middle",
		"m#3 diagram:B",
		"m#4 diagram:C",
		"m#5 text:Outro",
	}, nodeSummary(items), "blank text between adjacent diagrams makes no node")
}

func TestNewAssistantMessageItemsHead(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reasoning := message.ReasoningContent{Thinking: "hmm", StartedAt: testStartedAt, FinishedAt: testFinishedAt}
	tests := map[string]struct {
		parts []message.ContentPart
		want  []string
	}{
		"text first": {
			parts: []message.ContentPart{message.TextContent{Text: "Before\n\n```mermaid\nA\n```\n"}},
			want:  []string{"m text:Before", "m#1 diagram:A"},
		},
		"diagram first, no thinking": {
			parts: []message.ContentPart{message.TextContent{Text: "```mermaid\nA\n```\n\nAfter\n"}},
			want:  []string{"m diagram:A", "m#1 text:After"},
		},
		"blank lines, then a diagram": {
			parts: []message.ContentPart{message.TextContent{Text: "\n\n```mermaid\nA\n```\n"}},
			want:  []string{"m diagram:A"},
		},
		"diagram first, with thinking": {
			parts: []message.ContentPart{reasoning, message.TextContent{Text: "```mermaid\nA\n```\n"}},
			want:  []string{"m text:", "m#1 diagram:A"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			items := NewAssistantMessageItems(&sty, finishedMessage("m", tc.parts...))
			require.Equal(t, tc.want, nodeSummary(items))
		})
	}
}

func TestNewAssistantMessageItemsKeepsOneNode(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	text := message.TextContent{Text: "```mermaid\nA\n```\n"}
	tests := map[string]*message.Message{
		"still streaming": {ID: "m", Role: message.Assistant, Parts: []message.ContentPart{text}},
		"canceled": {ID: "m", Role: message.Assistant, Parts: []message.ContentPart{
			text, message.Finish{Reason: message.FinishReasonCanceled},
		}},
		"no diagrams":    finishedMessage("m", message.TextContent{Text: "plain"}),
		"nested diagram": finishedMessage("m", message.TextContent{Text: "- item\n\n  ```mermaid\n  A\n  ```\n"}),
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			items := NewAssistantMessageItems(&sty, msg)
			require.Len(t, items, 1)
			require.Equal(t, "m", items[0].ID())
			require.Empty(t, items[0].(SegmentKeyed).SegmentKey(), "an unsplit message")
		})
	}
}

func TestSegmentsShowOnlyTheirOwnText(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reasoning := message.ReasoningContent{Thinking: "hmm", StartedAt: testStartedAt, FinishedAt: testFinishedAt}
	msg := finishedMessage("m", reasoning, message.TextContent{Text: "Intro\n\n```mermaid\nA\n```\n\nOutro\n"})
	items := NewAssistantMessageItems(&sty, msg)
	require.Len(t, items, 3)

	head := ansi.Strip(items[0].Render(80))
	require.Contains(t, head, "Thought")
	require.Contains(t, head, "Intro")
	require.NotContains(t, head, "Outro")

	tail := ansi.Strip(items[2].Render(80))
	require.Contains(t, tail, "Outro")
	require.NotContains(t, tail, "Thought", "only the head shows the thinking")
	require.NotContains(t, tail, "Intro")
	require.Equal(t, "Outro", items[2].(*AssistantMessageItem).SelectionSource())
}

//nolint:paralleltest // Mutates the package-level Diagrams hook.
func TestDiagramItemRender(t *testing.T) {
	prev := Diagrams
	t.Cleanup(func() { Diagrams = prev })

	sty := styles.TokyoNight()
	item := NewDiagramItem(&sty, finishedMessage("m"), 1, "flowchart LR\n  A --> B\n")

	Diagrams = nil
	out := ansi.Strip(item.Render(80))
	require.Contains(t, out, "A --> B", "without images, the code block")

	Diagrams = fakeDiagrams{"flowchart LR\n  A --> B\n": "[[IMG1]]\n[[IMG2]]"}
	out = ansi.Strip(item.Render(80))
	require.Equal(t, []string{"[[IMG1]]", "[[IMG2]]"}, strings.Fields(out))
}

func TestDiagramItemActions(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := finishedMessage("m")
	item := NewDiagramItem(&sty, msg, 2, "A\n")
	require.Equal(t, "m#2", item.ID())
	require.True(t, IsSegmentOf(item.ID(), "m"))
	require.False(t, IsSegmentOf(item.ID(), "m#"), "not a segment of a longer ID")
	require.Equal(t, "m", item.SourceMessage().ID, "actions on the message work from any node")

	for _, key := range []string{"right", "l", "v"} {
		handled, cmd := item.HandleKeyEvent(keyPress(key))
		require.True(t, handled, key)
		require.Equal(t, DiagramOpenMsg{Source: "A\n"}, cmdMsg(t, cmd), key)
	}
	handled, cmd := item.HandleKeyEvent(keyPress("o"))
	require.True(t, handled)
	require.Equal(t, DiagramOpenMsg{Source: "A\n", External: true}, cmdMsg(t, cmd))
	handled, _ = item.HandleKeyEvent(keyPress("x"))
	require.False(t, handled)

	require.True(t, item.HandleMouseClick(ansi.MouseLeft, 3, 7))
	require.False(t, item.HandleMouseClick(ansi.MouseRight, 3, 7))
	require.Equal(t, DiagramOpenMsg{Source: "A\n"}, cmdMsg(t, item.ClickDrillIn()))
}

func keyPress(key string) tea.KeyPressMsg {
	if key == "right" {
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}
