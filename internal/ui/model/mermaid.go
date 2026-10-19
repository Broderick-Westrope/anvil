package model

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/draw"
	"log/slog"
	"math"
	"sync"

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

	requests  chan diagramRequest
	results   chan diagramReadyMsg
	startOnce sync.Once
	listening bool
	supported bool
}

func newMermaidFeature(host diagramHost, caps *common.Capabilities) *mermaidFeature {
	return &mermaidFeature{
		host:     host,
		caps:     caps,
		entries:  map[diagramKey]*diagramEntry{},
		requests: make(chan diagramRequest, 64),
		results:  make(chan diagramReadyMsg, 64),
	}
}

// Diagram implements chat.DiagramRenderer. It runs during rendering, so it
// only reads state and queues work.
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
	if e, ok := f.entries[key]; ok {
		if e.state != diagramReady {
			return "", false
		}
		out := fimage.EncodingKitty.Render(key.id(), e.cols, e.rows)
		return out, out != ""
	}
	select {
	case f.requests <- diagramRequest{key: key, source: source, cellW: cellW, cellH: cellH, maxCols: width}:
		f.entries[key] = &diagramEntry{state: diagramPending}
		f.startOnce.Do(func() { go f.work() })
	default:
		// Queue full: try again on a later render.
	}
	return "", false
}

// work renders diagrams one at a time; the goja runtime isn't safe for
// concurrent use.
func (f *mermaidFeature) work() {
	r, err := mermaid.NewRenderer()
	for req := range f.requests {
		if err != nil {
			f.results <- diagramReadyMsg{key: req.key, err: err}
			continue
		}
		f.results <- renderDiagram(r, req)
	}
}

func renderDiagram(r *mermaid.Renderer, req diagramRequest) diagramReadyMsg {
	fail := func(err error) diagramReadyMsg { return diagramReadyMsg{key: req.key, err: err} }
	cw, ch := float64(req.cellW), float64(req.cellH)
	maxPx := float64(req.maxCols) * cw
	scale := diagramTextCellRatio * ch / peleBaseFontPx

	res, err := r.Render(req.source, mermaid.Options{MaxWidth: int(maxPx / scale)})
	if err != nil {
		return fail(err)
	}
	if res.Width <= 0 || res.Height <= 0 {
		return fail(fmt.Errorf("empty diagram"))
	}
	scale = math.Min(scale, maxPx/res.Width)
	scale = math.Min(scale, diagramMaxRows*ch/res.Height)

	img, err := mermaid.Rasterize(res, mermaid.TokyoNight, scale)
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
		if msg.err != nil {
			slog.Warn("Failed to render mermaid diagram", "err", msg.err)
			f.setState(msg.key, diagramFailed, 0, 0)
			return f.listen, true
		}
		f.setState(msg.key, diagramPending, msg.cols, msg.rows)
		_, tmux := f.caps.Env.LookupEnv("TMUX")
		cs := fimage.CellSize{Width: msg.cellW, Height: msg.cellH}
		transmit := fimage.EncodingKitty.Transmit(msg.key.id(), msg.img, cs, msg.cols, msg.rows, tmux)
		shown := func() tea.Msg { return diagramShownMsg{key: msg.key} }
		return tea.Batch(f.listen, tea.Sequence(transmit, shown)), true
	case diagramShownMsg:
		f.mu.Lock()
		if e, ok := f.entries[msg.key]; ok {
			e.state = diagramReady
		}
		f.mu.Unlock()
		f.host.InvalidateChat()
		return nil, true
	}
	return nil, false
}

func (f *mermaidFeature) setState(key diagramKey, state diagramState, cols, rows int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[key] = &diagramEntry{state: state, cols: cols, rows: rows}
}

func (f *mermaidFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (f *mermaidFeature) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }
