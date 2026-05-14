package styles

import (
	"image/color"

	"github.com/charmbracelet/x/exp/charmtone"
)

// ThemeForProvider returns the Styles associated with the given provider
// ID. Unknown or empty provider IDs yield the default TokyoNight theme.
func ThemeForProvider(providerID string) Styles {
	switch providerID {
	case "hyper":
		return HypercrushObsidiana()
	default:
		return TokyoNight()
	}
}

// TokyoNight returns the TokyoNight Night theme, tuned for a dark terminal
// with a background around #050014.
//
// Palette reference: https://github.com/folke/tokyonight.nvim
func TokyoNight() Styles {
	return quickStyle(quickStyleOpts{
		// Blue is the dominant interactive/brand hue in TokyoNight.
		primary:   color.RGBA{R: 0xbb, G: 0x9a, B: 0xf7, A: 0xFF}, // magenta   #bb9af7
		secondary: color.RGBA{R: 0x73, G: 0xda, B: 0xca, A: 0xFF}, // green1    #73daca
		accent:    color.RGBA{R: 0x7a, G: 0xa2, B: 0xf7, A: 0xFF}, // blue      #7aa2f7
		keyword:   color.RGBA{R: 0xbb, G: 0x9a, B: 0xf7, A: 0xFF}, // magenta   #bb9af7

		fgBase:       color.RGBA{R: 0xc0, G: 0xca, B: 0xf5, A: 0xFF}, // fg        #c0caf5
		fgMoreSubtle: color.RGBA{R: 0xa9, G: 0xb1, B: 0xd6, A: 0xFF}, // fg_dark   #a9b1d6
		fgSubtle:     color.RGBA{R: 0x56, G: 0x5f, B: 0x89, A: 0xFF}, // comment   #565f89
		fgMostSubtle: color.RGBA{R: 0x3b, G: 0x42, B: 0x61, A: 0xFF}, // fg_gutter #3b4261

		onPrimary: color.RGBA{R: 0x1a, G: 0x1b, B: 0x26, A: 0xFF}, // bg        #1a1b26

		// Background scale stepped up from the user's #050014 terminal bg.
		bgBase:         color.RGBA{R: 0x0c, G: 0x0e, B: 0x14, A: 0xFF}, // bg_dark1  #0c0e14
		bgLeastVisible: color.RGBA{R: 0x16, G: 0x16, B: 0x1e, A: 0xFF}, // bg_dark   #16161e
		bgLessVisible:  color.RGBA{R: 0x1a, G: 0x1b, B: 0x26, A: 0xFF}, // bg        #1a1b26
		bgMostVisible:  color.RGBA{R: 0x29, G: 0x2e, B: 0x42, A: 0xFF}, // bg_highlight #292e42

		separator: color.RGBA{R: 0x3b, G: 0x42, B: 0x61, A: 0xFF}, // fg_gutter #3b4261

		destructive:       color.RGBA{R: 0xf7, G: 0x76, B: 0x8e, A: 0xFF}, // red       #f7768e
		error:             color.RGBA{R: 0xdb, G: 0x4b, B: 0x4b, A: 0xFF}, // red1      #db4b4b
		warningSubtle:     color.RGBA{R: 0xe0, G: 0xaf, B: 0x68, A: 0xFF}, // yellow    #e0af68
		warning:           color.RGBA{R: 0xff, G: 0x9e, B: 0x64, A: 0xFF}, // orange    #ff9e64
		busy:              color.RGBA{R: 0xe0, G: 0xaf, B: 0x68, A: 0xFF}, // yellow    #e0af68
		info:              color.RGBA{R: 0x7a, G: 0xa2, B: 0xf7, A: 0xFF}, // blue      #7aa2f7
		infoMoreSubtle:    color.RGBA{R: 0x2a, G: 0xc3, B: 0xde, A: 0xFF}, // blue1     #2ac3de
		infoMostSubtle:    color.RGBA{R: 0x3d, G: 0x59, B: 0xa1, A: 0xFF}, // blue0     #3d59a1
		success:           color.RGBA{R: 0x9e, G: 0xce, B: 0x6a, A: 0xFF}, // green     #9ece6a
		successMoreSubtle: color.RGBA{R: 0x73, G: 0xda, B: 0xca, A: 0xFF}, // green1    #73daca
		successMostSubtle: color.RGBA{R: 0x41, G: 0xa6, B: 0xb5, A: 0xFF}, // green2    #41a6b5
	})
}

// CharmtonePantera returns the Charmtone dark theme.
func CharmtonePantera() Styles {
	return quickStyle(quickStyleOpts{
		primary:   charmtone.Charple,
		secondary: charmtone.Dolly,
		accent:    charmtone.Bok,
		keyword:   charmtone.Blush,

		fgBase:       charmtone.Ash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: charmtone.Butter,

		bgBase:         color.RGBA{R: 0x16, G: 0x15, B: 0x1C, A: 0xFF},
		bgLeastVisible: charmtone.Pepper,
		bgLessVisible:  charmtone.BBQ,
		bgMostVisible:  charmtone.Charcoal,

		separator: charmtone.Charcoal,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,
	})
}

// HypercrushObsidiana returns the Hypercrush dark theme.
func HypercrushObsidiana() Styles {
	return quickStyle(quickStyleOpts{
		primary:   charmtone.Charple,
		secondary: charmtone.Dolly,
		accent:    charmtone.Bok,

		fgBase:       charmtone.Ash,
		fgMoreSubtle: charmtone.Squid,
		fgSubtle:     charmtone.Smoke,
		fgMostSubtle: charmtone.Oyster,

		onPrimary: charmtone.Butter,

		bgBase:         color.RGBA{R: 0x16, G: 0x15, B: 0x1C, A: 0xFF},
		bgLeastVisible: charmtone.Pepper,
		bgLessVisible:  charmtone.BBQ,
		bgMostVisible:  charmtone.Charcoal,

		separator: charmtone.Charcoal,

		destructive:       charmtone.Coral,
		error:             charmtone.Sriracha,
		warningSubtle:     charmtone.Zest,
		warning:           charmtone.Mustard,
		busy:              charmtone.Citron,
		info:              charmtone.Malibu,
		infoMoreSubtle:    charmtone.Sardine,
		infoMostSubtle:    charmtone.Damson,
		success:           charmtone.Julep,
		successMoreSubtle: charmtone.Bok,
		successMostSubtle: charmtone.Guac,
	})
}
