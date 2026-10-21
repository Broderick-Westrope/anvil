package model

import (
	"cmp"
	"encoding/binary"
	"errors"
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
	producer     producer
	hash         uint64
	width        int
	kind         diagramKind
	cellW, cellH int
	// gen is the drill-in opening a full-size image belongs to, so a
	// result or a delete from an earlier opening never touches it.
	gen uint64
}

func (k diagramKey) id() string {
	return fmt.Sprintf("%s-%x-%d-%d-%dx%d-%d", k.producer, k.hash, k.width, k.kind, k.cellW, k.cellH, k.gen)
}

// producer is what turns a request into an image.
type producer int

const (
	// producerMermaid lays out and rasterizes Mermaid source.
	producerMermaid producer = iota
	// producerBitmap decodes an image a tool returned.
	producerBitmap
)

func (p producer) String() string {
	if p == producerBitmap {
		return "image"
	}
	return "mermaid"
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
	key    diagramKey
	source string
	// data and mimeType are a bitmap's base64 data and type, for
	// producerBitmap.
	data, mimeType string
	theme          mermaid.Theme
	cellW, cellH   int
	maxCols        int
	// export writes the diagram as a page to open in the browser instead
	// of rasterizing it.
	export bool
}

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

// graphicsFeature is the cache of every image Anvil draws with Kitty
// graphics. Producers make the images: Mermaid renders ```mermaid fences
// in finished assistant messages, shows one full size in a drill-in and
// opens one in the browser (see internal/mermaid); bitmaps are the images
// tools return, drawn in their drill-ins (tool_image.go). Only the drill-in
// on top holds a full-size image.
type graphicsFeature struct {
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

	// top is the drill-in on top, the only one whose full-size image may
	// be in the terminal, and gen counts the times it changed. Both are
	// only touched on the UI goroutine.
	top *Chat
	gen uint64

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
	openURL   func(string) error
	listening bool
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

func (f *graphicsFeature) conditions() drawConditions {
	cellW, cellH := f.caps.CellSize()
	return drawConditions{kitty: f.caps.SupportsKittyGraphics(), cellW: cellW, cellH: cellH, bg: f.background()}
}

// usable reports whether diagrams can be drawn at all.
func (c drawConditions) usable() bool { return c.kitty && c.cellW > 0 && c.cellH > 0 }

func newGraphicsFeature(host featureHost, caps *common.Capabilities, com *common.Common) *graphicsFeature {
	return &graphicsFeature{
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
	}
}

// Diagram implements chat.DiagramRenderer.
func (f *graphicsFeature) Diagram(source string, width int) chat.DiagramView {
	return f.lookup(source, width, diagramInline, true)
}

// FullDiagram implements chat.DiagramRenderer, for when no drill-in is
// open; drill-ins ask through ForChat.
func (f *graphicsFeature) FullDiagram(source string, width int) chat.DiagramView {
	return f.fullDiagram(nil, source, width)
}

// fullDiagram is the diagram as owner's drill-in shows it. When the chat's
// image of the diagram wasn't squeezed it is already full size, so the
// drill-in shows it; otherwise it stands in while the full-size image
// renders, which it only does while owner is on top.
func (f *graphicsFeature) fullDiagram(owner *Chat, source string, width int) chat.DiagramView {
	inline, squeezed := f.inlineImage(source, width)
	if inline != "" && !squeezed {
		return chat.DiagramView{State: chat.DiagramReady, Image: inline}
	}
	if _, ok := f.imageKey(source, width, diagramFull); ok && owner != f.top {
		return chat.DiagramView{State: chat.DiagramPending, Image: inline}
	}
	view := f.lookup(source, width, diagramFull, true)
	if view.State == chat.DiagramPending {
		view.Image = inline
	}
	return view
}

// inlineImage is the chat's image of source if it is in the terminal and
// fits width, and whether it was squeezed.
func (f *graphicsFeature) inlineImage(source string, width int) (string, bool) {
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
func (f *graphicsFeature) background() string {
	return cmp.Or(hexColor(f.host.AppBackground()), f.termBg, hexColor(f.com.Styles.Background))
}

// theme is the palette diagrams draw with, from the styles. pele-bg fills
// the boxes behind edge labels, so it has to be the colour around the
// transparent image: Anvil's background, the terminal's own when the TUI
// is transparent, or the theme's until the terminal reports it.
func (f *graphicsFeature) theme() mermaid.Theme {
	f.initPalette()
	theme := maps.Clone(f.palette)
	theme["pele-bg"] = f.background()
	return theme
}

// initPalette builds the background-independent part of the theme once,
// since the styles don't change while Anvil runs.
func (f *graphicsFeature) initPalette() {
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
func (f *graphicsFeature) imageHash(source, bg string) uint64 {
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
func (f *graphicsFeature) InView(sources []string, width int) {
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
func (f *graphicsFeature) imageKey(source string, width int, kind diagramKind) (diagramKey, bool) {
	cellW, cellH, ok := f.drawable(width)
	if !ok {
		return diagramKey{}, false
	}
	return diagramKey{hash: f.imageHash(source, f.background()), width: width, kind: kind, cellW: cellW, cellH: cellH}, true
}

// drawable is the cell size images are drawn at width with, or false when
// they can't be drawn.
func (f *graphicsFeature) drawable(width int) (cellW, cellH int, ok bool) {
	cellW, cellH = f.caps.CellSize()
	return cellW, cellH, f.caps.SupportsKittyGraphics() && cellW > 0 && cellH > 0 && width > 0
}

// lookup finds source's image at width, queueing its render if there is
// none, and marks it drawn for eviction. Only with draw does it build the
// placeholders that show it. It runs during rendering, so it only touches
// the feature's own state and queues work.
func (f *graphicsFeature) lookup(source string, width int, kind diagramKind, draw bool) chat.DiagramView {
	key, ok := f.imageKey(source, width, kind)
	if !ok {
		return chat.DiagramView{State: chat.DiagramUnsupported}
	}
	req := diagramRequest{key: key, source: source, theme: f.theme(), cellW: key.cellW, cellH: key.cellH, maxCols: width}
	return f.lookupRequest(req, draw)
}

// lookupRequest is lookup for the image req asks for. A full-size image
// gets the current generation, which moves on when the drill-in's width
// does: the image at a width it returns to must not share its terminal ID
// with the one evicted when it left, whose delete may still be on its way.
func (f *graphicsFeature) lookupRequest(req diagramRequest, draw bool) chat.DiagramView {
	kind, width := req.key.kind, req.maxCols
	f.mu.Lock()
	defer f.mu.Unlock()
	if kind == diagramFull {
		if f.widths[kind] != width {
			f.gen++
		}
		req.key.gen = f.gen
	}
	key := req.key
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
	if f.enqueue(req) {
		f.entries[key] = &diagramEntry{state: diagramPending, lastUsed: f.clock}
	}
	return chat.DiagramView{State: chat.DiagramPending}
}

// enqueue hands req to the worker, starting it if needed, and reports false
// when the queue is full.
func (f *graphicsFeature) enqueue(req diagramRequest) bool {
	select {
	case f.requests <- req:
		f.startOnce.Do(func() { go f.work() })
		return true
	default:
		return false
	}
}

func (f *graphicsFeature) listen() tea.Msg { return <-f.results }

// Observe starts listening for rendered diagrams, asks the terminal for its
// background, re-renders the chat when Kitty graphics support is first
// detected, and frees the full-size image once its drill-in closes.
func (f *graphicsFeature) Observe(msg tea.Msg) tea.Cmd {
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
			// A full-size image asked for again at once must not share
			// its terminal ID with the one whose delete is on its way.
			f.gen++
		}
		if f.drawn.usable() || now.usable() {
			// Placeholders for forgotten images, or code blocks that
			// can now be images, are cached in the chat.
			f.host.InvalidateDiagrams()
		}
		f.drawn = now
	}
	if !f.listening {
		f.listening = true
		cmds = append(cmds, f.listen, tea.RequestBackgroundColor)
	}
	return tea.Batch(cmds...)
}

func (f *graphicsFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case chat.DiagramOpenMsg:
		return f.open(msg), true
	case chat.ImageOpenMsg:
		return f.openImage(msg), true
	case diagramExportedMsg:
		if msg.err != nil {
			slog.Warn("Failed to export mermaid diagram", "err", msg.err)
			return tea.Batch(f.listen, util.ReportError(fmt.Errorf("open diagram: %w", msg.err))), true
		}
		return tea.Batch(f.listen, f.openInBrowser(msg.path)), true
	case diagramReadyMsg:
		if msg.err != nil {
			logRenderFailure(msg.key, msg.err)
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

// logRenderFailure logs why an image didn't render: a warning, unless the
// image is one Anvil doesn't try to show.
func logRenderFailure(key diagramKey, err error) {
	if key.producer == producerBitmap {
		if errors.As(err, new(undisplayableError)) {
			slog.Debug("Tool image not displayable", "err", err)
			return
		}
		slog.Warn("Failed to decode tool image", "err", err)
		return
	}
	slog.Warn("Failed to render mermaid diagram", "err", err)
}

// open shows a diagram in a drill-in, or in the browser when asked to.
func (f *graphicsFeature) open(msg chat.DiagramOpenMsg) tea.Cmd {
	if msg.External {
		theme := f.theme()
		req := diagramRequest{key: diagramKey{hash: f.imageHash(msg.Source, theme["pele-bg"])}, source: msg.Source, theme: theme, export: true}
		if !f.enqueue(req) {
			return util.ReportWarn("Diagram renderer is busy; try again")
		}
		return util.ReportInfo("Opening diagram in browser")
	}
	c := NewChat(f.com)
	c.SetMessages(chat.NewDiagramDetailItem(f.com.Styles, msg.Source, f.ForChat(c)))
	c.SelectLast()
	f.host.PushDrillIn(c, "Diagram")
	return nil
}

// DrillInChanged implements drillInFeature. Only the drill-in on top may
// hold a full-size image, so the last one's is freed, and the chats
// re-render so the new top asks for its own.
func (f *graphicsFeature) DrillInChanged(top *Chat) tea.Cmd {
	if top == f.top {
		return nil
	}
	f.top = top
	f.gen++
	cmd := f.forgetKind(diagramFull)
	f.host.InvalidateDiagrams()
	return cmd
}

// ForChat is the renderer for the items of c, a drill-in: they get
// full-size images only while c is on top.
func (f *graphicsFeature) ForChat(c *Chat) chat.DiagramRenderer { return ownedGraphics{f: f, owner: c} }

// ImagesFor is ForChat for tool images, or nil when f is.
func (f *graphicsFeature) ImagesFor(c *Chat) chat.ImageRenderer {
	if f == nil {
		return nil
	}
	return ownedGraphics{f: f, owner: c}
}

// ownedGraphics is the feature as one drill-in's items see it.
type ownedGraphics struct {
	f     *graphicsFeature
	owner *Chat
}

func (o ownedGraphics) Diagram(source string, width int) chat.DiagramView {
	return o.f.Diagram(source, width)
}

func (o ownedGraphics) FullDiagram(source string, width int) chat.DiagramView {
	return o.f.fullDiagram(o.owner, source, width)
}

func (o ownedGraphics) InView(sources []string, width int) { o.f.InView(sources, width) }

func (o ownedGraphics) FullImage(data, mimeType string, width int) chat.DiagramView {
	return o.f.fullImage(o.owner, data, mimeType, width)
}

func (f *graphicsFeature) tmux() bool {
	_, ok := f.caps.Env.LookupEnv("TMUX")
	return ok
}

// evictLocked drops transmitted diagrams drawn at a width their kind no
// longer uses, then the least recently drawn until the rest fit
// cacheBudget. keep and diagrams in view are never dropped, even when they
// alone pass the budget: dropping one on screen would show its code block
// until it rendered again, and that would evict the next. It returns the commands that free
// them in the terminal.
func (f *graphicsFeature) evictLocked(keep diagramKey) tea.Cmd {
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
func (f *graphicsFeature) forgetKind(kind diagramKind) tea.Cmd {
	return f.forgetWhere(func(key diagramKey) bool { return key.kind == kind })
}

func (f *graphicsFeature) forgetWhere(match func(diagramKey) bool) tea.Cmd {
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

func (f *graphicsFeature) forgetLocked(key diagramKey) tea.Cmd {
	e := f.entries[key]
	delete(f.entries, key)
	if e.bytes == 0 {
		return nil
	}
	return fimage.EncodingKitty.Forget(key.id(), e.cols, e.rows, f.tmux())
}

func (f *graphicsFeature) HandleKey(tea.KeyPressMsg) (tea.Cmd, bool)  { return nil, false }
func (f *graphicsFeature) HandleAction(dialog.Action) (tea.Cmd, bool) { return nil, false }
