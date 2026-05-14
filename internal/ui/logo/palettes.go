package logo

import "image/color"

// titlePalette is a gradient pair applied to the ANVIL letterforms.
type titlePalette struct {
	a, b color.Color
}

// c builds an opaque color from RGB components.
func c(r, g, b uint8) color.Color { return color.RGBA{R: r, G: g, B: b, A: 0xFF} }

// titlePalettes is the pool of gradient options cycled through at random.
// Colors are sourced from the CharmTone palette.
var titlePalettes = []titlePalette{
	{c(0x0A, 0xDC, 0xD9), c(0x99, 0x53, 0xFF)}, // teal   → purple  (original)
	{c(0xFF, 0x98, 0x5A), c(0xF5, 0xEF, 0x34)}, // orange → yellow  (forge fire)
	{c(0xFF, 0x6E, 0x63), c(0xFF, 0xB5, 0x87)}, // red    → peach   (hot ember)
	{c(0x49, 0x49, 0xFF), c(0x5C, 0xDF, 0xEA)}, // blue   → cyan    (cold steel)
	{c(0x47, 0x76, 0xFF), c(0x00, 0xFF, 0xB2)}, // blue   → mint    (electric)
	{c(0x4D, 0x4C, 0x57), c(0xBF, 0xBC, 0xC8)}, // iron   → smoke   (iron)
	{c(0xC2, 0x59, 0xFF), c(0xE8, 0xFF, 0x27)}, // purple → yellow  (spark)
	{c(0x71, 0x34, 0xDD), c(0xFF, 0x98, 0x5A)}, // grape  → orange  (lava)
}
