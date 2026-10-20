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

func TestSplitMermaidLeavesExamplesInsideOtherFences(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"tilde fence":          "~~~markdown\n```mermaid\nA --> B\n```\n~~~\n",
		"longer backtick":      "````markdown\n```mermaid\nA --> B\n```\n````\n",
		"longer, then outro":   "````md\n```mermaid\nA\n```\n````\n\nOutro\n",
		"info string not ours": "```mermaidjs\nA\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, []markdownSegment{{text: content}}, splitMermaid(content))
		})
	}
}

func TestSplitMermaidFenceVariants(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		content string
		want    []markdownSegment
	}{
		"tilde mermaid": {
			content: "~~~mermaid\nA\n~~~\n",
			want:    []markdownSegment{{text: "A\n", mermaid: true}},
		},
		"longer fence holds a shorter one": {
			content: "````mermaid\nA\n```\nB\n````\n",
			want:    []markdownSegment{{text: "A\n```\nB\n", mermaid: true}},
		},
		"other fence char does not close": {
			content: "```mermaid\nA\n~~~\n```\n",
			want:    []markdownSegment{{text: "A\n~~~\n", mermaid: true}},
		},
		"info string with attributes": {
			content: "```mermaid title\nA\n```\n",
			want:    []markdownSegment{{text: "A\n", mermaid: true}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, splitMermaid(tc.content))
		})
	}
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

// fakeDiagrams answers with a fixed view per source; any other source is
// pending.
type fakeDiagrams struct {
	inline, full map[string]DiagramView
}

func (f fakeDiagrams) Diagram(source string, _ int) DiagramView     { return f.inline[source] }
func (f fakeDiagrams) FullDiagram(source string, _ int) DiagramView { return f.full[source] }

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
	items := NewAssistantMessageItems(&sty, finishedMessage("m", message.TextContent{Text: text}), fakeDiagrams{})
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
			items := NewAssistantMessageItems(&sty, finishedMessage("m", tc.parts...), fakeDiagrams{})
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
			items := NewAssistantMessageItems(&sty, msg, fakeDiagrams{})
			require.Len(t, items, 1)
			require.Equal(t, "m", items[0].ID())
			require.Empty(t, items[0].(SegmentKeyed).SegmentKey(), "an unsplit message")
		})
	}
}

func TestNewAssistantMessageItemsWithoutRendererKeepsOneNode(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := finishedMessage("m", message.TextContent{Text: "Intro\n\n```mermaid\nA\n```\n"})
	items := NewAssistantMessageItems(&sty, msg, nil)
	require.Len(t, items, 1, "diagrams turned off")
	out := ansi.Strip(items[0].Render(80))
	require.Contains(t, out, "Intro")
	require.Contains(t, out, "A", "the diagram stays a code block")
}

func TestSegmentsShowOnlyTheirOwnText(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	reasoning := message.ReasoningContent{Thinking: "hmm", StartedAt: testStartedAt, FinishedAt: testFinishedAt}
	msg := finishedMessage("m", reasoning, message.TextContent{Text: "Intro\n\n```mermaid\nA\n```\n\nOutro\n"})
	items := NewAssistantMessageItems(&sty, msg, fakeDiagrams{})
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

func TestDiagramItemRender(t *testing.T) {
	t.Parallel()

	const src = "flowchart LR\n  A --> B\n"
	sty := styles.TokyoNight()
	tests := map[string]struct {
		view     DiagramView
		want     []string
		wantNot  []string
		maxWidth int
	}{
		"pending shows the code block": {
			view:    DiagramView{State: DiagramPending},
			want:    []string{"A --> B"},
			wantNot: []string{"not rendered"},
		},
		"pending ignores a stand-in": {
			view:    DiagramView{State: DiagramPending, Image: "[[IMG]]"},
			want:    []string{"A --> B"},
			wantNot: []string{"[[IMG]]"},
		},
		"unsupported shows the code block without a note": {
			view:    DiagramView{State: DiagramUnsupported},
			want:    []string{"A --> B"},
			wantNot: []string{"not rendered"},
		},
		"failed shows the code block and why": {
			view: DiagramView{State: DiagramFailed, Err: "unsupported diagram type\n\"gantt\""},
			want: []string{"A --> B", `Diagram not rendered: unsupported diagram type "gantt"`},
		},
		"ready shows the image": {
			view:    DiagramView{State: DiagramReady, Image: "[[IMG1]]\n[[IMG2]]"},
			want:    []string{"[[IMG1]]", "[[IMG2]]"},
			wantNot: []string{"A --> B"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := NewDiagramItem(&sty, finishedMessage("m"), 1, src, fakeDiagrams{inline: map[string]DiagramView{src: tc.view}})
			out := ansi.Strip(item.Render(80))
			for _, want := range tc.want {
				require.Contains(t, out, want)
			}
			for _, not := range tc.wantNot {
				require.NotContains(t, out, not)
			}
		})
	}
}

func TestDiagramItemFailureNoteFitsTheWidth(t *testing.T) {
	t.Parallel()

	const src = "A\n"
	sty := styles.TokyoNight()
	view := DiagramView{State: DiagramFailed, Err: strings.Repeat("very long reason ", 20)}
	item := NewDiagramItem(&sty, finishedMessage("m"), 1, src, fakeDiagrams{inline: map[string]DiagramView{src: view}})
	for line := range strings.SplitSeq(item.Render(40), "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 40, line)
	}
	require.Contains(t, ansi.Strip(item.Render(40)), "Diagram not rendered: very")
}

func TestDiagramItemActions(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := finishedMessage("m")
	item := NewDiagramItem(&sty, msg, 2, "A\n", fakeDiagrams{})
	require.Equal(t, "m#2", item.ID())
	require.True(t, IsSegmentOf(item.ID(), "m"))
	require.False(t, IsSegmentOf(item.ID(), "m#"), "not a segment of a longer ID")
	require.Equal(t, "m", item.SourceMessage().ID, "actions on the message work from any node")

	handled, _ := item.HandleKeyEvent(keyPress("v"))
	require.False(t, handled, "v is not a diagram key")
	for _, key := range []string{"right", "l"} {
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

func TestDiagramCodeBlockHasNoBlankPaddingLines(t *testing.T) {
	t.Parallel()

	const src = "flowchart LR\n  A --> B\n"
	sty := styles.TokyoNight()
	failed := DiagramView{State: DiagramFailed, Err: "bad"}
	for name, view := range map[string]DiagramView{"code block": {}, "with a failure note": failed} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			item := NewDiagramItem(&sty, finishedMessage("m"), 1, src, fakeDiagrams{inline: map[string]DiagramView{src: view}})
			lines := strings.Split(ansi.Strip(item.RawRender(80)), "\n")
			require.NotEmpty(t, strings.TrimSpace(lines[0]), "first line")
			require.NotEmpty(t, strings.TrimSpace(lines[len(lines)-1]), "last line")
			require.Contains(t, lines[0], "flowchart LR")
		})
	}
}

