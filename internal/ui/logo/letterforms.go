package logo

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/slice"
)

// renderWord renders letterforms to fork a word. stretchIndex is the index of
// the letter to stretch, or -1 if no letter should be stretched.
func renderWord(spacing int, stretchIndex int, letterforms ...letterform) string {
	if spacing < 0 {
		spacing = 0
	}

	renderedLetterforms := make([]string, len(letterforms))

	// pick one letter randomly to stretch
	for i, letter := range letterforms {
		renderedLetterforms[i] = letter(i == stretchIndex)
	}

	if spacing > 0 {
		// Add spaces between the letters and render.
		renderedLetterforms = slice.Intersperse(renderedLetterforms, strings.Repeat(" ", spacing))
	}
	return strings.TrimSpace(
		lipgloss.JoinHorizontal(lipgloss.Top, renderedLetterforms...),
	)
}

// LetterA renders the letter A in a stylized way.
func LetterA(_ bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▄
	// █▀▀▀█
	// ▀   ▀
	return "▄▀▀▀▄\n█▀▀▀█\n▀   ▀"
}

// LetterI renders the letter I in a stylized way.
func LetterI(_ bool) string {
	// Here's what we're making:
	//
	// ▀▀█▀▀
	//   █
	// ▀▀▀▀▀
	return "▀▀█▀▀\n  █  \n▀▀▀▀▀"
}

// LetterL renders the letter L in a stylized way.
func LetterL(_ bool) string {
	// Here's what we're making:
	//
	// █
	// █
	// ▀▀▀▀▀
	return "█    \n█    \n▀▀▀▀▀"
}

// LetterN renders the letter N in a stylized way.
func LetterN(_ bool) string {
	// Here's what we're making:
	//
	// █▄  █
	// █ ▀▄█
	// ▀   ▀
	return "█▄  █\n█ ▀▄█\n▀   ▀"
}

// LetterV renders the letter V in a stylized way.
func LetterV(_ bool) string {
	// Here's what we're making:
	//
	// █   █
	// ▀▄ ▄▀
	//   ▀
	return "█   █\n▀▄ ▄▀\n  ▀  "
}
