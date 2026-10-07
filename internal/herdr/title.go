package herdr

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// CleanTitle makes a session title safe to show in a terminal or Herdr:
// escape sequences removed, other control characters turned into spaces
// and whitespace collapsed.
func CleanTitle(s string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
	return strings.Join(strings.Fields(cleaned), " ")
}
