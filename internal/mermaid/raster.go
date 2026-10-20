package mermaid

import (
	"context"
	"fmt"
	"image"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/Broderick-Westrope/anvil/internal/mermaid/resvg"
)

// MaxPixels is the default cap on a rasterized diagram's area (32 MB of
// RGBA), so one diagram can't grow resvg's memory without bound. The chat
// scales diagrams to its width and 40 rows, well inside this.
const MaxPixels = 4096 * 2048

// RasterOptions controls how a diagram is rasterized.
type RasterOptions struct {
	// Scale multiplies the diagram's natural size.
	Scale float64
	// MaxPixels caps width x height; zero uses [MaxPixels].
	MaxPixels int
}

// Rasterizer turns Pele SVG into pixels. It is not safe for concurrent use;
// Close frees everything it holds.
type Rasterizer struct {
	engine *resvg.Engine
}

// NewRasterizer compiles resvg, which takes a few hundred milliseconds.
func NewRasterizer(ctx context.Context) (*Rasterizer, error) {
	e, err := resvg.New(ctx)
	if err != nil {
		return nil, err
	}
	return &Rasterizer{engine: e}, nil
}

// Close frees resvg's compiled code and memory.
func (r *Rasterizer) Close() error { return r.engine.Close() }

var rootFontShorthand = regexp.MustCompile(`font:(\d+(?:\.\d+)?)px`)

// normalizeRootFont copies the root `font:` shorthand's size into a
// font-size attribute, because resvg ignores the shorthand and would fall
// back to 12px for every label that inherits it. A non-empty bg is painted
// behind the diagram.
func normalizeRootFont(svg string) string {
	if !strings.HasPrefix(svg, "<svg ") {
		return svg
	}
	size := "16"
	if m := rootFontShorthand.FindStringSubmatch(svg); m != nil {
		size = m[1]
	}
	return `<svg font-size="` + size + `" ` + strings.TrimPrefix(svg, "<svg ")
}

var colorMix = regexp.MustCompile(`color-mix\(in srgb,\s*(#[0-9a-fA-F]{3,6})\s+([\d.]+)%\s*,\s*(#[0-9a-fA-F]{3,6})\s*\)`)

// resolveColorMix evaluates the srgb color-mix() Pele uses for nested
// cluster titles, which resvg cannot parse.
func resolveColorMix(svg string) string {
	return colorMix.ReplaceAllStringFunc(svg, func(expr string) string {
		m := colorMix.FindStringSubmatch(expr)
		a, okA := parseHex(m[1])
		b, okB := parseHex(m[3])
		pct, err := strconv.ParseFloat(m[2], 64)
		if !okA || !okB || err != nil {
			return expr
		}
		w := pct / 100
		var out [3]uint8
		for i := range out {
			out[i] = uint8(math.Round(float64(a[i])*w + float64(b[i])*(1-w)))
		}
		return fmt.Sprintf("#%02x%02x%02x", out[0], out[1], out[2])
	})
}

func parseHex(s string) ([3]uint8, bool) {
	s = strings.TrimPrefix(s, "#")
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return [3]uint8{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return [3]uint8{}, false
	}
	return [3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}, true
}

// Rasterize renders a Pele result to pixels after resolving its CSS
// variables against theme.
func (r *Rasterizer) Rasterize(res Result, theme Theme, opts RasterOptions) (*image.RGBA, error) {
	w := int(math.Ceil(res.Width * opts.Scale))
	h := int(math.Ceil(res.Height * opts.Scale))
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("rasterize: empty %dx%d diagram", w, h)
	}
	limit := opts.MaxPixels
	if limit <= 0 {
		limit = MaxPixels
	}
	if w*h > limit {
		return nil, fmt.Errorf("rasterize: %dx%d exceeds %d pixels", w, h, limit)
	}
	svg := normalizeRootFont(resolveColorMix(ResolveVars(res.SVG, theme)))
	img, err := r.engine.Render([]byte(svg), w, h)
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}
