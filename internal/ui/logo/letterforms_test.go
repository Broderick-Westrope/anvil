package logo

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

func TestAnvilLetterforms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		letter   letterform
		expected string
	}{
		{
			name:   "A",
			letter: LetterA,
			expected: "" +
				"▄▀▀▀▄\n" +
				"█▀▀▀█\n" +
				"▀   ▀",
		},
		{
			name:   "N",
			letter: LetterN,
			expected: "" +
				"█▄  █\n" +
				"█ ▀▄█\n" +
				"▀   ▀",
		},
		{
			name:   "V",
			letter: LetterV,
			expected: "" +
				"█   █\n" +
				"▀▄ ▄▀\n" +
				"  ▀  ",
		},
		{
			name:   "I",
			letter: LetterI,
			expected: "" +
				"▄▄▄▄▄\n" +
				"  █  \n" +
				"▀▀▀▀▀",
		},
		{
			name:   "L",
			letter: LetterL,
			expected: "" +
				"█    \n" +
				"█    \n" +
				"▀▀▀▀▀",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Static: stretch arg must have no effect.
			require.Equal(t, tt.letter(false), tt.letter(true),
				"stretch must be a no-op for static letter %s", tt.name)

			got := tt.letter(false)
			require.Equal(t, tt.expected, got)

			// All rows must have the same cell width.
			rows := strings.Split(got, "\n")
			require.Len(t, rows, 3, "letter %s must have exactly 3 rows", tt.name)
			w0 := lipgloss.Width(rows[0])
			for i, row := range rows[1:] {
				require.Equal(t, w0, lipgloss.Width(row),
					"letter %s row %d width mismatch", tt.name, i+1)
			}
		})
	}
}

// TestAnvilLetterformsConsistentWidth ensures all ANVIL letters have the same
// cell width so they align correctly when rendered side by side.
func TestAnvilLetterformsConsistentWidth(t *testing.T) {
	t.Parallel()

	letters := []struct {
		name string
		fn   letterform
	}{
		{"A", LetterA},
		{"N", LetterN},
		{"V", LetterV},
		{"I", LetterI},
		{"L", LetterL},
	}

	want := lipgloss.Width(letters[0].fn(false))
	for _, l := range letters[1:] {
		got := lipgloss.Width(l.fn(false))
		require.Equal(t, want, got,
			"letter %s has width %d, want %d (same as A)", l.name, got, want)
	}
}
