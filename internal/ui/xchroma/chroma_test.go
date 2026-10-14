package xchroma

import (
	"bytes"
	"image/color"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFormatterKeepsColumnsAfterMultiLineTokens(t *testing.T) {
	t.Parallel()

	// The TypeScript lexer emits each line comment as one token that
	// includes its trailing newline, so a styled comment token spans two
	// lines.
	src := strings.Join([]string{
		"a -= b;          // short comment",
		"c -= d;",
		"// a much longer comment line on its own",
		"e -= f;",
		"",
	}, "\n")

	style := chroma.MustNewStyle("test", chroma.StyleEntries{
		chroma.Comment: "#888888",
		chroma.Keyword: "bold",
	})
	it, err := chroma.Coalesce(lexers.Get("typescript")).Tokenise(nil, src)
	require.NoError(t, err)

	var b strings.Builder
	require.NoError(t, Formatter(nil, nil).Format(&b, style, it))

	require.Equal(t, src, ansi.Strip(b.String()))
}

func TestFormatterPreservesIndentationAfterComments(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		// JavaScript's CommentSingle token includes the trailing newline.
		"js": "function f() {\n  // comment\n  const x = 1;\n  const y = 2; // inline\n}\n",
		"go": "func main() {\n\t// comment\n\tprintln(1)\n}\n",
		"sh": "if true; then\n  # comment\n  echo hi\nfi\n",
		"py": "def f():\n    # comment\n    x = 1\n",
	}

	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			lexer := chroma.Coalesce(MatchLexer("file." + name))
			require.NotNil(t, lexer)
			it, err := lexer.Tokenise(nil, src)
			require.NoError(t, err)

			var buf bytes.Buffer
			require.NoError(t, Formatter(color.Black, nil).Format(&buf, styles.Get("charm"), it))

			got := ansi.Strip(buf.String())
			// Lip Gloss expands tabs, so compare against the tab-expanded
			// source; what matters is that no stray padding is added.
			want := strings.ReplaceAll(src, "\t", "    ")
			require.Equal(t, strings.Split(want, "\n"), strings.Split(got, "\n"))
		})
	}
}
