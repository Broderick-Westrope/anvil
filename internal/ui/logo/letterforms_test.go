package logo

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/require"
)

func TestAnvilLetterforms(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		letter   letterform
		expected string
	}{
		"A": {
			letter: LetterA,
			expected: "" +
				"▄▀▀▀▄\n" +
				"█▀▀▀█\n" +
				"▀   ▀",
		},
		"N": {
			letter: LetterN,
			expected: "" +
				"█▄  █\n" +
				"█ ▀▄█\n" +
				"▀   ▀",
		},
		"V": {
			letter: LetterV,
			expected: "" +
				"█   █\n" +
				"▀▄ ▄▀\n" +
				"  ▀  ",
		},
		"I": {
			letter: LetterI,
			expected: "" +
				"▀▀█▀▀\n" +
				"  █  \n" +
				"▀▀▀▀▀",
		},
		"L": {
			letter: LetterL,
			expected: "" +
				"█    \n" +
				"█    \n" +
				"▀▀▀▀▀",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// Static: stretch arg must have no effect.
			require.Equal(t, tt.letter(false), tt.letter(true),
				"stretch must be a no-op for static letter %s", name)

			got := tt.letter(false)
			require.Equal(t, tt.expected, got)

			// All rows must have the same cell width.
			rows := strings.Split(got, "\n")
			require.Len(t, rows, 3, "letter %s must have exactly 3 rows", name)
			w0 := lipgloss.Width(rows[0])
			for i, row := range rows[1:] {
				require.Equal(t, w0, lipgloss.Width(row),
					"letter %s row %d width mismatch", name, i+1)
			}
		})
	}
}

// TestAnvilLetterformsConsistentWidth ensures all ANVIL letters have the same
// cell width so they align correctly when rendered side by side.
func TestAnvilLetterformsConsistentWidth(t *testing.T) {
	t.Parallel()

	letters := map[string]letterform{
		"A": LetterA,
		"N": LetterN,
		"V": LetterV,
		"I": LetterI,
		"L": LetterL,
	}

	want := lipgloss.Width(letters["A"](false))
	for name, fn := range letters {
		got := lipgloss.Width(fn(false))
		require.Equal(t, want, got,
			"letter %s has width %d, want %d (same as A)", name, got, want)
	}
}
