package mermaid

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMeasurerPicksTheFaceForTheStyle(t *testing.T) {
	t.Parallel()

	m, err := newMeasurer()
	require.NoError(t, err)
	// "To" kerns differently in every face, so each width is distinct.
	tests := map[string]struct {
		style int
		want  float64
	}{
		"regular":            {style: 0, want: 16.84375},
		"bold":               {style: styleBold, want: 19.78125},
		"italic":             {style: styleItalic, want: 17.5859375},
		"bold italic":        {style: styleBold | styleItalic, want: 19.84375},
		"other bits ignored": {style: 4 | styleBold, want: 19.78125},
	}
	for name, tc := range tests {
		require.Equal(t, tc.want, m.width("To", 16, tc.style), name)
	}
}

func TestMeasurerWidth(t *testing.T) {
	t.Parallel()

	m, err := newMeasurer()
	require.NoError(t, err)
	width := func(s string) float64 { return m.width(s, 16, 0) }

	require.Equal(t, 10.140625, width("n"))
	require.Equal(t, 2*width("n"), m.width("n", 32, 0), "width scales with size")
	require.Equal(t, width("a")+width("b"), width("ab"), "advances add up")
	require.Equal(t, 20.8671875, width("AV"), "kerning pulls A and V together")
	require.Less(t, width("AV"), width("A")+width("V"))
	require.Equal(t, width("n"), width("中"), "a missing glyph measures as n")
	require.Equal(t, 2*width("n"), width("中中"))
}
