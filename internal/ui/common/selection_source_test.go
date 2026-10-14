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

// Text hidden by rendering (link titles, reference definitions, comments)
// or repeated in a code block can pair a selected word with the wrong
// source. The copy must then fall back to the rendered text rather than
// include source text the user didn't select.
func TestMapSelectionToSource_NeverCopiesUnselectedText(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		source   string
		from, to string
		// nth picks which occurrence of from in the rendered text starts
		// the selection.
		nth int
		// wantOK is whether the selection maps; when it does, it must map
		// to exactly the selected word.
		wantOK bool
	}{
		"word repeated in a link title and the prose after it": {
			source: `Intro. production status [guide](https://e.co "production status") ` +
				`production status Follow **production status** carefully.`,
			from: "production",
			to:   "production",
			nth:  1,
		},
		"code block repeating its own comment": {
			source: "Intro.\n\n```html\nTODO: update configuration " +
				"<!-- deployment instructions for TODO: update configuration --> " +
				"TODO: update configuration\n```\n\n" +
				"Follow **deployment instructions for TODO: update configuration** carefully.",
			from:   "configuration",
			to:     "configuration",
			nth:    2,
			wantOK: true,
		},
		"word repeated in a link title after a repeated link": {
			source: `Prefix. [link](https://e.co "deployment") [link](https://e.co "other") deployment Suffix.`,
			from:   "deployment",
			to:     "deployment",
		},
		"word repeated in a reference definition": {
			source: "Prefix. [link][1] [link][2] deployment Suffix.\n\n" +
				"[1]: https://e.co \"deployment\"\n[2]: https://e.co \"other\"",
			from: "deployment",
			to:   "deployment",
		},
		"word repeated in an html comment": {
			source: "Prefix. https://e.co <!-- deployment --> https://e.co <!-- other --> deployment Suffix.",
			from:   "deployment",
			to:     "deployment",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			plain := renderPlain(t, tc.source)
			start := -1
			for range tc.nth + 1 {
				next := strings.Index(plain[start+1:], tc.from)
				require.GreaterOrEqual(t, next, 0, plain)
				start += next + 1
			}
			got, ok := MapSelectionToSource(tc.source, plain, start, start+len(tc.to))
			require.Equal(t, tc.wantOK, ok, plain)
			if ok {
				require.Equal(t, tc.to, got, plain)
			}
		})
	}
}

// These tests use hand-built rendered text so each case isolates one rule
// for when a selection maps and when it falls back.
func TestMapSelectionToSource_Rules(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		source, rendered string
		// from and to are the first and last selected text in rendered.
		from, to string
		want     string
		wantOK   bool
	}{
		"selection at the source start maps": {
			source: "hello world", rendered: "hello world",
			from: "hello", to: "hello",
			want: "hello", wantOK: true,
		},
		"selection from the first mapped rune keeps leading source": {
			source: "<!--c-->hello world", rendered: "hello world",
			from: "hello", to: "hello",
			want: "<!--c-->hello", wantOK: true,
		},
		"mapped rune at the line start keeps the block prefix out": {
			source: "x\n- beta", rendered: "x beta",
			from: "beta", to: "beta",
			want: "beta", wantOK: true,
		},
		"unmapped first word falls back": {
			source: "hello world", rendered: "Note hello world",
			from: "Note", to: "world",
		},
		"unselected unmapped words don't count": {
			source: "hello world", rendered: "Note hello world",
			from: "hello", to: "world",
			want: "hello world", wantOK: true,
		},
		"unmapped rune inside a word is tolerated": {
			source: "hello world", rendered: "hellqo world",
			from: "hellqo", to: "world",
			want: "hello world", wantOK: true,
		},
		// The q runs have no source match, and neither do b, c and d,
		// because the words after them don't confirm a match.
		"exactly half the words mapped maps": {
			source: "abcd efg", rendered: "abcd q efg",
			from: "abcd", to: "efg",
			want: "abcd efg", wantOK: true,
		},
		"under half the words mapped falls back": {
			source: "abcd efg", rendered: "abcd qq efg",
			from: "abcd", to: "efg",
		},
		"punctuation-only selection maps": {
			source: "x --- y", rendered: "x --- y",
			from: "---", to: "---",
			want: "---", wantOK: true,
		},
		"decoration-only selection falls back": {
			source: "hello", rendered: "│ hello",
			from: "│", to: "│",
		},
		// The comment repeats the letters after a, so a maps to the
		// source start and bcde to the visible text after the comment.
		"word split by hidden text falls back": {
			source: "a<!--bcd-->bcde", rendered: "abcde",
			from: "abcde", to: "abcde",
		},
		"selection ending inside a split word maps": {
			source: "a<!--bcd-->bcde", rendered: "abcde",
			from: "a", to: "a",
			want: "a", wantOK: true,
		},
		"split word before the selection is ignored": {
			source: "a<!--bcd-->bcde fg", rendered: "abcde fg",
			from: "fg", to: "fg",
			want: "fg", wantOK: true,
		},
		"hidden text between a word and punctuation maps": {
			source: "x<!--c-->.", rendered: "x.",
			from: "x.", to: "x.",
			want: "x<!--c-->.", wantOK: true,
		},
		"hidden text between two words maps": {
			source: "x<!--abc--> abcd", rendered: "x abcd",
			from: "x", to: "abcd",
			want: "x<!--abc--> abcd", wantOK: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			start, end := selectBetween(t, tc.rendered, tc.from, tc.to)
			got, ok := MapSelectionToSource(tc.source, tc.rendered, start, end)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestAnchorChain(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		rendered, source string
		want             []anchor
	}{
		"text exactly one anchor long": {
			rendered: "abcdefghij", source: "abcdefghij",
			want: []anchor{{rendered: 0, source: 0}},
		},
		"anchor after a rendered decoration": {
			rendered: "│abcdefghij", source: "abcdefghij",
			want: []anchor{{rendered: 1, source: 0}},
		},
		"run repeated in the source is not an anchor": {
			rendered: "abcdefghij", source: "abcdefghijabcdefghij",
			want: []anchor{},
		},
		"out of order runs keep the longest ordered chain": {
			rendered: "abcdefghijklmnopqrstuvwxyzABCD",
			source:   "klmnopqrstabcdefghijuvwxyzABCD",
			want:     []anchor{{rendered: 10, source: 0}, {rendered: 20, source: 20}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := anchorChain([]rune(tc.rendered), []rune(tc.source))
			require.Equal(t, tc.want, got)
		})
	}
}
