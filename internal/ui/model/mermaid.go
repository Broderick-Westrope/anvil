package model

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
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
	// diagramIdleTimeout is how long the render engines (about 22 MB of
	// process memory, nearly all of it resvg) stay loaded after the last
	// render.
	// Reloading them costs about 90 ms once resvg is in the compile
	// cache, 370 ms before.
	diagramIdleTimeout = 20 * time.Second
	// diagramCacheBudget caps the pixels of the diagrams held in the
	// terminal (Anvil keeps none once sent) that are out of view. Past it,
	// the least recently drawn are deleted and render again if they scroll
	// back into view. A wide diagram on a HiDPI screen is ~17 MB, and
	// Ghostty's default image storage is 320 MB.
	diagramCacheBudget = 128 << 20
	// diagramExportMaxAge is how long a page exported for the browser is
	// kept; older ones are deleted when the next is written.
	diagramExportMaxAge = 24 * time.Hour
	// diagramCopyScale is the scale a copied image is drawn at: twice the
	// layout's size, so it stays sharp on HiDPI screens.
	diagramCopyScale = 2
)

// diagramKind is where a diagram is drawn.
type diagramKind int

const (
	diagramInline diagramKind = iota
	diagramFull
)

// diagramKey names one image of a diagram. The cell size is part of it, so
// a result rendered for cells the terminal no longer has never matches.
type diagramKey struct {
	hash         uint64
	width        int
	kind         diagramKind
	cellW, cellH int
}

func (k diagramKey) id() string {
	return fmt.Sprintf("mermaid-%x-%d-%d-%dx%d", k.hash, k.width, k.kind, k.cellW, k.cellH)
}

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
	// bytes is the size of the image the terminal holds once the
	// diagram is transmitted.
	bytes int
	// lastUsed orders entries for eviction.
	lastUsed uint64
	// squeezed reports that a cap or the width shrank the image, so the
	// full-size view would differ from it.
	squeezed bool
	// transient reports that the failure may not happen again, and
	// retried that the diagram was already tried again after one.
	transient, retried bool
}

type diagramRequest struct {
	key          diagramKey
	source       string
	theme        mermaid.Theme
	cellW, cellH int
	maxCols      int
	job          diagramJob
}

// diagramJob is what the worker makes of a request.
type diagramJob int

const (
	// diagramJobRender rasterizes the diagram for the terminal.
	diagramJobRender diagramJob = iota
	// diagramJobExport writes it as a page to open in the browser.
	diagramJobExport
	// diagramJobCopy encodes it as a PNG for the clipboard.
	diagramJobCopy
)

type diagramReadyMsg struct {
	key        diagramKey
	img        image.Image
	cols, rows int
	squeezed   bool
	cellW      int
	cellH      int
	err        error
}

// diagramShownMsg follows a diagram's transmit command, once the image is
// in the terminal.
type diagramShownMsg struct {
	key        diagramKey
	cols, rows int
}

type diagramExportedMsg struct {
	path string
	err  error
}

type diagramCopiedMsg struct {
	png []byte
	err error
}

// mermaidFeature renders ```mermaid fences in finished assistant messages
// as Kitty graphics, shows one full size in a drill-in, opens one in the
// browser, and copies one to the clipboard. See internal/mermaid.
type mermaidFeature struct {
	host featureHost
	caps *common.Capabilities
	com  *common.Common

	mu      sync.Mutex
	entries map[diagramKey]*diagramEntry
	// widths are the widths each kind was last drawn at. Entries for
	// other widths are stale after a resize.
	widths [2]int
	clock  uint64
	// inView are the chat images on or near the chat's view, which are
	// never evicted (see InView).
	inView map[diagramKey]bool
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
	// compileCacheDir is where resvg's compiled code is shared between
	// processes; empty compiles it on every load.
	compileCacheDir string
	// palette is the theme without its background, and paletteHash its
	// hash; see initPalette.
	paletteOnce sync.Once
	palette     mermaid.Theme
	paletteHash uint64
	// returnMemory hands memory freed by releasing the engines back to
	// the OS; Go would otherwise keep it, and the process stays at its
	// peak.
	returnMemory func()
	// newRenderer loads the layout engine; tests make it fail.
	newRenderer func() (*mermaid.Renderer, error)
	// openURL opens a page in the user's browser.
	openURL func(string) error
	// writeImage puts a PNG on the system clipboard.
	writeImage func([]byte) error
	listening  bool
	// drawn is what diagrams were last drawn under.
	drawn drawConditions
}

// drawConditions are what decide whether diagrams draw, how big, and
// against which background. The chat caches its renders, so it has to be
// invalidated when any of them change.
type drawConditions struct {
	kitty        bool
	cellW, cellH int
	bg           string
}

func (f *mermaidFeature) conditions() drawConditions {
	cellW, cellH := f.caps.CellSize()
	return drawConditions{kitty: f.caps.SupportsKittyGraphics(), cellW: cellW, cellH: cellH, bg: f.background()}
}

// usable reports whether diagrams can be drawn at all.
func (c drawConditions) usable() bool { return c.kitty && c.cellW > 0 && c.cellH > 0 }

func newMermaidFeature(host featureHost, caps *common.Capabilities, com *common.Common) *mermaidFeature {
	return &mermaidFeature{
		host:            host,
		caps:            caps,
		com:             com,
		entries:         map[diagramKey]*diagramEntry{},
		requests:        make(chan diagramRequest, 64),
		results:         make(chan tea.Msg, 64),
		idleTimeout:     diagramIdleTimeout,
		cacheBudget:     diagramCacheBudget,
		exportDir:       diagramExportDir(),
		compileCacheDir: diagramCompileCacheDir(),
		returnMemory:    debug.FreeOSMemory,
		newRenderer:     mermaid.NewRenderer,
		openURL:         browser.OpenURL,
		writeImage:      writeClipboardImage,
	}
}

// Diagram implements chat.DiagramRenderer.
func (f *mermaidFeature) Diagram(source string, width int) chat.DiagramView {
	return f.lookup(source, width, diagramInline, true)
}

// FullDiagram implements chat.DiagramRenderer. When the chat's image of the
// diagram wasn't squeezed it is already full size, so the drill-in shows
// it; otherwise it stands in while the full-size image renders.
func (f *mermaidFeature) FullDiagram(source string, width int) chat.DiagramView {
	inline, squeezed := f.inlineImage(source, width)
	if inline != "" && !squeezed {
		return chat.DiagramView{State: chat.DiagramReady, Image: inline}
	}
	view := f.lookup(source, width, diagramFull, true)
	if view.State == chat.DiagramPending {
		view.Image = inline
	}
	return view
}

// inlineImage is the chat's image of source if it is in the terminal and
// fits width, and whether it was squeezed.
func (f *mermaidFeature) inlineImage(source string, width int) (string, bool) {
	cellW, cellH := f.caps.CellSize()
	f.mu.Lock()
	defer f.mu.Unlock()
	key := diagramKey{
		hash: f.imageHash(source, f.background()), width: f.widths[diagramInline], kind: diagramInline,
		cellW: cellW, cellH: cellH,
	}
	e, ok := f.entries[key]
	if !ok || e.state != diagramReady || e.cols > width {
		return "", false
	}
	f.clock++
	e.lastUsed = f.clock
	return fimage.EncodingKitty.Render(key.id(), e.cols, e.rows), e.squeezed
}

// background is the colour around a transparent diagram image.
func (f *mermaidFeature) background() string {
	return cmp.Or(hexColor(f.host.AppBackground()), f.termBg, hexColor(f.com.Styles.Background))
}

// theme is the palette diagrams draw with, from the styles. pele-bg fills
// the boxes behind edge labels, so it has to be the colour around the
// transparent image: Anvil's background, the terminal's own when the TUI
// is transparent, or the theme's until the terminal reports it.
func (f *mermaidFeature) theme() mermaid.Theme {
	f.initPalette()
	theme := maps.Clone(f.palette)
	theme["pele-bg"] = f.background()
	return theme
}

// initPalette builds the background-independent part of the theme once,
// since the styles don't change while Anvil runs.
func (f *mermaidFeature) initPalette() {
	f.paletteOnce.Do(func() {
		p := f.com.Styles.Diagram
		f.palette = mermaid.Theme{
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
		h := fnv.New64a()
		for _, name := range slices.Sorted(maps.Keys(f.palette)) {
			_, _ = h.Write([]byte(name + "=" + f.palette[name] + ";"))
		}
		f.paletteHash = h.Sum64()
	})
}

func hexColor(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// imageHash identifies source drawn with the whole theme on background bg.
func (f *mermaidFeature) imageHash(source, bg string) uint64 {
	f.initPalette()
	h := fnv.New64a()
	_, _ = h.Write([]byte(source))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(bg))
	var palette [8]byte
	binary.LittleEndian.PutUint64(palette[:], f.paletteHash)
	_, _ = h.Write(palette[:])
	return h.Sum64()
}

// InView implements chat.DiagramRenderer. It does what Diagram does for
// each image the chat shows or will soon, without building placeholders,
// and holds them against eviction until the next call.
func (f *mermaidFeature) InView(sources []string, width int) {
	inView := map[diagramKey]bool{}
	for _, source := range sources {
		if key, ok := f.imageKey(source, width, diagramInline); ok {
			inView[key] = true
		}
		f.lookup(source, width, diagramInline, false)
	}
	f.mu.Lock()
	f.inView = inView
	f.mu.Unlock()
}

// imageKey is the key of source's image of kind at width, or false when
// diagrams can't be drawn.
func (f *mermaidFeature) imageKey(source string, width int, kind diagramKind) (diagramKey, bool) {
	cellW, cellH := f.caps.CellSize()
	if !f.caps.SupportsKittyGraphics() || cellW == 0 || cellH == 0 || width <= 0 {
		return diagramKey{}, false
	}
	return diagramKey{hash: f.imageHash(source, f.background()), width: width, kind: kind, cellW: cellW, cellH: cellH}, true
}

// lookup finds source's image at width, queueing its render if there is
// none, and marks it drawn for eviction. Only with draw does it build the
// placeholders that show it. It runs during rendering, so it only touches
// the feature's own state and queues work.
func (f *mermaidFeature) lookup(source string, width int, kind diagramKind, draw bool) chat.DiagramView {
	key, ok := f.imageKey(source, width, kind)
	if !ok {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}

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
			if !e.transient || e.retried {
				return chat.DiagramView{State: chat.DiagramFailed, Err: e.err}
			}
			req := diagramRequest{key: key, source: source, theme: f.theme(), cellW: key.cellW, cellH: key.cellH, maxCols: width}
			if f.enqueue(req) {
				e.state, e.retried = diagramPending, true
			}
			return chat.DiagramView{State: chat.DiagramPending}
		}
		if !draw {
			if fimage.HasTransmitted(key.id(), e.cols, e.rows) {
				return chat.DiagramView{State: chat.DiagramReady}
			}
		} else if out := fimage.EncodingKitty.Render(key.id(), e.cols, e.rows); out != "" {
			return chat.DiagramView{State: chat.DiagramReady, Image: out}
		}
		// The image cache no longer has it, so the terminal may not
		// either: render it again.
		delete(f.entries, key)
	}
	req := diagramRequest{key: key, source: source, theme: f.theme(), cellW: key.cellW, cellH: key.cellH, maxCols: width}
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

func (f *mermaidFeature) listen() tea.Msg { return <-f.results }

// Observe starts listening for rendered diagrams, asks the terminal for its
// background, re-renders the chat when Kitty graphics support is first
// detected, and frees the full-size image once its drill-in closes.
func (f *mermaidFeature) Observe(msg tea.Msg) tea.Cmd {
	if bg, ok := msg.(tea.BackgroundColorMsg); ok {
		f.termBg = hexColor(bg.Color)
	}
	var cmds []tea.Cmd
	if now := f.conditions(); now != f.drawn {
		if f.drawn.usable() {
			// Images for the old cells or background would draw wrong;
			// results still rendering for them are dropped when they
			// arrive, as their entries are gone.
			cmds = append(cmds, f.forgetWhere(func(diagramKey) bool { return true }))
		}
		if f.drawn.usable() || now.usable() {
			// Placeholders for forgotten images, or code blocks that
			// can now be images, are cached in the chat.
			f.host.InvalidateDiagrams()
		}
		f.drawn = now
	}
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
	case chat.DiagramCopyImageMsg:
		return f.copyImage(msg), true
	case diagramCopiedMsg:
		if msg.err != nil {
			slog.Warn("Failed to render mermaid diagram for the clipboard", "err", msg.err)
			return tea.Batch(f.listen, util.ReportError(fmt.Errorf("copy diagram image: %w", msg.err))), true
		}
		return tea.Batch(f.listen, f.writeToClipboard(msg.png)), true
	case diagramExportedMsg:
		if msg.err != nil {
			slog.Warn("Failed to export mermaid diagram", "err", msg.err)
			return tea.Batch(f.listen, util.ReportError(fmt.Errorf("open diagram: %w", msg.err))), true
		}
		return tea.Batch(f.listen, f.openInBrowser(msg.path)), true
	case diagramReadyMsg:
		if msg.err != nil {
			slog.Warn("Failed to render mermaid diagram", "err", msg.err)
			f.mu.Lock()
			e := f.entries[msg.key]
			if e != nil {
				e.state, e.err, e.transient = diagramFailed, msg.err.Error(), transientRenderError(msg.err)
			}
			f.mu.Unlock()
			if e != nil {
				f.host.InvalidateDiagrams()
			}
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
		e.cols, e.rows, e.squeezed = msg.cols, msg.rows, msg.squeezed
		e.bytes = msg.cols * msg.cellW * msg.rows * msg.cellH * 4
		cs := fimage.CellSize{Width: msg.cellW, Height: msg.cellH}
		transmit := fimage.EncodingKitty.Transmit(msg.key.id(), msg.img, cs, msg.cols, msg.rows, f.tmux())
		shown := func() tea.Msg { return diagramShownMsg{key: msg.key, cols: msg.cols, rows: msg.rows} }
		return tea.Batch(f.listen, tea.Sequence(transmit, shown)), true
	case diagramShownMsg:
		f.mu.Lock()
		e, ok := f.entries[msg.key]
		if !ok {
			f.mu.Unlock()
			// Forgotten while it transmitted (its drill-in closed or it
			// was evicted), so the transmit put it back: free it again.
			return fimage.EncodingKitty.Forget(msg.key.id(), msg.cols, msg.rows, f.tmux()), true
		}
		e.state = diagramReady
		evict := f.evictLocked(msg.key)
		f.mu.Unlock()
		f.host.InvalidateDiagrams()
		return evict, true
	}
	return nil, false
}

// open shows a diagram in a drill-in, or in the browser when asked to.
func (f *mermaidFeature) open(msg chat.DiagramOpenMsg) tea.Cmd {
	if msg.External {
		theme := f.theme()
		req := diagramRequest{key: diagramKey{hash: f.imageHash(msg.Source, theme["pele-bg"])}, source: msg.Source, theme: theme, job: diagramJobExport}
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

// copyImage queues a render of the diagram for the clipboard.
func (f *mermaidFeature) copyImage(msg chat.DiagramCopyImageMsg) tea.Cmd {
	if f.overSSH() {
		// The clipboard is the remote machine's, and OSC 52, which copies
		// the source over SSH, only carries text.
		return util.ReportWarn("Can't copy a diagram image over SSH; c/y copies its source")
	}
	req := diagramRequest{source: msg.Source, theme: f.theme(), job: diagramJobCopy}
	if !f.enqueue(req) {
		return util.ReportWarn("Diagram renderer is busy; try again")
	}
	return util.ReportInfo("Copying diagram image")
}

func (f *mermaidFeature) tmux() bool {
	_, ok := f.caps.Env.LookupEnv("TMUX")
	return ok
}

func (f *mermaidFeature) overSSH() bool {
	_, conn := f.caps.Env.LookupEnv("SSH_CONNECTION")
	_, tty := f.caps.Env.LookupEnv("SSH_TTY")
	return conn || tty
}

// evictLocked drops transmitted diagrams drawn at a width their kind no
// longer uses, then the least recently drawn until the rest fit
// cacheBudget. keep and diagrams in view are never dropped, even when they
// alone pass the budget: dropping one on screen would show its code block
// until it rendered again, and that would evict the next. It returns the commands that free
// them in the terminal.
func (f *mermaidFeature) evictLocked(keep diagramKey) tea.Cmd {
	var sent []diagramKey
	for key, e := range f.entries {
		switch {
		case e.bytes > 0:
			sent = append(sent, key)
		case e.state == diagramFailed && key.width != f.widths[key.kind]:
			// Nothing to free in the terminal.
			delete(f.entries, key)
		}
	}
	// Oldest first, in a fixed order, so eviction doesn't depend on map
	// order.
	slices.SortFunc(sent, func(a, b diagramKey) int {
		return cmp.Or(cmp.Compare(f.entries[a].lastUsed, f.entries[b].lastUsed), strings.Compare(a.id(), b.id()))
	})
	var held []diagramKey
	total := 0
	var cmds []tea.Cmd
	for _, key := range sent {
		if key.width != f.widths[key.kind] && key != keep {
			cmds = append(cmds, f.forgetLocked(key))
			continue
		}
		held = append(held, key)
		total += f.entries[key].bytes
	}
	for _, key := range held {
		if total > f.cacheBudget && key != keep && !f.inView[key] {
			total -= f.entries[key].bytes
			cmds = append(cmds, f.forgetLocked(key))
		}
	}
	return tea.Batch(cmds...)
}

// forgetKind drops every diagram of kind.
func (f *mermaidFeature) forgetKind(kind diagramKind) tea.Cmd {
	return f.forgetWhere(func(key diagramKey) bool { return key.kind == kind })
}

func (f *mermaidFeature) forgetWhere(match func(diagramKey) bool) tea.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []diagramKey
	for key := range f.entries {
		if match(key) {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b diagramKey) int { return strings.Compare(a.id(), b.id()) })
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
