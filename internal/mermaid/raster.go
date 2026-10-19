package mermaid

import (
	"fmt"
	"image"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	resvg "github.com/boolean-maybe/go-resvg"
)

var (
	rasterizer     *resvg.Renderer
	rasterizerErr  error
	rasterizerOnce sync.Once
)

func sharedRasterizer() (*resvg.Renderer, error) {
	rasterizerOnce.Do(func() {
		rasterizer, rasterizerErr = resvg.New()
	})
	return rasterizer, rasterizerErr
}

var rootFontShorthand = regexp.MustCompile(`font:(\d+(?:\.\d+)?)px`)

// normalizeRootFont copies the root `font:` shorthand's size into a
// font-size attribute, because resvg ignores the shorthand and would fall
// back to 12px for every label that inherits it. It also paints bg behind
// the diagram so translucent fills look the same on any terminal.
func normalizeRootFont(svg, bg string) string {
	if !strings.HasPrefix(svg, "<svg ") {
		return svg
	}
	size := "16"
	if m := rootFontShorthand.FindStringSubmatch(svg); m != nil {
		size = m[1]
	}
	svg = `<svg font-size="` + size + `" ` + strings.TrimPrefix(svg, "<svg ")
	if bg == "" {
		return svg
	}
	end := strings.Index(svg, ">")
	return svg[:end+1] + `<rect width="100%" height="100%" fill="` + bg + `"/>` + svg[end+1:]
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

// Rasterize renders a Pele result to pixels at scale times its natural size,
// after resolving its CSS variables against theme.
func Rasterize(res Result, theme Theme, scale float64) (image.Image, error) {
	r, err := sharedRasterizer()
	if err != nil {
		return nil, fmt.Errorf("init resvg: %w", err)
	}
	svg := normalizeRootFont(resolveColorMix(ResolveVars(res.SVG, theme)), theme["pele-bg"])
	img, err := r.Render([]byte(svg), resvg.RenderOptions{
		Width:  int(math.Ceil(res.Width * scale)),
		Height: int(math.Ceil(res.Height * scale)),
	})
	if err != nil {
		return nil, fmt.Errorf("rasterize: %w", err)
	}
	return img, nil
}
