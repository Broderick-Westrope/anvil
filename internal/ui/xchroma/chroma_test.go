package xchroma

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
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
