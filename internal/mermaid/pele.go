// Package mermaid renders Mermaid diagram source to SVG with Pele running in
// goja, and rasterizes that SVG for terminal display.
package mermaid

import (
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
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

//go:embed pele.global.js
var peleSource string

// Options controls Pele's layout.
type Options struct {
	// MaxWidth is the available width in pixels; zero leaves it unset.
	MaxWidth int
}

// Result is a rendered diagram.
type Result struct {
	SVG    string
	Width  float64
	Height float64
	Type   string
}

// Renderer owns one goja runtime with Pele loaded. It is not safe for
// concurrent use. Nothing is shared between Renderers, so dropping one
// frees all of it.
type Renderer struct {
	vm       *goja.Runtime
	render   goja.Callable
	measurer goja.Value
	// afterFunc schedules the layout deadline; tests replace it.
	afterFunc func(time.Duration, func()) *time.Timer
}

// NewRenderer loads Pele into a fresh goja runtime.
func NewRenderer() (*Renderer, error) {
	prog, err := goja.Compile("pele.global.js", peleSource, true)
	if err != nil {
		return nil, fmt.Errorf("compile pele: %w", err)
	}
	vm := goja.New()
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("load pele: %w", err)
	}
	pele := vm.Get("pele")
	if pele == nil || goja.IsUndefined(pele) {
		return nil, errors.New("pele global not defined")
	}
	render, ok := goja.AssertFunction(pele.ToObject(vm).Get("render"))
	if !ok {
		return nil, errors.New("pele.render is not a function")
	}
	m, err := newMeasurer()
	if err != nil {
		return nil, fmt.Errorf("load fonts: %w", err)
	}
	jsMeasurer := vm.NewObject()
	if err := jsMeasurer.Set("width", m.width); err != nil {
		return nil, fmt.Errorf("set measurer: %w", err)
	}
	return &Renderer{vm: vm, render: render, measurer: jsMeasurer, afterFunc: time.AfterFunc}, nil
}

// Render converts Mermaid source into SVG.
func (r *Renderer) Render(source string, opts Options) (Result, error) {
	if len(source) > MaxSourceBytes {
		return Result{}, ErrSourceTooLarge
	}
	jsOpts := map[string]any{"responsive": false, "measurer": r.measurer}
	if opts.MaxWidth > 0 {
		jsOpts["maxWidth"] = opts.MaxWidth
	}
	fired := make(chan struct{})
	timer := r.afterFunc(renderTimeout, func() {
		r.vm.Interrupt(ErrRenderTimeout)
		close(fired)
	})
	val, err := r.render(goja.Undefined(), r.vm.ToValue(source), r.vm.ToValue(jsOpts))
	if !timer.Stop() {
		// The deadline has fired or is firing: wait for its interrupt,
		// then clear it, or it would stop the next render.
		<-fired
	}
	r.vm.ClearInterrupt()
	if interrupted, ok := errors.AsType[*goja.InterruptedError](err); ok && interrupted.Value() == ErrRenderTimeout {
		return Result{}, ErrRenderTimeout
	}
	if err != nil {
		return Result{}, fmt.Errorf("pele render: %w", err)
	}
	obj := val.ToObject(r.vm)
	return Result{
		SVG:    obj.Get("svg").String(),
		Width:  obj.Get("width").ToFloat(),
		Height: obj.Get("height").ToFloat(),
		Type:   obj.Get("type").String(),
	}, nil
}
