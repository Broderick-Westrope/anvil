package model

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/pkg/browser"
)

// mermaidFeature renders ```mermaid fences in finished assistant messages
// as Kitty graphics, shows one full size in a drill-in, and opens one in
// the browser. See internal/mermaid.

// diagramHost is what the mermaid feature needs from UI.
type diagramHost interface {
	// InvalidateChat drops every chat item's render cache, in the root
	// chat and every drill-in, so diagrams that just became ready replace
	// their placeholders.
	InvalidateChat()
	// AppBackground is the background Anvil paints the terminal with, or
	// nil when the TUI is transparent and the terminal's own shows.
	AppBackground() color.Color
	// PushDrillIn opens c as a drill-in labelled label, focuses it and
	// blurs the editor, as the tool and thinking drill-ins do.
	PushDrillIn(c *Chat, label string)
	// DrillInOpen reports whether c is still on the drill-in stack.
	DrillInOpen(c *Chat) bool
}

func (h uiHost) InvalidateChat() {
	if h.m.chat != nil {
		h.m.chat.InvalidateRenderCaches()
	}
	for _, entry := range h.m.drillStack {
		if entry.chat != nil {
			entry.chat.InvalidateRenderCaches()
		}
	}
}

func (h uiHost) AppBackground() color.Color {
	if h.m.isTransparent {
		return nil
	}
	return h.m.com.Styles.Background
}

func (h uiHost) PushDrillIn(c *Chat, label string) {
	c.SetFollow(false)
	c.Focus()
	h.m.drillStack = append(h.m.drillStack, drillInEntry{chat: c, label: label})
	h.m.textarea.Blur()
	h.m.focus = uiFocusMain
	h.m.updateLayoutAndSize()
	// Sizing a new chat pins it to the bottom; start at the top.
	c.ScrollToTop()
}

func (h uiHost) DrillInOpen(c *Chat) bool {
	return slices.ContainsFunc(h.m.drillStack, func(e drillInEntry) bool { return e.chat == c })
}

const (
	// diagramMaxRows caps a diagram's height in the chat so one diagram
	// can't take over. The drill-in shows it uncapped.
	diagramMaxRows = 40
	// diagramMaxFullRows is the most rows a Kitty placeholder can address.
	diagramMaxFullRows = 297
	// diagramFullMaxPixels caps a drill-in image (64 MB of RGBA). There's
	// only ever one, and it's freed when the drill-in closes.
	diagramFullMaxPixels = 16 << 20
	// diagramTextCellRatio is the height of Pele's 16px label text as a
	// fraction of a terminal cell, so diagram text reads like the chat's.
	diagramTextCellRatio = 0.8
	peleBaseFontPx       = 16
	// diagramIdleTimeout is how long the render engines (about 95 MB of
	// process memory together) stay loaded after the last render.
	diagramIdleTimeout = 2 * time.Minute
	// diagramCacheBudget caps the decoded pixels kept for diagrams that
	// are already in the terminal. Past it, the least recently drawn are
	// dropped and render again if they scroll back into view.
	diagramCacheBudget = 32 << 20
)

// diagramKind is where a diagram is drawn.
type diagramKind int

const (
	diagramInline diagramKind = iota
	diagramFull
)

type diagramKey struct {
	hash  uint64
	width int
	kind  diagramKind
}

func (k diagramKey) id() string { return fmt.Sprintf("mermaid-%x-%d-%d", k.hash, k.width, k.kind) }

type diagramState int

const (
	diagramPending diagramState = iota
	diagramReady
	diagramFailed
)

type diagramEntry struct {
	state diagramState
	// err is why the render failed.
	err        string
	cols, rows int
	// bytes is the decoded image held in the image cache once the
	// diagram is transmitted.
	bytes int
	// lastUsed orders entries for eviction.
	lastUsed uint64
}

type diagramRequest struct {
	key          diagramKey
	source       string
	theme        mermaid.Theme
	cellW, cellH int
	maxCols      int
	// export writes the diagram as a page to open in the browser instead
	// of rasterizing it.
	export bool
}

type diagramReadyMsg struct {
	key        diagramKey
	img        image.Image
	cols, rows int
	cellW      int
	cellH      int
	err        error
}

type diagramShownMsg struct{ key diagramKey }

type diagramExportedMsg struct {
	path string
	err  error
}

type mermaidFeature struct {
	host diagramHost
	caps *common.Capabilities
	com  *common.Common

	mu      sync.Mutex
	entries map[diagramKey]*diagramEntry
	// widths are the widths each kind was last drawn at. Entries for
	// other widths are stale after a resize.
	widths [2]int
	clock  uint64
	// termBg is the terminal's own background, for when the TUI is
	// transparent.
	termBg string

	// drill is the open diagram drill-in's chat, or nil.
	drill *Chat

	requests    chan diagramRequest
	results     chan tea.Msg
	startOnce   sync.Once
	idleTimeout time.Duration
	cacheBudget int
	exportDir   string
	// enginesLoaded reports whether the worker holds the render engines.
	enginesLoaded atomic.Bool
	listening     bool
	supported     bool
}

func newMermaidFeature(host diagramHost, caps *common.Capabilities, com *common.Common) *mermaidFeature {
	return &mermaidFeature{
		host:        host,
		caps:        caps,
		com:         com,
		entries:     map[diagramKey]*diagramEntry{},
		requests:    make(chan diagramRequest, 64),
		results:     make(chan tea.Msg, 64),
		idleTimeout: diagramIdleTimeout,
		cacheBudget: diagramCacheBudget,
		exportDir:   filepath.Join(os.TempDir(), "anvil-diagrams"),
	}
}

// Diagram implements chat.DiagramRenderer.
func (f *mermaidFeature) Diagram(source string, width int) chat.DiagramView {
	return f.diagram(source, width, diagramInline)
}

// FullDiagram implements chat.DiagramRenderer.
func (f *mermaidFeature) FullDiagram(source string, width int) chat.DiagramView {
	return f.diagram(source, width, diagramFull)
}

// theme is the palette diagrams draw with, from the styles. pele-bg fills
// the boxes behind edge labels, so it has to be the colour around the
// transparent image: Anvil's background, the terminal's own when the TUI
// is transparent, or the theme's until the terminal reports it.
func (f *mermaidFeature) theme() mermaid.Theme {
	p := f.com.Styles.Diagram
	bg := cmp.Or(hexColor(f.host.AppBackground()), f.termBg, hexColor(f.com.Styles.Background))
	return mermaid.Theme{
		"pele-bg":           bg,
		"pele-fg":           hexColor(p.Fg),
		"pele-muted":        hexColor(p.Muted),
		"pele-line":         hexColor(p.Line),
		"pele-surface":      hexColor(p.Surface),
		"pele-surface-alt":  hexColor(p.SurfaceAlt),
		"pele-border":       hexColor(p.Border),
		"pele-accent":       hexColor(p.Accent),
		"pele-critical":     hexColor(p.Critical),
		"pele-error":        hexColor(p.Error),
		"pele-series-1":     hexColor(p.Series[0]),
		"pele-series-2":     hexColor(p.Series[1]),
		"pele-series-3":     hexColor(p.Series[2]),
		"pele-series-4":     hexColor(p.Series[3]),
		"pele-series-5":     hexColor(p.Series[4]),
		"pele-series-6":     hexColor(p.Series[5]),
		"pele-series-7":     hexColor(p.Series[6]),
		"pele-series-8":     hexColor(p.Series[7]),
		"pele-font":         "sans-serif",
		"pele-font-mono":    "monospace",
		"pele-title-weight": "bold",
	}
}

func hexColor(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

func sourceHash(source string, theme mermaid.Theme) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(source))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(theme["pele-bg"]))
	return h.Sum64()
}

// diagram runs during rendering, so it only touches the feature's own state
// and queues work.
func (f *mermaidFeature) diagram(source string, width int, kind diagramKind) chat.DiagramView {
	cellW, cellH := f.caps.CellSize()
	if !f.caps.SupportsKittyGraphics() || cellW == 0 || cellH == 0 || width <= 0 {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}
	theme := f.theme()
	key := diagramKey{hash: sourceHash(source, theme), width: width, kind: kind}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.widths[kind] = width
	f.clock++
	if e, ok := f.entries[key]; ok {
		e.lastUsed = f.clock
		switch e.state {
		case diagramPending:
			return chat.DiagramView{State: chat.DiagramPending}
		case diagramFailed:
			return chat.DiagramView{State: chat.DiagramFailed, Err: e.err}
		}
		if out := fimage.EncodingKitty.Render(key.id(), e.cols, e.rows); out != "" {
			return chat.DiagramView{State: chat.DiagramReady, Image: out}
		}
		// The image cache was reset (e.g. on session switch) or the
		// entry was evicted: render it again.
		delete(f.entries, key)
	}
	req := diagramRequest{key: key, source: source, theme: theme, cellW: cellW, cellH: cellH, maxCols: width}
	if f.enqueue(req) {
		f.entries[key] = &diagramEntry{state: diagramPending, lastUsed: f.clock}
	}
	return chat.DiagramView{State: chat.DiagramPending}
}

// enqueue hands req to the worker, starting it if needed, and reports false
// when the queue is full.
func (f *mermaidFeature) enqueue(req diagramRequest) bool {
	select {
	case f.requests <- req:
		f.startOnce.Do(func() { go f.work() })
		return true
	default:
		return false
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
	scale := diagramTextCellRatio * ch / peleBaseFontPx

	res, err := e.pele.Render(req.source, mermaid.Options{MaxWidth: int(maxPx / scale)})
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
	return diagramReadyMsg{key: req.key, img: padded, cols: cols, rows: rows, cellW: req.cellW, cellH: req.cellH}
}

func (f *mermaidFeature) listen() tea.Msg { return <-f.results }

// Observe starts listening for rendered diagrams, asks the terminal for its
// background, re-renders the chat when Kitty graphics support is first
// detected, and frees the full-size image once its drill-in closes.
func (f *mermaidFeature) Observe(msg tea.Msg) tea.Cmd {
	if bg, ok := msg.(tea.BackgroundColorMsg); ok {
		f.termBg = hexColor(bg.Color)
	}
	if supported := f.caps.SupportsKittyGraphics(); supported != f.supported {
		f.supported = supported
		if supported {
			f.host.InvalidateChat()
		}
	}
	var cmds []tea.Cmd
	if f.drill != nil && !f.host.DrillInOpen(f.drill) {
		f.drill = nil
		cmds = append(cmds, f.forgetKind(diagramFull))
	}
	if !f.listening {
		f.listening = true
		cmds = append(cmds, f.listen, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

func (f *mermaidFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case chat.DiagramOpenMsg:
		return f.open(msg), true
	case diagramExportedMsg:
		if msg.err != nil {
			slog.Warn("Failed to export mermaid diagram", "err", msg.err)
			return tea.Batch(f.listen, util.ReportError(fmt.Errorf("open diagram: %w", msg.err))), true
		}
		return tea.Batch(f.listen, openInBrowser(msg.path)), true
	case diagramReadyMsg:
		if msg.err != nil {
			slog.Warn("Failed to render mermaid diagram", "err", msg.err)
			f.mu.Lock()
			f.entries[msg.key] = &diagramEntry{state: diagramFailed, err: msg.err.Error()}
			f.mu.Unlock()
			f.host.InvalidateChat()
			return f.listen, true
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		e := f.entries[msg.key]
		if e == nil || msg.key.width != f.widths[msg.key.kind] {
			// Resized while it rendered; it would be evicted at once.
			delete(f.entries, msg.key)
			return f.listen, true
		}
		e.cols, e.rows = msg.cols, msg.rows
		e.bytes = msg.cols * msg.cellW * msg.rows * msg.cellH * 4
		cs := fimage.CellSize{Width: msg.cellW, Height: msg.cellH}
		transmit := fimage.EncodingKitty.Transmit(msg.key.id(), msg.img, cs, msg.cols, msg.rows, f.tmux())
		shown := func() tea.Msg { return diagramShownMsg{key: msg.key} }
		return tea.Batch(f.listen, tea.Sequence(transmit, shown)), true
	case diagramShownMsg:
		f.mu.Lock()
		e, ok := f.entries[msg.key]
		if ok {
			e.state = diagramReady
		}
		evict := f.evictLocked(msg.key)
		f.mu.Unlock()
		if ok {
			f.host.InvalidateChat()
		}
		return evict, true
	}
	return nil, false
}

// open shows a diagram in a drill-in, or in the browser when asked to.
func (f *mermaidFeature) open(msg chat.DiagramOpenMsg) tea.Cmd {
	if msg.External {
		theme := f.theme()
		req := diagramRequest{key: diagramKey{hash: sourceHash(msg.Source, theme)}, source: msg.Source, theme: theme, export: true}
		if !f.enqueue(req) {
			return util.ReportWarn("Diagram renderer is busy; try again")
		}
		return util.ReportInfo("Opening diagram in browser")
	}
	c := NewChat(f.com)
	c.SetMessages(chat.NewDiagramDetailItem(f.com.Styles, msg.Source, f))
	c.SelectLast()
	f.host.PushDrillIn(c, "Diagram")
	var cmd tea.Cmd
	if f.drill != nil {
		// Replacing one diagram drill-in with another.
		cmd = f.forgetKind(diagramFull)
	}
	f.drill = c
	return cmd
}

func openInBrowser(path string) tea.Cmd {
	return func() tea.Msg {
		if err := browser.OpenURL("file://" + path); err != nil {
			return util.NewErrorMsg(fmt.Errorf("open diagram: %w", err))
		}
		return util.NewInfoMsg("Opened diagram in browser")
	}
}

func (f *mermaidFeature) tmux() bool {
	_, ok := f.caps.Env.LookupEnv("TMUX")
	return ok
}

// evictLocked drops transmitted diagrams drawn at a width their kind no
// longer uses, then the least recently drawn until the rest fit
// cacheBudget. keep is never dropped. It returns the commands that free
// them in the terminal.
func (f *mermaidFeature) evictLocked(keep diagramKey) tea.Cmd {
	var held []diagramKey
	total := 0
	var cmds []tea.Cmd
	for key, e := range f.entries {
		if e.bytes == 0 {
			continue
		}
		if key.width != f.widths[key.kind] && key != keep {
			cmds = append(cmds, f.forgetLocked(key))
			continue
		}
		held = append(held, key)
		total += e.bytes
	}
	slices.SortFunc(held, func(a, b diagramKey) int {
		return cmp.Compare(f.entries[a].lastUsed, f.entries[b].lastUsed)
	})
	for _, key := range held {
		if total <= f.cacheBudget {
			break
		}
		if key == keep {
			continue
		}
		total -= f.entries[key].bytes
		cmds = append(cmds, f.forgetLocked(key))
	}
	return tea.Batch(cmds...)
}

// forgetKind drops every diagram of kind.
func (f *mermaidFeature) forgetKind(kind diagramKind) tea.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []diagramKey
	for key := range f.entries {
		if key.kind == kind {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b diagramKey) int { return cmp.Compare(a.hash, b.hash) })
	var cmds []tea.Cmd
	for _, key := range keys {
		cmds = append(cmds, f.forgetLocked(key))
	}
	return tea.Batch(cmds...)
}

func (f *mermaidFeature) forgetLocked(key diagramKey) tea.Cmd {
	e := f.entries[key]
	delete(f.entries, key)
	if e.bytes == 0 {
		return nil
	}
	return fimage.EncodingKitty.Forget(key.id(), e.cols, e.rows, f.tmux())
}

func (f *mermaidFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (f *mermaidFeature) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }
