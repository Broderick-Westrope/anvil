package model

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"image"
	"image/draw"
	"log/slog"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
)

// SPIKE: renders ```mermaid fences in finished assistant messages as Kitty
// graphics. See internal/mermaid.

// diagramHost is what the mermaid feature needs from UI.
type diagramHost interface {
	// InvalidateChat drops every chat item's render cache so diagrams
	// that just became ready replace their code blocks.
	InvalidateChat()
}

func (h uiHost) InvalidateChat() {
	if h.m.chat != nil {
		h.m.chat.InvalidateRenderCaches()
	}
}

const (
	// diagramMaxRows caps a diagram's height so one diagram can't take
	// over the chat.
	diagramMaxRows = 40
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

type diagramKey struct {
	hash  uint64
	width int
}

func (k diagramKey) id() string { return fmt.Sprintf("mermaid-%x-%d", k.hash, k.width) }

type diagramState int

const (
	diagramPending diagramState = iota
	diagramReady
	diagramFailed
)

type diagramEntry struct {
	state      diagramState
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
	cellW, cellH int
	maxCols      int
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

type mermaidFeature struct {
	host diagramHost
	caps *common.Capabilities

	mu      sync.Mutex
	entries map[diagramKey]*diagramEntry
	// width is the chat width diagrams were last drawn at. Entries for
	// other widths are stale after a resize.
	width int
	clock uint64

	requests    chan diagramRequest
	results     chan diagramReadyMsg
	startOnce   sync.Once
	idleTimeout time.Duration
	cacheBudget int
	// enginesLoaded reports whether the worker holds the render engines.
	enginesLoaded atomic.Bool
	listening     bool
	supported     bool
}

func newMermaidFeature(host diagramHost, caps *common.Capabilities) *mermaidFeature {
	return &mermaidFeature{
		host:        host,
		caps:        caps,
		entries:     map[diagramKey]*diagramEntry{},
		requests:    make(chan diagramRequest, 64),
		results:     make(chan diagramReadyMsg, 64),
		idleTimeout: diagramIdleTimeout,
		cacheBudget: diagramCacheBudget,
	}
}

// Diagram implements chat.DiagramRenderer. It runs during rendering, so it
// only touches the feature's own state and queues work.
func (f *mermaidFeature) Diagram(source string, width int) (string, bool) {
	if !f.caps.SupportsKittyGraphics() {
		return "", false
	}
	cellW, cellH := f.caps.CellSize()
	if cellW == 0 || cellH == 0 || width <= 0 {
		return "", false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(source))
	key := diagramKey{hash: h.Sum64(), width: width}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.width = width
	f.clock++
	if e, ok := f.entries[key]; ok {
		e.lastUsed = f.clock
		if e.state != diagramReady {
			return "", false
		}
		if out := fimage.EncodingKitty.Render(key.id(), e.cols, e.rows); out != "" {
			return out, true
		}
		// The image cache was reset (e.g. on session switch) or the
		// entry was evicted: render it again.
		delete(f.entries, key)
	}
	select {
	case f.requests <- diagramRequest{key: key, source: source, cellW: cellW, cellH: cellH, maxCols: width}:
		f.entries[key] = &diagramEntry{state: diagramPending, lastUsed: f.clock}
		f.startOnce.Do(func() { go f.work() })
	default:
		// Queue full: try again on a later render.
	}
	return "", false
}

// diagramEngines is everything a render needs. The worker owns it alone,
// since neither engine is safe for concurrent use.
type diagramEngines struct {
	pele   *mermaid.Renderer
	raster *mermaid.Rasterizer
}

func loadDiagramEngines() (*diagramEngines, error) {
	pele, err := mermaid.NewRenderer()
	if err != nil {
		return nil, err
	}
	raster, err := mermaid.NewRasterizer(context.Background())
	if err != nil {
		return nil, err
	}
	return &diagramEngines{pele: pele, raster: raster}, nil
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
				var err error
				if engines, err = loadDiagramEngines(); err != nil {
					f.results <- diagramReadyMsg{key: req.key, err: err}
					continue
				}
				f.enginesLoaded.Store(true)
			}
			f.results <- renderDiagram(engines, req)
			idle.Reset(f.idleTimeout)
		case <-idle.C:
			if engines != nil {
				_ = engines.raster.Close()
				engines = nil
				f.enginesLoaded.Store(false)
			}
		}
	}
}

func renderDiagram(e *diagramEngines, req diagramRequest) diagramReadyMsg {
	fail := func(err error) diagramReadyMsg { return diagramReadyMsg{key: req.key, err: err} }
	cw, ch := float64(req.cellW), float64(req.cellH)
	maxPx := float64(req.maxCols) * cw
	scale := diagramTextCellRatio * ch / peleBaseFontPx

	res, err := e.pele.Render(req.source, mermaid.Options{MaxWidth: int(maxPx / scale)})
	if err != nil {
		return fail(err)
	}
	if res.Width <= 0 || res.Height <= 0 {
		return fail(fmt.Errorf("empty diagram"))
	}
	scale = math.Min(scale, maxPx/res.Width)
	scale = math.Min(scale, diagramMaxRows*ch/res.Height)

	img, err := e.raster.Rasterize(res, mermaid.TokyoNight, scale)
	if err != nil {
		return fail(err)
	}
	b := img.Bounds()
	cols := int(math.Ceil(float64(b.Dx()) / cw))
	rows := int(math.Ceil(float64(b.Dy()) / ch))

	// Pad to whole cells so the image fills its placeholder grid exactly
	// and terminals that stretch to the grid (Herdr) keep the aspect.
	padded := image.NewRGBA(image.Rect(0, 0, cols*req.cellW, rows*req.cellH))
	draw.Draw(padded, b, img, b.Min, draw.Src)
	return diagramReadyMsg{key: req.key, img: padded, cols: cols, rows: rows, cellW: req.cellW, cellH: req.cellH}
}

func (f *mermaidFeature) listen() tea.Msg { return <-f.results }

// Observe starts listening for rendered diagrams, and re-renders the chat
// when Kitty graphics support is first detected.
func (f *mermaidFeature) Observe(tea.Msg) tea.Cmd {
	if supported := f.caps.SupportsKittyGraphics(); supported != f.supported {
		f.supported = supported
		if supported {
			f.host.InvalidateChat()
		}
	}
	if f.listening {
		return nil
	}
	f.listening = true
	return f.listen
}

func (f *mermaidFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case diagramReadyMsg:
		f.mu.Lock()
		defer f.mu.Unlock()
		if msg.err != nil {
			slog.Warn("Failed to render mermaid diagram", "err", msg.err)
			f.entries[msg.key] = &diagramEntry{state: diagramFailed}
			return f.listen, true
		}
		e := f.entries[msg.key]
		if e == nil || msg.key.width != f.width {
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

func (f *mermaidFeature) tmux() bool {
	_, ok := f.caps.Env.LookupEnv("TMUX")
	return ok
}

// evictLocked drops transmitted diagrams drawn at another width, then the
// least recently drawn until the rest fit cacheBudget. keep is never
// dropped. It returns the commands that free them in the terminal.
func (f *mermaidFeature) evictLocked(keep diagramKey) tea.Cmd {
	var held []diagramKey
	total := 0
	var cmds []tea.Cmd
	for key, e := range f.entries {
		if e.bytes == 0 {
			continue
		}
		if key.width != f.width && key != keep {
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

func (f *mermaidFeature) forgetLocked(key diagramKey) tea.Cmd {
	e := f.entries[key]
	delete(f.entries, key)
	return fimage.EncodingKitty.Forget(key.id(), e.cols, e.rows, f.tmux())
}

func (f *mermaidFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (f *mermaidFeature) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }
