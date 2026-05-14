// Package logo renders the Anvil wordmark in a stylized way.
package logo

import (
	"fmt"
	"image/color"
	"math/rand/v2"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

// letterform represents a letterform. It can be stretched horizontally by
// a given amount via the boolean argument.
type letterform func(bool) string

// sparkGlyphs is the weighted pool of runes used to build spark fields.
// Heavy space weighting keeps the field sparse, like a star field.
var sparkGlyphs = []rune{
	' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ',
	' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ',
	' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ',
	' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ',
	' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ',
	' ', ' ', ' ', ' ', ' ', ' ', ' ', '·', '✧', '✦',
}

// sparkSeed is a per-session random value mixed into all position hashes so
// the field looks different on every launch.
var sparkSeed = sync.OnceValue(func() uint32 {
	return rand.Uint32()
})

// sparkGlyph maps an absolute cell position to a glyph by hashing the
// position with the session seed. Each cell is independent — no tiling.
func sparkGlyph(pos int) rune {
	h := (sparkSeed() ^ uint32(pos)) * 2654435761
	h ^= h >> 16
	h += h << 3
	h ^= h >> 4
	return sparkGlyphs[h%uint32(len(sparkGlyphs))]
}

// SparkField builds a decorative field string of exactly n cells. offset is
// the absolute start position for this row, so every row draws from a
// completely independent region — no tiling, no shifted duplicates.
// ramp is a pre-computed gradient; each non-space character is assigned a
// random stop from it via a secondary hash of the same position.
func SparkField(n, offset int, ramp []color.Color) string {
	if n <= 0 {
		return ""
	}

	// Pre-build one style per ramp stop to avoid allocating a new
	// lipgloss.Style for every non-space cell on each render frame.
	rampStyles := make([]lipgloss.Style, len(ramp))
	for i, c := range ramp {
		rampStyles[i] = lipgloss.NewStyle().Foreground(c)
	}

	var sb strings.Builder
	for i := range n {
		pos := offset + i
		ch := sparkGlyph(pos)
		if ch == ' ' {
			sb.WriteByte(' ')
			continue
		}
		// Secondary hash for color — different multiplier avoids correlation
		// with the glyph selection hash.
		h := (sparkSeed() ^ uint32(pos)) * 2246822519
		h ^= h >> 16
		colorIdx := h % uint32(len(ramp))
		sb.WriteString(rampStyles[colorIdx].Render(string(ch)))
	}
	return sb.String()
}

// RandomPalette returns the stable-random gradient pair for this session,
// drawn from the built-in ANVIL palette pool.
func RandomPalette() (a, b color.Color) {
	p := titlePalettes[cachedRandN(len(titlePalettes))]
	return p.a, p.b
}

// Opts are the options for rendering the Anvil title art.
type Opts struct {
	TitleColorA  color.Color // left gradient ramp point (ignored when RandomColor is set)
	TitleColorB  color.Color // right gradient ramp point (ignored when RandomColor is set)
	VersionColor color.Color // version text color
	Width        int         // width of the rendered logo, used for truncation

	// RandomColor picks a gradient from the built-in palette pool at random.
	// The choice is stable across re-renders unless Unstable is also set.
	RandomColor bool

	// Unstable re-randomises the color palette on every render. Mainly for
	// testing/preview; use RandomColor alone in production to avoid jitter on
	// resize.
	Unstable bool
}

// resolvePalette returns the gradient endpoints to use, respecting the
// RandomColor and Unstable options. fallbackA/fallbackB are used when
// RandomColor is not set.
func resolvePalette(o Opts, fallbackA, fallbackB color.Color) (color.Color, color.Color) {
	if !o.RandomColor {
		return fallbackA, fallbackB
	}
	var idx int
	if o.Unstable {
		idx = rand.IntN(len(titlePalettes))
	} else {
		idx = cachedRandN(len(titlePalettes))
	}
	p := titlePalettes[idx]
	return p.a, p.b
}

// Render renders the Anvil logo.
//
// The compact argument determines whether it renders compact for the sidebar
// or wider for the main pane.
func Render(base lipgloss.Style, version string, compact bool, o Opts) string {
	fg := func(c color.Color, s string) string {
		return lipgloss.NewStyle().Foreground(c).Render(s)
	}

	// Title.
	const spacing = 1
	anvilLetterforms := []letterform{
		LetterA,
		LetterN,
		LetterV,
		LetterI,
		LetterL,
	}

	anvil := renderWord(spacing, -1, anvilLetterforms...)

	// Resolve the gradient colors, using a random palette if requested.
	colorA, colorB := resolvePalette(o, o.TitleColorA, o.TitleColorB)

	// Pre-compute a gradient ramp shared by all field rows.
	const gradSteps = 64
	fieldRamp := lipgloss.Blend1D(gradSteps, colorA, colorB)

	anvilWidth := lipgloss.Width(anvil)
	b := new(strings.Builder)
	for r := range strings.SplitSeq(anvil, "\n") {
		fmt.Fprintln(b, styles.ApplyForegroundGrad(base, r, colorA, colorB))
	}
	anvil = b.String()

	// Version row, right-aligned above the wordmark in the right-end gradient color.
	version = ansi.Truncate(version, anvilWidth, "…")
	gap := max(0, anvilWidth-lipgloss.Width(version))
	metaRow := strings.Repeat(" ", gap) + fg(colorB, version)
	anvil = strings.TrimRight(anvil, "\n")
	anvil = metaRow + "\n" + anvil

	// Narrow / sidebar version.
	if compact {
		// Use a different row offset for each field line so they don't repeat.
		const rowStride = 1024
		mkField := func(row int) string {
			return SparkField(anvilWidth, row*rowStride, fieldRamp)
		}
		return strings.Join([]string{mkField(0), mkField(1), anvil, mkField(2), ""}, "\n")
	}

	fieldHeight := lipgloss.Height(anvil)

	// Left field — each row draws from a non-overlapping position range.
	const leftWidth = 6
	const rowStride = 1024
	leftField := new(strings.Builder)
	for i := range fieldHeight {
		fmt.Fprintln(leftField, SparkField(leftWidth, i*rowStride, fieldRamp))
	}

	// Right field — steps down one cell per row, different offset per row.
	rightWidth := max(15, o.Width-anvilWidth-leftWidth-2) // 2 for the gap.
	rightField := new(strings.Builder)
	for i := range fieldHeight {
		width := max(0, rightWidth-i)
		fmt.Fprint(rightField, SparkField(width, i*rowStride+7, fieldRamp), "\n")
	}

	// Return the wide version.
	const hGap = " "
	logo := lipgloss.JoinHorizontal(lipgloss.Top, leftField.String(), hGap, anvil, hGap, rightField.String())
	if o.Width > 0 {
		lines := strings.Split(logo, "\n")
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, o.Width, "")
		}
		logo = strings.Join(lines, "\n")
	}
	return logo
}

// SmallRender renders a smaller version of the Anvil logo, suitable for
// smaller windows or sidebar usage.
func SmallRender(t *styles.Styles, width int, o Opts) string {
	gradA, gradB := resolvePalette(o, t.Logo.SmallGradFromColor, t.Logo.SmallGradToColor)

	fieldRamp := lipgloss.Blend1D(64, gradA, gradB)

	title := styles.ApplyBoldForegroundGrad(t.Logo.GradCanvas, "Anvil", gradA, gradB)
	remainingWidth := width - lipgloss.Width(title) - 1 // 1 for the space
	if remainingWidth > 0 {
		title = fmt.Sprintf("%s %s", title, SparkField(remainingWidth, 0, fieldRamp))
	}
	return title
}
