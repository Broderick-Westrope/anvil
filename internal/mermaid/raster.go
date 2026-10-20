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
	// MaxPixels caps width x height, shrinking Scale as far as it must to
	// fit; zero uses [MaxPixels].
	MaxPixels int
}

// Rasterizer turns Pele SVG into pixels. It is not safe for concurrent use;
// Close frees everything it holds.
type Rasterizer struct {
	engine *resvg.Engine
}

// NewRasterizer compiles resvg, which takes a few hundred milliseconds, or
// loads it from cacheDir when an earlier Rasterizer left it there. An empty
// cacheDir always compiles.
func NewRasterizer(ctx context.Context, cacheDir string) (*Rasterizer, error) {
	e, err := resvg.New(ctx, cacheDir)
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
// back to 12px for every label that inherits it.
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
// cluster titles, which resvg cannot parse, in one attribute value.
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

// fitPixels returns the whole-pixel size of a width x height diagram at
// scale, shrinking the scale when rounding each side up would take the area
// past limit.
func fitPixels(width, height, scale float64, limit int) (w, h int, err error) {
	usable := func(fw, fh float64) bool {
		return fw > 0 && fh > 0 && !math.IsInf(fw, 0) && !math.IsInf(fh, 0)
	}
	fw, fh := width*scale, height*scale
	unusable := fmt.Errorf("rasterize: unusable size %vx%v at scale %v", width, height, scale)
	if !usable(fw, fh) {
		return 0, 0, unusable
	}
	if math.Ceil(fw)*math.Ceil(fh) > float64(limit) {
		// Rounding adds less than a pixel to each side, so the largest s
		// with (width*s+1)*(height*s+1) <= limit always fits. This is the
		// positive root of that quadratic, in a form that keeps its
		// precision when the area is large.
		l := float64(limit) - 1
		s := 2 * l / (width + height + math.Sqrt((width+height)*(width+height)+4*width*height*l))
		fw, fh = width*s, height*s
	}
	w, h = int(math.Ceil(fw)), int(math.Ceil(fh))
	// Shrinking can take a thin side below the smallest float.
	if !usable(fw, fh) || w*h > limit {
		return 0, 0, unusable
	}
	return w, h, nil
}

// Rasterize renders a Pele result to pixels after resolving its CSS
// variables against theme.
func (r *Rasterizer) Rasterize(res Result, theme Theme, opts RasterOptions) (*image.RGBA, error) {
	limit := opts.MaxPixels
	if limit <= 0 {
		limit = MaxPixels
	}
	w, h, err := fitPixels(res.Width, res.Height, opts.Scale, limit)
	if err != nil {
		return nil, err
	}
	svg, err := resolveVars(res.SVG, theme, resolveColorMix)
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	img, err := r.engine.Render([]byte(normalizeRootFont(svg)), w, h)
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}
