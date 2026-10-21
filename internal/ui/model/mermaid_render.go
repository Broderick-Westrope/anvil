package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/mermaid/resvg"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// This file is the graphics feature's worker side: it owns the Mermaid
// render engines, turns requests into images or browser pages, and never
// touches the feature's cache, which graphics.go owns.

// diagramExportDir is where pages for the browser are written: the user's
// own cache directory, not a shared temp directory another user could
// plant files in.
func diagramExportDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("anvil-%d", os.Getuid()))
	}
	return filepath.Join(dir, "anvil", "diagrams")
}

// diagramCompileCacheDir is where resvg's compiled code is kept for every
// Anvil process to load, or empty to compile it each time. Unlike exports
// there's no temp directory fallback: the cache runs as native code, so it
// must not be somewhere another user could write.
func diagramCompileCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "anvil", "wasm")
}

// pruneExports deletes exports (diagram pages and tool images) in dir older
// than diagramExportMaxAge.
func pruneExports(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-diagramExportMaxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			slog.Warn("Failed to remove old export", "err", err)
		}
	}
}

// errEnginesUnavailable wraps a failure to load a render engine, which
// says nothing about the diagram.
var errEnginesUnavailable = errors.New("load diagram renderer")

// transientRenderError reports whether err may not happen again, so the
// diagram is worth one more try: it ran out of time, or an engine didn't
// load.
func transientRenderError(err error) bool {
	return errors.Is(err, mermaid.ErrRenderTimeout) || errors.Is(err, resvg.ErrRenderTimeout) || errors.Is(err, errEnginesUnavailable)
}

// diagramEngines is everything a render needs. The worker owns it alone,
// since the rasterizer isn't safe for concurrent use. The rasterizer loads on
// the first image, so exporting alone doesn't compile resvg.
type diagramEngines struct {
	pele   *mermaid.Renderer
	raster *mermaid.Rasterizer
	// cacheDir is where the rasterizer keeps its compiled code.
	cacheDir string
}

func (e *diagramEngines) rasterizer() (*mermaid.Rasterizer, error) {
	if e.raster == nil {
		r, err := mermaid.NewRasterizer(context.Background(), e.cacheDir)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errEnginesUnavailable, err)
		}
		e.raster = r
	}
	return e.raster, nil
}

func (e *diagramEngines) close() {
	if e.raster != nil {
		_ = e.raster.Close()
	}
}

// work renders diagrams one at a time. It loads the engines on the first
// request and drops them after idleTimeout without one, so a session that
// stops drawing diagrams gives the memory back.
func (f *graphicsFeature) work() {
	var engines *diagramEngines
	idle := time.NewTimer(f.idleTimeout)
	idle.Stop()
	for {
		select {
		case req := <-f.requests:
			var msg tea.Msg
			engines, msg = f.serve(engines, req)
			f.results <- msg
			idle.Reset(f.idleTimeout)
		case <-idle.C:
			if engines != nil {
				engines.close()
				engines = nil
				f.returnMemory()
			}
		}
	}
}

// serve answers req with engines, loading them first if they're nil, and
// returns the engines to keep for the next request.
func (f *graphicsFeature) serve(engines *diagramEngines, req diagramRequest) (*diagramEngines, tea.Msg) {
	if req.key.producer == producerBitmap {
		// Bitmaps need no engines.
		msg, _ := f.runRequest(nil, req)
		return engines, msg
	}
	if engines == nil {
		pele, err := f.newRenderer()
		if err != nil {
			_, fail := f.job(req)
			return nil, fail(fmt.Errorf("%w: %w", errEnginesUnavailable, err))
		}
		engines = &diagramEngines{pele: pele, cacheDir: f.compileCacheDir}
	}
	msg, ok := f.runRequest(engines, req)
	if !ok {
		// A panic can leave either engine mid-call; start the next request
		// from fresh ones.
		engines.close()
		return nil, msg
	}
	return engines, msg
}

// runRequest does req's job. Pele, resvg and the image decoders run on
// model- or tool-written input, so a panic in any is reported as the
// request failing, and ok is false, rather than taking the TUI down.
func (f *graphicsFeature) runRequest(e *diagramEngines, req diagramRequest) (msg tea.Msg, ok bool) {
	run, fail := f.job(req)
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Graphics request panicked", "engine", req.key.producer.engine(), "panic", r)
			msg, ok = fail(fmt.Errorf("%s crashed: %v", req.key.producer.engine(), r)), false
		}
	}()
	return run(e), true
}

// job is how the worker answers req, and what it answers when that fails.
func (f *graphicsFeature) job(req diagramRequest) (run func(*diagramEngines) tea.Msg, fail func(error) tea.Msg) {
	if req.key.producer == producerBitmap {
		return func(*diagramEngines) tea.Msg { return renderBitmap(req) },
			func(err error) tea.Msg { return diagramReadyMsg{key: req.key, err: err} }
	}
	switch req.job {
	case diagramJobExport:
		return func(e *diagramEngines) tea.Msg { return f.export(e, req) },
			func(err error) tea.Msg { return diagramExportedMsg{err: err} }
	case diagramJobCopy:
		return func(e *diagramEngines) tea.Msg { return f.copyDiagram(e, req) },
			func(err error) tea.Msg { return diagramCopiedMsg{err: err} }
	}
	return func(e *diagramEngines) tea.Msg { return renderDiagram(e, req) },
		func(err error) tea.Msg { return diagramReadyMsg{key: req.key, err: err} }
}

// copyDiagram draws the diagram at diagramCopyScale on the theme's
// background, which the chat leaves transparent: pasted somewhere else, a
// diagram drawn for a dark theme would be unreadable on a light page. It
// writes the clipboard here, on the worker, so copies land in the order
// they were asked for.
func (f *graphicsFeature) copyDiagram(e *diagramEngines, req diagramRequest) diagramCopiedMsg {
	fail := func(err error) diagramCopiedMsg { return diagramCopiedMsg{err: err} }
	raster, err := e.rasterizer()
	if err != nil {
		return fail(err)
	}
	res, err := e.pele.Render(context.Background(), req.source, mermaid.Options{})
	if err != nil {
		return fail(err)
	}
	img, err := raster.Rasterize(res, req.theme, mermaid.RasterOptions{Scale: diagramCopyScale})
	if err != nil {
		return fail(err)
	}
	fillBehind(img, color.RGBAModel.Convert(lipgloss.Color(req.theme["pele-bg"])).(color.RGBA))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fail(err)
	}
	if err := f.writeImage(buf.Bytes()); err != nil {
		return fail(err)
	}
	return diagramCopiedMsg{}
}

func (f *graphicsFeature) export(e *diagramEngines, req diagramRequest) tea.Msg {
	res, err := e.pele.Render(context.Background(), req.source, mermaid.Options{})
	if err != nil {
		return diagramExportedMsg{err: err}
	}
	doc, err := mermaid.HTMLDocument(res, req.theme, "Anvil diagram")
	if err != nil {
		return diagramExportedMsg{err: err}
	}
	if err := os.MkdirAll(f.exportDir, 0o700); err != nil {
		return diagramExportedMsg{err: err}
	}
	pruneExports(f.exportDir)
	path := filepath.Join(f.exportDir, fmt.Sprintf("%x.html", req.key.hash))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		return diagramExportedMsg{err: err}
	}
	return diagramExportedMsg{path: path}
}

func renderDiagram(e *diagramEngines, req diagramRequest) diagramReadyMsg {
	fail := func(err error) diagramReadyMsg { return diagramReadyMsg{key: req.key, err: err} }
	raster, err := e.rasterizer()
	if err != nil {
		return fail(err)
	}
	cw, ch := float64(req.cellW), float64(req.cellH)
	fit := newDiagramFit(req)
	res, err := e.pele.Render(context.Background(), req.source, mermaid.Options{MaxWidth: fit.maxWidth})
	if err != nil {
		return fail(err)
	}
	scale, squeezed, err := fit.scale(res.Width, res.Height)
	if err != nil {
		return fail(err)
	}

	img, err := raster.Rasterize(res, req.theme, mermaid.RasterOptions{Scale: scale, MaxPixels: fit.maxPixels})
	if err != nil {
		return fail(err)
	}
	b := img.Bounds()
	cols := int(math.Ceil(float64(b.Dx()) / cw))
	rows := int(math.Ceil(float64(b.Dy()) / ch))

	// Pad to whole cells so the image fills its placeholder grid exactly
	// and terminals that stretch to the grid (Herdr) keep the aspect. The
	// padding is transparent, like the diagram's background.
	padded := image.NewRGBA(image.Rect(0, 0, cols*req.cellW, rows*req.cellH))
	draw.Draw(padded, b, img, b.Min, draw.Src)
	return diagramReadyMsg{key: req.key, img: padded, cols: cols, rows: rows, squeezed: squeezed, cellW: req.cellW, cellH: req.cellH}
}

// diagramFit sizes one image: Pele lays the diagram out within maxWidth,
// and scale fits the layout it returns to the chat.
type diagramFit struct {
	maxPx, cellH       float64
	natural            float64
	maxWidth           int
	maxRows, maxPixels int
}

func newDiagramFit(req diagramRequest) diagramFit {
	ch := float64(req.cellH)
	maxPx := float64(req.maxCols) * float64(req.cellW)
	natural := diagramTextCellRatio * ch / peleBaseFontPx
	fit := diagramFit{
		maxPx: maxPx, cellH: ch, natural: natural, maxWidth: int(maxPx / natural),
		maxRows: diagramMaxRows, maxPixels: mermaid.MaxPixels,
	}
	if req.key.kind == diagramFull {
		fit.maxRows, fit.maxPixels = diagramMaxFullRows, diagramFullMaxPixels
	}
	return fit
}

// scale is the scale to draw a w×h layout at, and whether a cap or the
// width squeezed it.
func (s diagramFit) scale(w, h float64) (scale float64, squeezed bool, err error) {
	if w <= 0 || h <= 0 {
		return 0, false, errors.New("empty diagram")
	}
	scale = math.Min(s.natural, s.maxPx/w)
	scale = math.Min(scale, float64(s.maxRows)*s.cellH/h)
	scale = math.Min(scale, math.Sqrt(float64(s.maxPixels)/(w*h)))
	// Pele lays a diagram out narrower when it reaches MaxWidth, so one
	// that fills it may have been rearranged to fit.
	return scale, scale < s.natural || w >= float64(s.maxWidth-1), nil
}

func (f *graphicsFeature) openInBrowser(path string) tea.Cmd {
	return func() tea.Msg {
		if err := f.openURL(fileURL(path)); err != nil {
			return util.NewErrorMsg(fmt.Errorf("open diagram: %w", err))
		}
		return util.NewInfoMsg("Opened diagram in browser")
	}
}

// fileURL is the file:// URL for path, which may be a Windows path with a
// drive letter and backslashes.
func fileURL(path string) string {
	slashed := strings.ReplaceAll(path, `\`, "/")
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// fillBehind draws opaque bg under img in place, sparing a second
// full-size image. img's pixels are premultiplied, so each channel gains
// bg's in proportion to what the pixel leaves uncovered.
func fillBehind(img *image.RGBA, bg color.RGBA) {
	under := [4]uint32{uint32(bg.R), uint32(bg.G), uint32(bg.B), 0xff}
	for i := 0; i < len(img.Pix); i += 4 {
		uncovered := 0xff - uint32(img.Pix[i+3])
		for c := range under {
			img.Pix[i+c] += uint8((under[c]*uncovered + 0x7f) / 0xff)
		}
	}
}
