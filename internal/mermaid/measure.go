package mermaid

import (
	"embed"
	"fmt"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// These are the same DejaVu Sans subsets baked into resvg/resvg.wasm, so
// Pele lays labels out with the widths resvg will actually draw.
//
//go:embed fonts/*.ttf
var fontFiles embed.FS

// Pele's label style bits (text/label.d.ts).
const (
	styleBold   = 1
	styleItalic = 2
)

// measurePPEM measures at a large ppem so rounding to 26.6 fixed point is
// negligible, then scales linearly.
const measurePPEM = 1024

type measurer struct {
	faces [4]*sfnt.Font
	buf   sfnt.Buffer
	mu    sync.Mutex
}

var fontPaths = [4]string{
	"fonts/DejaVuSans.ttf",
	"fonts/DejaVuSans-Bold.ttf",
	"fonts/DejaVuSans-Oblique.ttf",
	"fonts/DejaVuSans-BoldOblique.ttf",
}

func newMeasurer() (*measurer, error) {
	m := &measurer{}
	for i, p := range fontPaths {
		data, err := fontFiles.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := sfnt.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		m.faces[i] = f
	}
	return m, nil
}

// width returns the advance width of text in pixels at size, matching
// Pele's TextMeasurer.width(text, size, style).
func (m *measurer) width(text string, size float64, style int) float64 {
	idx := 0
	if style&styleBold != 0 {
		idx |= 1
	}
	if style&styleItalic != 0 {
		idx |= 2
	}
	f := m.faces[idx]

	m.mu.Lock()
	defer m.mu.Unlock()

	ppem := fixed.I(measurePPEM)
	var total fixed.Int26_6
	prev := sfnt.GlyphIndex(0)
	for i, r := range text {
		g, err := f.GlyphIndex(&m.buf, r)
		if err != nil || g == 0 {
			// Missing glyphs fall back to an average advance.
			g, _ = f.GlyphIndex(&m.buf, 'n')
		}
		if i > 0 {
			if k, err := f.Kern(&m.buf, prev, g, ppem, font.HintingNone); err == nil {
				total += k
			}
		}
		adv, err := f.GlyphAdvance(&m.buf, g, ppem, font.HintingNone)
		if err == nil {
			total += adv
		}
		prev = g
	}
	return float64(total) / 64 / measurePPEM * size
}
