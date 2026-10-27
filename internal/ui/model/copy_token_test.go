package model

import (
	"slices"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"
	"github.com/stretchr/testify/require"
)

func TestFindWordBoundaries_CopyTokens(t *testing.T) {
	t.Parallel()

	const uuid = "3f2a9c1e-7b4d-4e8a-9f10-0c2b5d6e7a81"

	tests := map[string]struct {
		line   string
		marker string // the substring whose first byte is clicked
		want   string
	}{
		"url from its scheme": {
			line:   "see https://example.com/a/b?q=1 now",
			marker: "https",
			want:   "https://example.com/a/b?q=1",
		},
		"url from its path": {
			line:   "see https://example.com/a/b?q=1 now",
			marker: "/b",
			want:   "https://example.com/a/b?q=1",
		},
		"url from its last character": {
			line:   "see https://example.com/a/b?q=1 now",
			marker: "1 now",
			want:   "https://example.com/a/b?q=1",
		},
		"url drops a full stop": {
			line:   "Open https://example.com/docs.",
			marker: "docs",
			want:   "https://example.com/docs",
		},
		"url drops stacked punctuation": {
			line:   "Really https://example.com/x?!",
			marker: "example",
			want:   "https://example.com/x",
		},
		"url drops an unbalanced paren": {
			line:   "(https://example.com/x)",
			marker: "example",
			want:   "https://example.com/x",
		},
		"url drops a paren and full stop": {
			line:   "(see https://example.com/x).",
			marker: "example",
			want:   "https://example.com/x",
		},
		"url drops an unbalanced bracket": {
			line:   "[https://example.com/x]",
			marker: "example",
			want:   "https://example.com/x",
		},
		"url drops an unbalanced brace": {
			line:   "{https://example.com/x}",
			marker: "example",
			want:   "https://example.com/x",
		},
		"url keeps balanced parens": {
			line:   "Read https://en.wikipedia.org/wiki/Go_(programming_language) first",
			marker: "wiki/Go",
			want:   "https://en.wikipedia.org/wiki/Go_(programming_language)",
		},
		"url wins over a uuid inside it": {
			line:   "https://linear.app/x/" + uuid,
			marker: uuid,
			want:   "https://linear.app/x/" + uuid,
		},
		"second url on the line": {
			line:   "https://a.example/one and https://b.example/two",
			marker: "b.example",
			want:   "https://b.example/two",
		},
		"url after wide characters": {
			line:   "日本語 https://example.com/x",
			marker: "example",
			want:   "https://example.com/x",
		},
		"uuid from a hyphen": {
			line:   "id " + uuid + " done",
			marker: "-4e8a",
			want:   uuid,
		},
		"uppercase uuid": {
			line:   "ID=3F2A9C1E-7B4D-4E8A-9F10-0C2B5D6E7A81",
			marker: "7B4D",
			want:   "3F2A9C1E-7B4D-4E8A-9F10-0C2B5D6E7A81",
		},
		"issue id from its team key": {
			line:   "Fixes ENG-1234.",
			marker: "ENG",
			want:   "ENG-1234",
		},
		"issue id from its number": {
			line:   "Fixes ENG-1234.",
			marker: "234",
			want:   "ENG-1234",
		},
		"issue id with a digit in its key": {
			line:   "see A11Y-7",
			marker: "A11Y",
			want:   "A11Y-7",
		},
		"lowercase issue id stays a word": {
			line:   "branch eng-1234",
			marker: "eng",
			want:   "eng",
		},
		"one-letter key stays a word": {
			line:   "plan A-1",
			marker: "A-1",
			want:   "A",
		},
		"too-long uuid group stays a word": {
			line:   "3f2a9c1e-7b4d-4e8a-9f10-0c2b5d6e7a81ff",
			marker: "7b4d",
			want:   "7b4d",
		},
		"cuid is one word": {
			line:   "order clx8k2m9q0000abcd1234efgh",
			marker: "clx8",
			want:   "clx8k2m9q0000abcd1234efgh",
		},
		"prose before a token": {
			line:   "see ENG-1",
			marker: "see",
			want:   "see",
		},
		"prose after a url": {
			line:   "https://example.com/x then",
			marker: "then",
			want:   "then",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			col := columnOf(t, tc.line, tc.marker)
			start, end := findWordBoundaries(tc.line, col)
			require.Equal(t, tc.want, sliceColumns(tc.line, start, end))
		})
	}
}

func TestFindWordBoundaries_SpaceAfterURLSelectsNothing(t *testing.T) {
	t.Parallel()

	line := "https://example.com/x then"
	col := columnOf(t, line, " then")
	start, end := findWordBoundaries(line, col)
	require.Equal(t, col, start)
	require.Equal(t, col, end)
}

// columnOf returns the display column of marker's first byte in line.
func columnOf(t *testing.T, line, marker string) int {
	t.Helper()
	idx := strings.Index(line, marker)
	require.GreaterOrEqual(t, idx, 0, "marker %q not in %q", marker, line)
	return displaywidth.String(line[:idx])
}

// sliceColumns returns the text of line between display columns start and
// end.
func sliceColumns(line string, start, end int) string {
	var out []rune
	col := 0
	for _, r := range line {
		if col >= start && col < end {
			out = append(out, r)
		}
		col += displaywidth.Rune(r)
	}
	return string(out)
}

func TestDoubleClickCopiesWholeTokens(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		marker string
		want   string
	}{
		"url":      {marker: "example", want: "https://example.com/a-b/c"},
		"uuid":     {marker: "7b4d", want: "3f2a9c1e-7b4d-4e8a-9f10-0c2b5d6e7a81"},
		"issue id": {marker: "1234", want: "ENG-1234"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			com := common.DefaultCommon(nil)
			text := "Open https://example.com/a-b/c for 3f2a9c1e-7b4d-4e8a-9f10-0c2b5d6e7a81 in ENG-1234.\n"
			c := NewChat(com)
			c.SetSize(120, 10)
			c.SetMessages(chat.NewAssistantMessageItems(com.Styles, finishedText("m", text), nil)...)

			item := c.MessageItems()[0].(list.RawRenderable)
			lines := strings.Split(ansi.Strip(item.RawRender(c.list.Width())), "\n")
			row := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, tc.marker) })
			require.GreaterOrEqual(t, row, 0)

			c.selectWord(0, columnOf(t, lines[row], tc.marker)+chat.MessageLeftPaddingTotal, row)
			c.list.Render()
			require.Equal(t, tc.want, c.HighlightContent())
		})
	}
}
