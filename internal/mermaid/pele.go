// Package mermaid renders Mermaid diagram source to SVG with pele-go, a Go
// port of Pele, and rasterizes that SVG for terminal display.
package mermaid

import (
	"context"
	"errors"
	"fmt"
	"time"

	pele "github.com/Broderick-Westrope/pele-go"
	_ "github.com/Broderick-Westrope/pele-go/all"
	"github.com/Broderick-Westrope/pele-go/fontmeasure"
)

// MaxSourceBytes is the largest diagram source Render accepts.
const MaxSourceBytes = 64 << 10

// renderTimeout bounds one layout.
const renderTimeout = 5 * time.Second

var (
	// ErrSourceTooLarge is returned for sources over MaxSourceBytes.
	ErrSourceTooLarge = errors.New("diagram source too large")
	// ErrRenderTimeout is returned when layout runs past its deadline.
	ErrRenderTimeout = errors.New("diagram took too long to lay out")
)

// Options controls Pele's layout.
type Options struct {
	// MaxWidth is the available width in pixels.
	MaxWidth int
}

// Result is a rendered diagram.
type Result struct {
	SVG    string
	Width  float64
	Height float64
}

// Renderer lays diagrams out with labels measured in the embedded DejaVu
// Sans. It is safe for concurrent use.
type Renderer struct {
	measurer pele.TextMeasurer
}

// NewRenderer parses the embedded fonts.
func NewRenderer() (*Renderer, error) {
	m, err := fontmeasure.New(fontRegular, fontBold, fontItalic, fontBoldItalic)
	if err != nil {
		return nil, fmt.Errorf("load fonts: %w", err)
	}
	return &Renderer{measurer: m}, nil
}

// Render converts Mermaid source into SVG. It returns ErrRenderTimeout when
// layout runs past its own deadline or ctx's.
func (r *Renderer) Render(ctx context.Context, source string, opts Options) (Result, error) {
	if len(source) > MaxSourceBytes {
		return Result{}, ErrSourceTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	res, err := pele.Render(ctx, source, pele.Options{
		Measurer:   r.measurer,
		Responsive: pele.Ptr(false),
		// Images are off because the source is model-written and the
		// exported page is opened in a browser, which would fetch every
		// image href.
		Images: pele.Ptr(false),
		// Set even when 0, as it was for Pele in JavaScript, so diagrams
		// lay out as they did there.
		MaxWidth: pele.Ptr(float64(opts.MaxWidth)),
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return Result{}, ErrRenderTimeout
	}
	if err != nil {
		return Result{}, fmt.Errorf("pele render: %w", err)
	}
	return Result{SVG: res.SVG, Width: res.Width, Height: res.Height}, nil
}
