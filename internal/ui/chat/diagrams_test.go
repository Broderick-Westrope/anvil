package chat

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
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

type fakeDiagrams map[string]string

func (f fakeDiagrams) Diagram(source string, _ int) (string, bool) {
	out, ok := f[source]
	return out, ok
}

//nolint:paralleltest // Mutates the package-level Diagrams hook.
func TestRenderWithDiagrams(t *testing.T) {
	renderer, err := glamour.NewTermRenderer(glamour.WithStandardStyle("notty"), glamour.WithWordWrap(80))
	require.NoError(t, err)
	content := "Before\n\n```mermaid\nA\n```\n\n```mermaid\nB\n```\n\nAfter\n"

	prev := Diagrams
	t.Cleanup(func() { Diagrams = prev })

	Diagrams = fakeDiagrams{}
	_, _, ok := renderWithDiagrams(content, 80, renderer)
	require.False(t, ok, "no diagram ready keeps the monolithic render")

	Diagrams = fakeDiagrams{"A\n": "[[IMAGE A]]\n[[ROW 2]]"}
	out, spans, ok := renderWithDiagrams(content, 80, renderer)
	require.True(t, ok)
	lines := strings.Split(out, "\n")
	require.Len(t, spans, 1)
	require.Equal(t, "A\n", spans[0].source)
	require.Equal(t, []string{"[[IMAGE A]]", "[[ROW 2]]"}, lines[spans[0].start:spans[0].end])
	require.Contains(t, out, "[[IMAGE A]]")
	require.Contains(t, out, "B", "a diagram that isn't ready stays a code block")
	require.Less(t, strings.Index(out, "Before"), strings.Index(out, "[[IMAGE A]]"))
	require.Less(t, strings.Index(out, "[[IMAGE A]]"), strings.Index(out, "After"))
}

func TestDiagramSources(t *testing.T) {
	t.Parallel()

	require.Nil(t, DiagramSources("no diagrams"))
	require.Equal(t, []string{"A\n", "B\n"}, DiagramSources("```mermaid\nA\n```\ntext\n```mermaid\nB\n```\n```mermaid\nopen"))
}
