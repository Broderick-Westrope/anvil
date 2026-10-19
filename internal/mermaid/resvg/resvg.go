// Package resvg rasterizes SVG with resvg compiled to WebAssembly and run by
// wazero, so it needs no CGO.
//
// resvg.wasm and the ABI below come from github.com/boolean-maybe/go-resvg
// v0.0.2 (Apache-2.0, see LICENSE): resvg 0.44 with DejaVu Sans built in.
// The Go side is rewritten so one Engine owns its runtime and its only
// instance, and Close frees all of it. Upstream keeps a package-level
// runtime for the life of the process and pools instances in a sync.Pool
// while also tracking them for Close, so every GC leaks an instance.
package resvg

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed resvg.wasm
var wasmBytes []byte

// The render export returns a pointer to a 20-byte little-endian header:
// status i32, width u32, height u32, pixel pointer u32, pixel length u32.
const headerLen = 20

var (
	// ErrInvalidSVG is returned when the input cannot be parsed as SVG.
	ErrInvalidSVG = errors.New("resvg: invalid svg")
	// ErrRenderFailed is returned when rasterization fails, e.g. for a zero
	// or oversized canvas.
	ErrRenderFailed = errors.New("resvg: render failed")
)

// DefaultMaxMemory is the WebAssembly memory size above which an Engine
// replaces its instance after a render. Linear memory never shrinks, so
// one huge diagram would otherwise pin its peak for good. Typical diagrams
// settle at about 10 MB.
const DefaultMaxMemory = 32 << 20

// Engine rasterizes SVG. It is not safe for concurrent use.
type Engine struct {
	ctx       context.Context
	rt        wazero.Runtime
	compiled  wazero.CompiledModule
	inst      *instance
	maxMemory uint32
}

type instance struct {
	mod    api.Module
	alloc  api.Function
	free   api.Function
	render api.Function
}

// New compiles the module. Compiling takes a few hundred milliseconds and
// the compiled code is most of the Engine's memory.
func New(ctx context.Context) (*Engine, error) {
	rt := wazero.NewRuntime(ctx)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("resvg: wasi: %w", err)
	}
	compiled, err := rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("resvg: compile: %w", err)
	}
	return &Engine{ctx: ctx, rt: rt, compiled: compiled, maxMemory: DefaultMaxMemory}, nil
}

// Close frees the instance, the compiled code and the runtime.
func (e *Engine) Close() error {
	e.inst = nil
	return e.rt.Close(e.ctx)
}

// MemorySize is the current instance's linear memory in bytes, or 0 when
// there is no instance.
func (e *Engine) MemorySize() uint32 {
	if e.inst == nil {
		return 0
	}
	return e.inst.mod.Memory().Size()
}

// Render rasterizes svg at width x height pixels. A zero dimension is
// derived from the other and the SVG's aspect ratio; both zero uses the
// intrinsic size.
func (e *Engine) Render(svg []byte, width, height int) (*image.RGBA, error) {
	if e.inst == nil {
		inst, err := e.instantiate()
		if err != nil {
			return nil, err
		}
		e.inst = inst
	}
	img, err := e.inst.renderRaw(e.ctx, svg, width, height)
	if err != nil || e.inst.mod.Memory().Size() > e.maxMemory {
		// A trap can leave the instance unusable, and a grown memory
		// never shrinks; start the next render from a fresh one.
		_ = e.inst.mod.Close(e.ctx)
		e.inst = nil
	}
	return img, err
}

func (e *Engine) instantiate() (*instance, error) {
	// An empty name lets the runtime hold more than one instance over its
	// life without a name clash.
	mod, err := e.rt.InstantiateModule(e.ctx, e.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("resvg: instantiate: %w", err)
	}
	return &instance{
		mod:    mod,
		alloc:  mod.ExportedFunction("wasm_alloc"),
		free:   mod.ExportedFunction("wasm_free"),
		render: mod.ExportedFunction("render"),
	}, nil
}

func (in *instance) renderRaw(ctx context.Context, svg []byte, width, height int) (*image.RGBA, error) {
	svgLen := uint64(len(svg))
	res, err := in.alloc.Call(ctx, svgLen)
	if err != nil {
		return nil, fmt.Errorf("resvg: alloc: %w", err)
	}
	svgPtr := uint32(res[0])
	if svgPtr == 0 {
		return nil, errors.New("resvg: alloc returned null")
	}
	if !in.mod.Memory().Write(svgPtr, svg) {
		_, _ = in.free.Call(ctx, uint64(svgPtr), svgLen)
		return nil, errors.New("resvg: write svg out of range")
	}
	res, err = in.render.Call(ctx, uint64(svgPtr), svgLen, uint64(width), uint64(height))
	_, _ = in.free.Call(ctx, uint64(svgPtr), svgLen)
	if err != nil {
		return nil, fmt.Errorf("resvg: render: %w", err)
	}
	return in.readResult(ctx, uint32(res[0]))
}

func (in *instance) readResult(ctx context.Context, headerPtr uint32) (*image.RGBA, error) {
	header, ok := in.mod.Memory().Read(headerPtr, headerLen)
	if !ok {
		return nil, errors.New("resvg: read header out of range")
	}
	defer func() { _, _ = in.free.Call(ctx, uint64(headerPtr), headerLen) }()
	status := int32(binary.LittleEndian.Uint32(header[0:4]))
	w := int(binary.LittleEndian.Uint32(header[4:8]))
	h := int(binary.LittleEndian.Uint32(header[8:12]))
	dataPtr := binary.LittleEndian.Uint32(header[12:16])
	dataLen := binary.LittleEndian.Uint32(header[16:20])
	switch status {
	case 0:
	case 1:
		return nil, ErrInvalidSVG
	case 2:
		return nil, ErrRenderFailed
	default:
		return nil, fmt.Errorf("resvg: unknown status %d", status)
	}
	raw, ok := in.mod.Memory().Read(dataPtr, dataLen)
	if !ok {
		return nil, errors.New("resvg: read pixels out of range")
	}
	defer func() { _, _ = in.free.Call(ctx, uint64(dataPtr), uint64(dataLen)) }()
	// Copy out: the view aliases wasm memory, which is freed above.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(img.Pix, raw)
	return img, nil
}
