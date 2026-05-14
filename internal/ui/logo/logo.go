// Package logo renders a Crush wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

const diag = `╱`

// Opts are the options for rendering the Crush title art.
type Opts struct {
	FieldColor   color.Color // diagonal lines
	TitleColorA  color.Color // left gradient ramp point (ignored when RandomColor is set)
	TitleColorB  color.Color // right gradient ramp point (ignored when RandomColor is set)
	CharmColor   color.Color // Charm™ text color
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation
	Hyper        bool        // whether it is Crush or Hypercrush

	// RandomColor picks a gradient from the built-in palette pool at random.
	// The choice is stable across re-renders unless Unstable is also set.
	RandomColor bool

	// Unstable re-randomises both the stretched letterform and the color palette
	// on every render. Mainly for testing/preview; use RandomColor alone in
	// production to avoid jitter on resize.
	Unstable bool
}

// Render renders the Crush logo. Set the argument to true to render the narrow
// version, intended for use in a sidebar.
//
// The compact argument determines whether it renders compact for the sidebar
// or wider for the main pane.
func Render(base lipgloss.Style, version string, compact bool, o Opts) string {
	charm := "Charm™"
	if !o.Hyper {
		charm = " " + charm
	}

	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// Title.
	const spacing = 1
	var hyperLetterforms []letterform
	if o.Hyper {
		hyperLetterforms = []letterform{
			LetterH,
			LetterYAlt,
			LetterP,
			LetterE,
			LetterR,
		}
	}
	crushLetterforms := []letterform{
		LetterA,
		LetterN,
		LetterV,
		LetterI,
		LetterL,
	}
	if o.Hyper && !compact {
		crushLetterforms = append(hyperLetterforms, crushLetterforms...)
	}

	stretchIndex := -1 // -1 means no stretching.
	if !compact && !o.Unstable {
		// Always stretch the same letterform, which is picked once at random.
		stretchIndex = cachedRandN(len(crushLetterforms))
	} else if !compact && o.Unstable {
		// Stretch a random letterform on every render.
		stretchIndex = rand.IntN(len(crushLetterforms))
	}
	crush := renderWord(spacing, stretchIndex, crushLetterforms...)
	if o.Hyper && compact {
		crush = renderWord(spacing, stretchIndex, hyperLetterforms...) + "\n" + crush
	}
	// Resolve the gradient colors, using a random palette if requested.
	colorA, colorB := o.TitleColorA, o.TitleColorB
	if o.RandomColor {
		var idx int
		if o.Unstable {
			idx = rand.IntN(len(titlePalettes))
		} else {
			idx = cachedRandN(len(titlePalettes))
		}
		p := titlePalettes[idx]
		colorA, colorB = p.a, p.b
	}

	crushWidth := lipgloss.Width(crush)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(crush, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(base, r, colorA, colorB))
	}
	crush = b.String()

	// Charm and version.
	metaRowGap := 1
	maxVersionWidth := crushWidth - lipgloss.Width(charm) - metaRowGap
	version = ansi.Truncate(version, maxVersionWidth, "…") // truncate version if too long.
	if o.Hyper && compact {
		version += " "
	}
	gap := max(0, crushWidth-lipgloss.Width(charm)-lipgloss.Width(version))
	metaRow := fg(o.CharmColor, charm) + strings.Repeat(" ", gap) + fg(o.VersionColor, version)

	// Join the meta row and big Crush title.
	crush = strings.TrimSpace(metaRow + "\n" + crush)

	// Narrow version. If this is Hypercrush, this is also a stacked version.
	if compact {
		field := fg(o.FieldColor, strings.Repeat(diag, crushWidth))
		return strings.Join([]string{field, field, crush, field, ""}, "\n")
	}

	fieldHeight := lipgloss.Height(crush)

	// Left field.
	const leftWidth = 6
	leftFieldRow := fg(o.FieldColor, strings.Repeat(diag, leftWidth))
	leftField := new(strings.Builder)
	for range fieldHeight {
		fmt.Fprintln(leftField, leftFieldRow)
	}

	// Right field.
	rightWidth := max(15, o.Width-crushWidth-leftWidth-2) // 2 for the gap.
	const stepDownAt = 0
	rightField := new(strings.Builder)
	for i := range fieldHeight {
		width := rightWidth
		if i >= stepDownAt {
			width = rightWidth - (i - stepDownAt)
		}
		fmt.Fprint(rightField, fg(o.FieldColor, strings.Repeat(diag, width)), "\n")
	}

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftField.String(), hGap, crush, hGap, rightField.String())
	if o.Width > 0 {
		// Truncate the logo to the specified width.
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// SmallRender renders a smaller version of the Crush logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	name := "Anvil"
	if o.Hyper {
		name = "HYPERANVIL"
	}
	charm := "Charm™"
	if !o.Hyper {
		charm = " " + charm
	}

	gradA, gradB := t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor
	if o.RandomColor {
		var idx int
		if o.Unstable {
			idx = rand.IntN(len(titlePalettes))
		} else {
			idx = cachedRandN(len(titlePalettes))
		}
		p := titlePalettes[idx]
		gradA, gradB = p.a, p.b
	}

	title := t.Logo.SmallCharm.Render(charm)
	title = fmt.Sprintf("%s %s", title, styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, name, gradA, gradB))
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space after the name
	if remainingWidth > 0 {
		lines := strings.Repeat("╱", remainingWidth)
		title = fmt.Sprintf("%s %s", title, t.Logo.SmallDiagonals.Render(lines))
	}
	return title
}
