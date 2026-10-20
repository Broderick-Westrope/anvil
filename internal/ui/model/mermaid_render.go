package model

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/pkg/browser"
)

// This file is the mermaid feature's worker side: it owns the render
// engines, turns requests into images or browser pages, and never touches
// the feature's cache, which mermaid.go owns.

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

// pruneExports deletes pages in dir older than diagramExportMaxAge.
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
			slog.Warn("Failed to remove old diagram page", "err", err)
		}
	}
}

// diagramEngines is everything a render needs. The worker owns it alone,
// since neither engine is safe for concurrent use. The rasterizer loads on
// the first image, so exporting alone doesn't compile resvg.
type diagramEngines struct {
	pele   *mermaid.Renderer
	raster *mermaid.Rasterizer
}

func (e *diagramEngines) rasterizer() (*mermaid.Rasterizer, error) {
	if e.raster == nil {
		r, err := mermaid.NewRasterizer(context.Background())
		if err != nil {
			return nil, err
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
func (f *mermaidFeature) work() {
	var engines *diagramEngines
	idle := time.NewTimer(f.idleTimeout)
	idle.Stop()
	for {
		select {
		case req := <-f.requests:
			if engines == nil {
				pele, err := mermaid.NewRenderer()
				if err != nil {
					f.results <- f.failure(req, err)
					continue
				}
				engines = &diagramEngines{pele: pele}
				f.enginesLoaded.Store(true)
			}
			if req.export {
				f.results <- f.export(engines, req)
			} else {
				f.results <- renderDiagram(engines, req)
			}
			idle.Reset(f.idleTimeout)
		case <-idle.C:
			if engines != nil {
				engines.close()
				engines = nil
				f.enginesLoaded.Store(false)
				f.returnMemory()
			}
		}
	}
}

func (f *mermaidFeature) failure(req diagramRequest, err error) tea.Msg {
	if req.export {
		return diagramExportedMsg{err: err}
	}
	return diagramReadyMsg{key: req.key, err: err}
}

func (f *mermaidFeature) export(e *diagramEngines, req diagramRequest) tea.Msg {
	res, err := e.pele.Render(req.source, mermaid.Options{})
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
	maxPx := float64(req.maxCols) * cw
	natural := diagramTextCellRatio * ch / peleBaseFontPx
	scale := natural

	maxWidth := int(maxPx / scale)
	res, err := e.pele.Render(req.source, mermaid.Options{MaxWidth: maxWidth})
	if err != nil {
		return fail(err)
	}
	if res.Width <= 0 || res.Height <= 0 {
		return fail(errors.New("empty diagram"))
	}
	scale = math.Min(scale, maxPx/res.Width)
	maxRows, maxPixels := diagramMaxRows, mermaid.MaxPixels
	if req.key.kind == diagramFull {
		maxRows, maxPixels = diagramMaxFullRows, diagramFullMaxPixels
	}
	scale = math.Min(scale, float64(maxRows)*ch/res.Height)
	scale = math.Min(scale, math.Sqrt(float64(maxPixels)/(res.Width*res.Height)))

	// Pele lays a diagram out narrower when it reaches MaxWidth, so one
	// that fills it may have been rearranged to fit.
	squeezed := scale < natural || res.Width >= float64(maxWidth-1)

	img, err := raster.Rasterize(res, req.theme, mermaid.RasterOptions{Scale: scale, MaxPixels: maxPixels})
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

func openInBrowser(path string) tea.Cmd {
	return func() tea.Msg {
		if err := browser.OpenURL(fileURL(path)); err != nil {
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
