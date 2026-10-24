package dialog

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestRenderContextGapsBetweenParts(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	for name, tc := range map[string]struct {
		gap  int
		want string
	}{
		// Without a gap every part keeps its line, empty ones included.
		"no gap": {0, "a\n\nb\nhelp"},
		// With a gap, empty parts are dropped but keep their separator,
		// and the last part is followed only by the gap before the help.
		"gap of one": {1, "a\n\n\nb\n\nhelp"},
		"gap of two": {2, "a\n\n\n\n\nb\n\n\nhelp"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rc := NewRenderContext(&sty, 40)
			rc.IsOnboarding = true
			rc.Gap = tc.gap
			rc.Parts = []string{"a", "", "b"}
			rc.Help = "help"
			require.Equal(t, tc.want, rc.Render())
		})
	}
}
