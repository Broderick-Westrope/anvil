// Package mermaid renders Mermaid diagram source to SVG with Pele running in
// goja, and rasterizes that SVG for terminal display.
package mermaid

import (
	_ "embed"
	"fmt"
	"sync"

	"github.com/dop251/goja"
)

//go:embed pele.global.js
var peleSource string

var (
	peleProgram     *goja.Program
	peleProgramErr  error
	peleProgramOnce sync.Once
)

func compiledPele() (*goja.Program, error) {
	peleProgramOnce.Do(func() {
		peleProgram, peleProgramErr = goja.Compile("pele.global.js", peleSource, true)
	})
	return peleProgram, peleProgramErr
}

// Options controls Pele's layout.
type Options struct {
	// MaxWidth is the available width in pixels; zero leaves it unset.
	MaxWidth int
	// FontFamily is the label font Pele measures against.
	FontFamily string
	// FontSize is the base font size in pixels; zero uses Pele's default.
	FontSize int
}

// Result is a rendered diagram.
type Result struct {
	SVG    string
	Width  float64
	Height float64
	Type   string
}

// Renderer owns one goja runtime with Pele loaded. It is not safe for
// concurrent use.
type Renderer struct {
	vm       *goja.Runtime
	render   goja.Callable
	measurer goja.Value
}

// NewRenderer loads Pele into a fresh goja runtime.
func NewRenderer() (*Renderer, error) {
	prog, err := compiledPele()
	if err != nil {
		return nil, fmt.Errorf("compile pele: %w", err)
	}
	vm := goja.New()
	if _, err := vm.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("load pele: %w", err)
	}
	pele := vm.Get("pele")
	if pele == nil || goja.IsUndefined(pele) {
		return nil, fmt.Errorf("pele global not defined")
	}
	render, ok := goja.AssertFunction(pele.ToObject(vm).Get("render"))
	if !ok {
		return nil, fmt.Errorf("pele.render is not a function")
	}
	m, err := newMeasurer()
	if err != nil {
		return nil, fmt.Errorf("load fonts: %w", err)
	}
	jsMeasurer := vm.NewObject()
	if err := jsMeasurer.Set("width", m.width); err != nil {
		return nil, err
	}
	return &Renderer{vm: vm, render: render, measurer: jsMeasurer}, nil
}

// Render converts Mermaid source into SVG.
func (r *Renderer) Render(source string, opts Options) (Result, error) {
	jsOpts := map[string]any{"responsive": false, "measurer": r.measurer}
	if opts.MaxWidth > 0 {
		jsOpts["maxWidth"] = opts.MaxWidth
	}
	if opts.FontFamily != "" {
		jsOpts["fontFamily"] = opts.FontFamily
	}
	if opts.FontSize > 0 {
		jsOpts["fontSize"] = opts.FontSize
	}
	val, err := r.render(goja.Undefined(), r.vm.ToValue(source), r.vm.ToValue(jsOpts))
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
