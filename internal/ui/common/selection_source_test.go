package common

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func renderPlain(t *testing.T, source string) string {
	t.Helper()
	return renderPlainWidth(t, source, 80)
}

func renderPlainWidth(t *testing.T, source string, width int) string {
	t.Helper()
	sty := styles.TokyoNight()
	renderer := MarkdownRenderer(&sty, width)
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

// Glamour repeats table link URLs in a reference list below the table.
// That list has no counterpart in the source and shares text with an
// email address further down, so it must not pull the alignment past the
// content in between.
func TestMapSelectionToSource_TableLinkReferences(t *testing.T) {
	t.Parallel()

	const url = "docs.example.com/document/d/7qWzR2kLmN4pXvB8sTyH3jFdC6gA9eUo1iKwZ5-rPtMx"
	source := strings.Join([]string{
		"| Date | From | What |",
		"|---|---|---|",
		"| 19 Feb | Alice ([link](https://example.enterprise.slack.com/archives/C1/p1)) | Sets up team@example.vc |",
		"| 26 Feb | Bob ([link](https://example.enterprise.slack.com/archives/C1/p2)) | Slides |",
		"",
		"## Documents",
		"",
		"1. **Rollout guide for the payments team (June):** `" + url + "`",
		"2. **Second guide:** `docs.example.com/document/d/4hGtQ8wZrYv2Ln`",
		"",
		"Email team@example.vc to confirm the launch date.",
	}, "\n")

	for width := 40; width <= 120; width += 4 {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			t.Parallel()
			plain := renderPlainWidth(t, source, width)
			from := strings.Index(plain, "(June):")
			require.GreaterOrEqual(t, from, 0, plain)
			start := from + strings.Index(plain[from:], "docs.")
			end := start + strings.Index(plain[start:], "rPtMx") + len("rPtMx")

			got, ok := MapSelectionToSource(source, plain, start, end)
			require.True(t, ok)
			require.Equal(t, "`"+url+"`", got)
		})
	}
}

// A selection that is mostly rendered-only text can't be trusted to map to
// the source, so the caller falls back to the rendered text instead of
// copying the few runes that happened to match.
func TestMapSelectionToSource_MostlyDecoration(t *testing.T) {
	t.Parallel()

	source := "hello world"
	rendered := "Note: qqqq zzzz\nhello world"
	start := strings.Index(rendered, "qqqq")
	end := strings.Index(rendered, "hello") + len("he")
	_, ok := MapSelectionToSource(source, rendered, start, end)
	require.False(t, ok)
}

func TestMapSelectionToSource_AmbiguousText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		source   string
		from, to string
		want     string
	}{
		"text repeated in a hidden link title": {
			source: `Intro **deployment** instructions for production environments. ` +
				`[x](https://e.co "deployment instructions for production environments") End.`,
			from: "deployment",
			to:   "deployment",
			want: "**deployment**",
		},
		"rune repeated in a hidden link title": {
			source: `Read [guide](https://example.com "x") **x** abcdefghijklmnop.`,
			from:   "x ",
			to:     "abcdefghijklmnop",
			want:   "**x** abcdefghijklmnop",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plain := renderPlain(t, tc.source)
			start, end := selectBetween(t, plain, tc.from, tc.to)
			got, ok := MapSelectionToSource(tc.source, plain, start, end)
			require.True(t, ok, plain)
			require.Equal(t, tc.want, got, plain)
		})
	}
}

// When the selection's first or last word rune has no source match, the
// mapped range would cut the selection short, so the caller falls back to
// the rendered text. Here the cell's URL also appears, hidden, as the
// link's target, and the "[1]" link reference stops the end of the cell's
// URL from matching.
func TestMapSelectionToSource_UnmappedEndpoint(t *testing.T) {
	t.Parallel()

	source := strings.Join([]string{
		"| Name | Description |",
		"|---|---|",
		"| [link](https://example.com/abcdefghij) | `https://example.com/abcdefghij` |",
		"",
		"Footer text.",
	}, "\n")
	plain := renderPlain(t, source)
	start, end := selectBetween(t, plain, "https", "abcdefghij")
	_, ok := MapSelectionToSource(source, plain, start, end)
	require.False(t, ok, plain)
}
