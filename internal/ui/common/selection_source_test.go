package common

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func renderPlain(t *testing.T, source string) string {
	t.Helper()
	sty := styles.TokyoNight()
	renderer := MarkdownRenderer(&sty, 80)
	mu := LockMarkdownRenderer(renderer)
	mu.Lock()
	out, err := renderer.Render(source)
	mu.Unlock()
	require.NoError(t, err)
	return ansi.Strip(out)
}

// selectBetween returns the byte range in plain spanning from the first
// occurrence of from to the end of the first following occurrence of to.
func selectBetween(t *testing.T, plain, from, to string) (int, int) {
	t.Helper()
	start := strings.Index(plain, from)
	require.GreaterOrEqual(t, start, 0, "rendered text missing %q:\n%s", from, plain)
	rel := strings.Index(plain[start:], to)
	require.GreaterOrEqual(t, rel, 0, "rendered text missing %q:\n%s", to, plain)
	return start, start + rel + len(to)
}

func TestMapSelectionToSource(t *testing.T) {
	t.Parallel()

	source := strings.Join([]string{
		"# Title",
		"",
		"Intro with `inline code` and **bold text** here.",
		"",
		"- first item",
		"- second `item`",
		"",
		"```go",
		"func main() {",
		"\tfmt.Println(\"hi\")",
		"}",
		"```",
		"",
		"Closing paragraph.",
	}, "\n")

	tests := []struct {
		name     string
		from, to string
		want     string
	}{
		{
			name: "inline code keeps backticks",
			from: "inline",
			to:   "code",
			want: "`inline code`",
		},
		{
			name: "bold keeps markers",
			from: "with",
			to:   "text",
			want: "with `inline code` and **bold text**",
		},
		{
			name: "list items keep bullets",
			from: "first",
			to:   "first item",
			want: "- first item",
		},
		{
			name: "full code block keeps fences",
			from: "func main",
			to:   "}",
			want: "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```",
		},
		{
			name: "partial code line stays plain",
			from: "fmt.Println",
			to:   "(\"hi\")",
			want: "fmt.Println(\"hi\")",
		},
		{
			name: "selection crossing into code block closes fence",
			from: "second",
			to:   "func main() {",
			want: "- second `item`\n\n```go\nfunc main() {\n```",
		},
		{
			name: "whole message returns source",
			from: "Title",
			to:   "Closing paragraph.",
			want: source,
		},
	}

	plain := renderPlain(t, source)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start, end := selectBetween(t, plain, tt.from, tt.to)
			got, ok := MapSelectionToSource(source, plain, start, end)
			require.True(t, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMapSelectionToSource_DecorationOnly(t *testing.T) {
	t.Parallel()

	source := "hello world"
	rendered := "Thinking:\n\nhello world"
	_, ok := MapSelectionToSource(source, rendered, 0, len("Thinking:"))
	require.False(t, ok)
}
