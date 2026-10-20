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

// fontFaces lists the embedded faces with their CSS weight and style,
// indexed by Pele's style bits.
var fontFaces = [4]struct {
	path, weight, style string
}{
	{"fonts/DejaVuSans.ttf", "normal", "normal"},
	{"fonts/DejaVuSans-Bold.ttf", "bold", "normal"},
	{"fonts/DejaVuSans-Oblique.ttf", "normal", "italic"},
	{"fonts/DejaVuSans-BoldOblique.ttf", "bold", "italic"},
}

func newMeasurer() (*measurer, error) {
	m := &measurer{}
	for i, face := range fontFaces {
		data, err := fontFiles.ReadFile(face.path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", face.path, err)
		}
		f, err := sfnt.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", face.path, err)
		}
		m.faces[i] = f
	}
	return m, nil
}

// width returns the advance width of text in pixels at size, matching
// Pele's TextMeasurer.width(text, size, style).
func (m *measurer) width(text string, size float64, style int) float64 {
	f := m.faces[style&(styleBold|styleItalic)]

	m.mu.Lock()
	defer m.mu.Unlock()

	ppem := fixed.I(measurePPEM)
	var total fixed.Int26_6
	prev := sfnt.GlyphIndex(0)
	for _, r := range text {
		g, err := f.GlyphIndex(&m.buf, r)
		if err != nil || g == 0 {
			// Missing glyphs fall back to an average advance.
			g, _ = f.GlyphIndex(&m.buf, 'n')
		}
		if prev != 0 {
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
