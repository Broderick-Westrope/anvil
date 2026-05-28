package styles

import "image/color"

// tn constructs a TokyoNight palette color from RGB components.
func tn(r, g, b uint8) color.Color {
	return color.RGBA{R: r, G: g, B: b, A: 0xFF}
}

// TokyoNight returns the TokyoNight Night theme, tuned for a dark terminal
// with a background around #050014.
//
// Palette reference: https://github.com/folke/tokyonight.nvim
func TokyoNight() Styles {
	return quickStyle(quickStyleOpts{
		primary:   tn(0xbb, 0x9a, 0xf7), // magenta   #bb9af7
		secondary: tn(0x73, 0xda, 0xca), // green1    #73daca
		accent:    tn(0x7a, 0xa2, 0xf7), // blue      #7aa2f7
		keyword:   tn(0xbb, 0x9a, 0xf7), // magenta   #bb9af7

		fgBase:       tn(0xc0, 0xca, 0xf5), // fg        #c0caf5
		fgMoreSubtle: tn(0xa9, 0xb1, 0xd6), // fg_dark   #a9b1d6
		fgSubtle:     tn(0x56, 0x5f, 0x89), // comment   #565f89
		fgMostSubtle: tn(0x3b, 0x42, 0x61), // fg_gutter #3b4261

		onPrimary: tn(0x1a, 0x1b, 0x26), // bg        #1a1b26

		// Background scale stepped up from the user's #050014 terminal bg.
		bgBase:         tn(0x0c, 0x0e, 0x14), // bg_dark1     #0c0e14
		bgLeastVisible: tn(0x11, 0x12, 0x19), // midpoint     #111219
		bgLessVisible:  tn(0x1a, 0x1b, 0x26), // bg           #1a1b26
		bgMostVisible:  tn(0x29, 0x2e, 0x42), // bg_highlight #292e42

		separator: tn(0x3b, 0x42, 0x61), // fg_gutter #3b4261

		destructive:       tn(0xf7, 0x76, 0x8e), // red       #f7768e
		error:             tn(0xdb, 0x4b, 0x4b), // red1      #db4b4b
		warningSubtle:     tn(0xe0, 0xaf, 0x68), // yellow    #e0af68
		warning:           tn(0xff, 0x9e, 0x64), // orange    #ff9e64
		denied:            tn(0xff, 0x9e, 0x64), // orange    #ff9e64
		busy:              tn(0xe0, 0xaf, 0x68), // yellow    #e0af68
		info:              tn(0x7a, 0xa2, 0xf7), // blue      #7aa2f7
		infoMoreSubtle:    tn(0x2a, 0xc3, 0xde), // blue1     #2ac3de
		infoMostSubtle:    tn(0x3d, 0x59, 0xa1), // blue0     #3d59a1
		success:           tn(0x9e, 0xce, 0x6a), // green     #9ece6a
		successMoreSubtle: tn(0x73, 0xda, 0xca), // green1    #73daca
		successMostSubtle: tn(0x41, 0xa6, 0xb5), // green2    #41a6b5
	})
}
