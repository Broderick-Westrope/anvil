package model

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/require"
)

// fakeDiagramHost is the featureHost surface diagrams use; the rest is
// unimplemented.
type fakeDiagramHost struct {
	featureHost
	invalidations int
	bg            color.Color
	drills        []*Chat
	labels        []string
}

func (h *fakeDiagramHost) InvalidateDiagrams()        { h.invalidations++ }
func (h *fakeDiagramHost) AppBackground() color.Color { return h.bg }
func (h *fakeDiagramHost) PushDrillIn(c *Chat, label string) {
	h.drills = append(h.drills, c)
	h.labels = append(h.labels, label)
}

func (h *fakeDiagramHost) DrillInOpen(c *Chat) bool { return slices.Contains(h.drills, c) }

// engineSlots caps how many tests run render engines at once. Each worker
// loads its own resvg, and under the race detector a few dozen at
// once starve each other past their deadlines.
var engineSlots = make(chan struct{}, 4)

func newTestMermaidFeature(t *testing.T, host *fakeDiagramHost, caps *common.Capabilities) *mermaidFeature {
	t.Helper()
	engineSlots <- struct{}{}
	t.Cleanup(func() { <-engineSlots })
	st := uistyles.TokyoNight()
	f := newMermaidFeature(host, caps, &common.Common{Styles: &st})
	f.exportDir = t.TempDir()
	f.compileCacheDir = t.TempDir()
	// Free the engines soon after a test stops using them.
	f.idleTimeout = 200 * time.Millisecond
	// Tests read results straight off the channel, so Observe mustn't
	// hand back the blocking listen command.
	f.listening = true
	return f
}

func kittyCaps() *common.Capabilities {
	return &common.Capabilities{KittyGraphics: true, Columns: 100, Rows: 40, PixelX: 1000, PixelY: 800}
}

func waitResult(t *testing.T, f *mermaidFeature) tea.Msg {
	t.Helper()
	select {
	case msg := <-f.results:
		return msg
	case <-time.After(30 * time.Second):
		t.Fatal("the worker never answered")
		return nil
	}
}

func waitReady(t *testing.T, f *mermaidFeature) diagramReadyMsg {
	t.Helper()
	ready, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)
	require.NoError(t, ready.err)
	return ready
}

// showDiagram drives one diagram from request to shown, doing by hand what
// the transmit command in HandleMsg's sequence does. It returns the eviction
// command the shown message produced.
func showDiagram(t *testing.T, f *mermaidFeature, src string, width int) (diagramReadyMsg, tea.Cmd) {
	t.Helper()
	require.Equal(t, chat.DiagramPending, f.Diagram(src, width).State, "first request only queues the render")
	ready := waitReady(t, f)
	_, handled := f.HandleMsg(ready)
	require.True(t, handled)

	transmit(ready)

	evict, handled := f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
	require.True(t, handled)
	return ready, evict
}

// transmit does what the transmit command in HandleMsg's sequence does. The
// image cache is global, so a repeat run (-count) may find it already
// sent, and then there is nothing to do.
func transmit(ready diagramReadyMsg) {
	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	if cmd := fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false); cmd != nil {
		cmd()
	}
}

// rawMsgs runs cmd, a batch or a single command, and returns the raw
// terminal output it produces.
func rawMsgs(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	var out []string
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			out = append(out, rawMsgs(c)...)
		}
	case tea.RawMsg:
		out = append(out, fmt.Sprint(msg.Msg))
	}
	return out
}

func TestMermaidFeatureRendersKittyPlaceholders(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	src := "flowchart LR\n  A[Prompt] --> B[Pele] --> C[Kitty]\n"

	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State, "first request only queues the render")
	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State, "a pending render isn't queued twice")

	ready := waitReady(t, f)
	require.LessOrEqual(t, ready.cols, 80)
	require.LessOrEqual(t, ready.rows, diagramMaxRows)
	require.Equal(t, ready.cols*10, ready.img.Bounds().Dx(), "padded to whole cells")
	require.Equal(t, ready.rows*20, ready.img.Bounds().Dy(), "padded to whole cells")

	_, handled := f.HandleMsg(ready)
	require.True(t, handled)
	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State, "not shown until the image is transmitted")

	transmit(ready)
	_, handled = f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
	require.True(t, handled)
	require.Equal(t, 1, host.invalidations)

	view := f.Diagram(src, 80)
	require.Equal(t, chat.DiagramReady, view.State)
	require.Contains(t, view.Image, string(kitty.Placeholder))
}

func TestMermaidFeatureWithoutKittyKeepsCodeBlock(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, &common.Capabilities{PixelX: 1000, PixelY: 800, Columns: 100, Rows: 40})
	require.Equal(t, chat.DiagramUnsupported, f.Diagram("flowchart LR\n A --> B\n", 80).State)
	require.Equal(t, chat.DiagramUnsupported, f.FullDiagram("flowchart LR\n A --> B\n", 80).State)
	require.Empty(t, f.entries, "nothing is queued")
}

func TestMermaidFeatureReportsFailures(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	const src = "notADiagram at all\n"
	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State)
	failed, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)
	require.ErrorContains(t, failed.err, "No diagram type detected")

	_, handled := f.HandleMsg(failed)
	require.True(t, handled)
	require.Equal(t, 1, host.invalidations, "the chat shows the failure at once")
	view := f.Diagram(src, 80)
	require.Equal(t, chat.DiagramFailed, view.State)
	require.Equal(t, failed.err.Error(), view.Err)
	require.Equal(t, chat.DiagramPending, f.FullDiagram(src, 80).State, "each kind renders on its own")
}

func TestMermaidFeatureReleasesEnginesWhenIdle(t *testing.T) {
	t.Parallel()

	st := uistyles.TokyoNight()
	require.Equal(t, 20*time.Second, newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &st}).idleTimeout)

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	f.idleTimeout = 50 * time.Millisecond
	var released atomic.Int32
	f.returnMemory = func() { released.Add(1) }
	time.Sleep(100 * time.Millisecond)
	require.Zero(t, released.Load(), "nothing loads, so nothing is released, before the first diagram")

	f.Diagram("flowchart LR\n A --> B\n", 80)
	waitReady(t, f)
	require.Eventually(t, func() bool { return released.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"idle engines are dropped and their memory handed back to the OS")

	f.Diagram("flowchart LR\n C --> D\n", 80)
	waitReady(t, f)
	require.Eventually(t, func() bool { return released.Load() == 2 }, 5*time.Second, 10*time.Millisecond,
		"the engines load again on demand, and are dropped again")
}

func TestMermaidFeatureEvictsOtherWidthsOnResize(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	src := "flowchart LR\n  W[Wide] --> N[Narrow]\n"
	wide, evict := showDiagram(t, f, src, 80)
	require.Empty(t, rawMsgs(evict))

	narrow, evict := showDiagram(t, f, src, 40)
	deletes := rawMsgs(evict)
	require.Len(t, deletes, 1, "the 80-column copy is dropped")
	require.Contains(t, deletes[0], "a=d,d=I,")
	require.NotContains(t, f.entries, wide.key)
	require.False(t, fimage.HasTransmitted(wide.key.id(), wide.cols, wide.rows))
	require.True(t, fimage.HasTransmitted(narrow.key.id(), narrow.cols, narrow.rows))
}

func TestMermaidFeatureEvictsLeastRecentlyDrawnOverBudget(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	srcs := []string{
		"flowchart LR\n  A1 --> B1\n",
		"flowchart LR\n  A2 --> B2\n",
		"flowchart LR\n  A3 --> B3\n",
	}
	first, _ := showDiagram(t, f, srcs[0], 80)
	second, _ := showDiagram(t, f, srcs[1], 80)
	// Room for two: drawing the first again makes the second the oldest.
	f.cacheBudget = f.entries[first.key].bytes + f.entries[second.key].bytes
	require.Equal(t, chat.DiagramReady, f.Diagram(srcs[0], 80).State)

	third, evict := showDiagram(t, f, srcs[2], 80)
	require.Len(t, rawMsgs(evict), 1)
	require.Contains(t, f.entries, first.key)
	require.NotContains(t, f.entries, second.key)
	require.Contains(t, f.entries, third.key)

	view := f.Diagram(srcs[1], 80)
	require.Equal(t, chat.DiagramPending, view.State, "an evicted diagram renders again")
	require.Empty(t, view.Image)
	require.True(t, strings.HasPrefix(waitReady(t, f).key.id(), "mermaid-"))
}

func TestMermaidFeatureThemeUsesTheBackgroundBehindTheImage(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{bg: color.RGBA{R: 0x0c, G: 0x0e, B: 0x14, A: 0xff}}
	f := newTestMermaidFeature(t, host, kittyCaps())
	require.Equal(t, "#0c0e14", f.theme()["pele-bg"], "Anvil's own background")

	host.bg = nil
	require.Equal(t, "#0c0e14", f.theme()["pele-bg"], "transparent TUI before the terminal answers: the theme's background")
	f.Observe(tea.BackgroundColorMsg{Color: color.RGBA{R: 0x05, G: 0x00, B: 0x14, A: 0xff}})
	require.Equal(t, "#050014", f.theme()["pele-bg"], "the terminal's background")

	src := "flowchart LR\n A --> B\n"
	require.NotEqual(t, f.imageHash(src, "#0c0e14"), f.imageHash(src, f.background()),
		"a new background renders the diagram again")
}

func TestMermaidFeatureRendersTransparentImages(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	f.Diagram("flowchart LR\n A --> B\n", 80)
	ready := waitReady(t, f)
	_, _, _, a := ready.img.At(0, 0).RGBA()
	require.Zero(t, a, "the terminal shows through")
}

func TestMermaidFeatureOpensDrillIn(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	src := "flowchart TB\n"
	for i := range 30 {
		src += fmt.Sprintf("  n%d[Step %d] --> n%d[Step %d]\n", i, i, i+1, i+1)
	}

	cmd, handled := f.HandleMsg(chat.DiagramOpenMsg{Source: src})
	require.True(t, handled)
	require.Nil(t, cmd)
	require.Equal(t, []string{"Diagram"}, host.labels)
	item, ok := host.drills[0].MessageItems()[0].(*chat.DiagramDetailItem)
	require.True(t, ok)

	out := item.RawRender(80)
	require.Contains(t, out, "Rendering diagram")
	full := waitReady(t, f)
	require.Equal(t, diagramFull, full.key.kind)
	require.Greater(t, full.rows, diagramMaxRows, "the drill-in isn't capped at the chat's height")
	require.LessOrEqual(t, full.rows, diagramMaxFullRows)

	_, handled = f.HandleMsg(full)
	require.True(t, handled)
	transmit(full)
	f.HandleMsg(diagramShownMsg{key: full.key, cols: full.cols, rows: full.rows})
	require.Contains(t, item.RawRender(80), string(kitty.Placeholder))

	// Closing the drill-in frees the full-size image.
	host.drills = nil
	deletes := rawMsgs(f.Observe(nil))
	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, full.key)
	require.Nil(t, f.drill)
}

func TestMermaidFeatureWithoutKittyOpensTheCodeBlock(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, &common.Capabilities{})
	cmd, handled := f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n"})
	require.True(t, handled)
	require.Nil(t, cmd, "nothing opens the browser")
	require.Len(t, host.drills, 1)
	item, ok := host.drills[0].MessageItems()[0].(*chat.DiagramDetailItem)
	require.True(t, ok)
	out := ansi.Strip(item.RawRender(80))
	require.Contains(t, out, "A --> B")
	require.NotContains(t, out, "Rendering diagram")
	select {
	case msg := <-f.results:
		t.Fatalf("the worker was asked for something: %T", msg)
	default:
	}
}

func TestMermaidFeatureExportsForTheBrowser(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		caps *common.Capabilities
	}{
		"with kitty":           {caps: kittyCaps()},
		"terminal lacks kitty": {caps: &common.Capabilities{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			host := &fakeDiagramHost{}
			f := newTestMermaidFeature(t, host, tc.caps)
			cmd, handled := f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n", External: true})
			require.True(t, handled)
			require.NotNil(t, cmd)
			require.Empty(t, host.drills)

			exported, ok := waitResult(t, f).(diagramExportedMsg)
			require.True(t, ok)
			require.NoError(t, exported.err)
			doc, err := os.ReadFile(exported.path)
			require.NoError(t, err)
			require.Contains(t, string(doc), "<svg")
			require.Empty(t, f.entries, "exporting caches nothing")
		})
	}
}

func TestMermaidFeatureThemeIsTheTokyoNightPalette(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{bg: color.RGBA{R: 0x0c, G: 0x0e, B: 0x14, A: 0xff}}
	f := newTestMermaidFeature(t, host, kittyCaps())
	require.Equal(t, mermaid.Theme{
		"pele-bg":           "#0c0e14",
		"pele-fg":           "#c0caf5",
		"pele-muted":        "#565f89",
		"pele-line":         "#a9b1d6",
		"pele-surface":      "#292e42",
		"pele-surface-alt":  "#1a1b26",
		"pele-border":       "#bb9af7",
		"pele-accent":       "#7aa2f7",
		"pele-critical":     "#f7768e",
		"pele-error":        "#db4b4b",
		"pele-series-1":     "#7aa2f7",
		"pele-series-2":     "#ff9e64",
		"pele-series-3":     "#9ece6a",
		"pele-series-4":     "#f7768e",
		"pele-series-5":     "#73daca",
		"pele-series-6":     "#e0af68",
		"pele-series-7":     "#bb9af7",
		"pele-series-8":     "#2ac3de",
		"pele-font":         "sans-serif",
		"pele-font-mono":    "monospace",
		"pele-title-weight": "bold",
	}, f.theme())
}

func TestMermaidFeatureThemeFollowsTheStyles(t *testing.T) {
	t.Parallel()

	st := uistyles.TokyoNight()
	st.Diagram.Accent = color.RGBA{R: 1, G: 2, B: 3, A: 0xff}
	st.Diagram.Series[7] = color.RGBA{R: 4, G: 5, B: 6, A: 0xff}
	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &st})
	theme := f.theme()
	require.Equal(t, "#010203", theme["pele-accent"])
	require.Equal(t, "#040506", theme["pele-series-8"])
}

func TestMermaidFeatureFreesAnImageForgottenWhileItTransmits(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n"})
	item := host.drills[0].MessageItems()[0].(*chat.DiagramDetailItem)
	item.RawRender(80)
	full := waitReady(t, f)
	_, handled := f.HandleMsg(full)
	require.True(t, handled)

	// The drill-in closes before the transmit command runs.
	host.drills = nil
	f.Observe(nil)
	transmit(full)
	require.NotEmpty(t, fimage.EncodingKitty.Render(full.key.id(), full.cols, full.rows), "the late transmit cached it")

	invalidations := host.invalidations
	cmd, handled := f.HandleMsg(diagramShownMsg{key: full.key, cols: full.cols, rows: full.rows})
	require.True(t, handled)
	require.Len(t, rawMsgs(cmd), 1, "the terminal is told to delete it")
	require.Empty(t, fimage.EncodingKitty.Render(full.key.id(), full.cols, full.rows), "and the cache drops it")
	require.Equal(t, invalidations, host.invalidations, "nothing new to show")
}

func TestMermaidFeatureRedrawsWhenTheCellSizeArrivesAfterKitty(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	caps := &common.Capabilities{KittyGraphics: true}
	f := newTestMermaidFeature(t, host, caps)
	f.Observe(nil)
	require.Equal(t, chat.DiagramUnsupported, f.Diagram("flowchart LR\n A --> B\n", 80).State, "no cell size yet")
	before := host.invalidations

	caps.Columns, caps.Rows, caps.PixelX, caps.PixelY = 100, 40, 1000, 800
	f.Observe(nil)
	require.Greater(t, host.invalidations, before, "the code blocks the chat cached are redrawn")
}

func TestMermaidFeatureDropsImagesWhenTheCellSizeChanges(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	caps := kittyCaps()
	f := newTestMermaidFeature(t, host, caps)
	f.Observe(nil)
	showDiagram(t, f, "flowchart LR\n A --> B\n", 80)
	before := host.invalidations

	caps.PixelX = 1200 // A bigger font: same columns, wider cells.
	deletes := rawMsgs(f.Observe(nil))
	require.Len(t, deletes, 1, "the old-size image is deleted")
	require.Empty(t, f.entries)
	require.Greater(t, host.invalidations, before)
	require.Equal(t, chat.DiagramPending, f.Diagram("flowchart LR\n A --> B\n", 80).State, "it renders again at the new size")
}

func TestMermaidFeatureIgnoresAResultForTheOldCellSize(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	caps := kittyCaps()
	f := newTestMermaidFeature(t, host, caps)
	f.Observe(nil)
	const src = "flowchart LR\n A --> B\n"
	f.Diagram(src, 80)
	stale := waitReady(t, f)

	caps.PixelX = 1200
	f.Observe(nil)
	f.Diagram(src, 80)
	_, handled := f.HandleMsg(stale)
	require.True(t, handled)
	require.NotContains(t, f.entries, stale.key, "nothing is kept for the old cells")
	fresh := waitReady(t, f)
	require.Equal(t, 12, fresh.cellW)
	require.NotEqual(t, stale.key, fresh.key)
	require.Equal(t, diagramPending, f.entries[fresh.key].state)
}

func TestMermaidFeatureRedrawsWhenTheBackgroundChanges(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{bg: color.RGBA{R: 0x0c, G: 0x0e, B: 0x14, A: 0xff}}
	f := newTestMermaidFeature(t, host, kittyCaps())
	f.Observe(nil)
	before := host.invalidations
	f.Observe(nil)
	require.Equal(t, before, host.invalidations, "nothing changed")

	host.bg = nil // Transparency toggled on: the terminal's own background shows.
	f.Observe(tea.BackgroundColorMsg{Color: color.RGBA{R: 0x05, G: 0x00, B: 0x14, A: 0xff}})
	require.Equal(t, before+1, host.invalidations, "diagrams redraw against the new background")
}

func tallFlowchart(steps int) string {
	src := "flowchart TB\n"
	for i := range steps {
		src += fmt.Sprintf("  n%d[Step %d] --> n%d[Step %d]\n", i, i, i+1, i+1)
	}
	return src
}

func TestMermaidFeatureDrillInReusesAnUnsqueezedInlineImage(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	// Sources are unique per test: the terminal image cache is shared.
	const src = "flowchart LR\n Reused --> Inline\n"
	inline, _ := showDiagram(t, f, src, 80)
	require.False(t, inline.squeezed)

	view := f.FullDiagram(src, 100)
	require.Equal(t, chat.DiagramReady, view.State, "no second render")
	require.Equal(t, f.Diagram(src, 80).Image, view.Image, "the same image")
	select {
	case msg := <-f.results:
		t.Fatalf("the worker was asked for something: %T", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMermaidFeatureDrillInShowsTheSqueezedInlineImageWhileItRenders(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	src := tallFlowchart(31)
	inline, _ := showDiagram(t, f, src, 80)
	require.True(t, inline.squeezed, "capped at the chat's height")

	view := f.FullDiagram(src, 100)
	require.Equal(t, chat.DiagramPending, view.State, "the full size renders")
	require.Equal(t, f.Diagram(src, 80).Image, view.Image, "with the inline image standing in")
	require.Equal(t, diagramFull, waitReady(t, f).key.kind)
}

func TestMermaidFeatureDrillInWithoutAnInlineImageRenders(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	view := f.FullDiagram("flowchart LR\n A --> B\n", 100)
	require.Equal(t, chat.DiagramPending, view.State)
	require.Empty(t, view.Image)
	require.Equal(t, diagramFull, waitReady(t, f).key.kind)
}

func TestMermaidFeatureImageHashCoversThePalette(t *testing.T) {
	t.Parallel()

	st := uistyles.TokyoNight()
	plain := newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &st})
	other := uistyles.TokyoNight()
	other.Diagram.Series[7] = color.RGBA{R: 1, A: 0xff}
	changed := newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &other})
	require.NotEqual(t, plain.imageHash("A", "#000000"), changed.imageHash("A", "#000000"))
	require.Equal(t, plain.imageHash("A", "#000000"), plain.imageHash("A", "#000000"))
}

func TestMermaidFeatureDropsAFailureForAForgottenDiagram(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	caps := kittyCaps()
	f := newTestMermaidFeature(t, host, caps)
	f.Observe(nil)
	f.Diagram("notADiagram at all\n", 80)
	failed, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)

	caps.PixelX = 1200 // Forgets every entry.
	f.Observe(nil)
	f.HandleMsg(failed)
	require.NotContains(t, f.entries, failed.key, "nothing is kept for the old cells")
}

func TestMermaidFeatureExportPrunesOldPages(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	old := filepath.Join(f.exportDir, "old.html")
	recent := filepath.Join(f.exportDir, "recent.html")
	for _, p := range []string{old, recent} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	stale := time.Now().Add(-2 * diagramExportMaxAge)
	require.NoError(t, os.Chtimes(old, stale, stale))

	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n Prune --> Old\n", External: true})
	exported, ok := waitResult(t, f).(diagramExportedMsg)
	require.True(t, ok)
	require.NoError(t, exported.err)
	require.NoFileExists(t, old)
	require.FileExists(t, recent)
	require.FileExists(t, exported.path)
}

func TestMermaidFeatureExportsToTheUsersCacheDir(t *testing.T) {
	t.Parallel()

	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	st := uistyles.TokyoNight()
	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &st})
	require.Equal(t, filepath.Join(cache, "anvil", "diagrams"), f.exportDir)
}

func TestMermaidFeatureCachesCompiledCodeInTheUsersCacheDir(t *testing.T) {
	t.Parallel()

	cache, err := os.UserCacheDir()
	require.NoError(t, err)
	st := uistyles.TokyoNight()
	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps(), &common.Common{Styles: &st})
	require.Equal(t, filepath.Join(cache, "anvil", "wasm"), f.compileCacheDir)
}

func TestMermaidFeatureRendersThroughTheCompileCache(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	showDiagram(t, f, "flowchart LR\n  A --> B", 60)
	entries, err := filepath.Glob(filepath.Join(f.compileCacheDir, "wazero-*", "*"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the rasterizer wrote its compiled code to the cache")
}

// TestFileURL tests the URL directly, since the path that uses it opens the
// user's browser.
func TestFileURL(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ path, want string }{
		"unix":    {path: "/home/me/a b.html", want: "file:///home/me/a%20b.html"},
		"windows": {path: `C:\Users\me\a.html`, want: "file:///C:/Users/me/a.html"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, fileURL(tc.path))
		})
	}
}

func TestMermaidFeatureRedrawsWhenDiagramsBecomeUndrawable(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	caps := kittyCaps()
	f := newTestMermaidFeature(t, host, caps)
	f.Observe(nil)
	before := host.invalidations

	caps.KittyGraphics = false
	f.Observe(nil)
	require.Equal(t, before+1, host.invalidations, "the images are gone, so their placeholders must go too")
}

func TestMermaidRequestPanicBecomesAFailure(t *testing.T) {
	t.Parallel()

	// Engines with no Pele make any render panic, as a renderer bug would.
	broken := &diagramEngines{}
	tests := map[string]diagramRequest{
		"image":  {key: diagramKey{hash: 1}, source: "A"},
		"export": {key: diagramKey{hash: 2}, source: "A", job: diagramJobExport},
		"copy":   {key: diagramKey{hash: 3}, source: "A", job: diagramJobCopy},
	}
	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
			msg, ok := f.runRequest(broken, req)
			require.False(t, ok, "the engines are dropped")
			switch msg := msg.(type) {
			case diagramReadyMsg:
				require.Equal(t, diagramJobRender, req.job)
				require.Equal(t, req.key, msg.key)
				require.ErrorContains(t, msg.err, "diagram renderer crashed")
			case diagramExportedMsg:
				require.Equal(t, diagramJobExport, req.job)
				require.ErrorContains(t, msg.err, "diagram renderer crashed")
			case diagramCopiedMsg:
				require.Equal(t, diagramJobCopy, req.job)
				require.ErrorContains(t, msg.err, "diagram renderer crashed")
			default:
				t.Fatalf("unexpected %T", msg)
			}
		})
	}
}

func TestMermaidFeatureInViewQueuesAndKeepsDiagrams(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	const src = "flowchart LR\n Prefetched --> Early\n"
	f.InView([]string{src}, 80)
	ready := waitReady(t, f)
	require.Equal(t, diagramInline, ready.key.kind, "the chat's image is queued")
	f.HandleMsg(ready)
	transmit(ready)
	f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})

	used := f.entries[ready.key].lastUsed
	f.InView([]string{src}, 80)
	require.Greater(t, f.entries[ready.key].lastUsed, used, "a diagram in view counts as drawn for eviction")
	select {
	case msg := <-f.results:
		t.Fatalf("a ready diagram was queued again: %T", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

// On a HiDPI screen two or three diagrams can fill the budget. Evicting one
// that's on screen made it fall back to its code block, render again and
// evict the next, so diagrams flickered while scrolling.
func TestMermaidFeatureNeverEvictsDiagramsInView(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	srcA, srcB := "flowchart LR\n InViewA --> X\n", "flowchart LR\n InViewB --> Y\n"
	a, _ := showDiagram(t, f, srcA, 80)
	b, _ := showDiagram(t, f, srcB, 80)
	f.cacheBudget = f.entries[a.key].bytes // Room for one.
	f.InView([]string{srcA, srcB}, 80)

	c, evict := showDiagram(t, f, "flowchart LR\n Elsewhere --> Z\n", 80)
	require.Empty(t, rawMsgs(evict), "nothing in view is deleted, though over budget")
	require.Contains(t, f.entries, a.key)
	require.Contains(t, f.entries, b.key)
	require.Contains(t, f.entries, c.key)

	f.InView(nil, 80)
	_, evict = showDiagram(t, f, "flowchart LR\n Later --> W\n", 80)
	require.Len(t, rawMsgs(evict), 3, "once out of view, the oldest go")
	require.NotContains(t, f.entries, a.key)
}

func TestMermaidFeatureRetriesATransientFailureOnce(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	const src = "flowchart LR\n Slow --> Retry\n"
	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State)
	first := waitReady(t, f)

	f.HandleMsg(diagramReadyMsg{key: first.key, err: mermaid.ErrRenderTimeout})
	require.Equal(t, chat.DiagramPending, f.Diagram(src, 80).State, "a timeout is tried again")
	waitReady(t, f)

	f.HandleMsg(diagramReadyMsg{key: first.key, err: mermaid.ErrRenderTimeout})
	require.Equal(t, chat.DiagramFailed, f.Diagram(src, 80).State, "but only once")
}

func TestMermaidFeatureDropsFailuresAtOldWidths(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	const bad = "notADiagram, at an old width\n"
	f.Diagram(bad, 40)
	failed, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)
	f.HandleMsg(failed)
	require.Contains(t, f.entries, failed.key)

	showDiagram(t, f, "flowchart LR\n New --> Width\n", 80)
	require.NotContains(t, f.entries, failed.key, "a failure for a width no longer drawn is dropped")
}

// newIdleMermaidFeature is a mermaid feature for tests that never queue a
// render, so it takes no engine slot. It never opens a browser.
func newIdleMermaidFeature(host *fakeDiagramHost, caps *common.Capabilities) *mermaidFeature {
	st := uistyles.TokyoNight()
	f := newMermaidFeature(host, caps, &common.Common{Styles: &st})
	f.listening = true
	f.openURL = func(string) error { return errors.New("no browser in tests") }
	return f
}

func TestMermaidDiagramLimits(t *testing.T) {
	t.Parallel()

	require.Equal(t, 16_777_216, diagramFullMaxPixels)
	require.Equal(t, 134_217_728, diagramCacheBudget)
	require.Equal(t, 20*time.Second, diagramIdleTimeout)
	require.Equal(t, 24*time.Hour, diagramExportMaxAge)
}

func TestDrawConditionsUsable(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		c    drawConditions
		want bool
	}{
		"kitty with a cell size": {c: drawConditions{kitty: true, cellW: 10, cellH: 20}, want: true},
		"no kitty":               {c: drawConditions{cellW: 10, cellH: 20}},
		"no cell width":          {c: drawConditions{kitty: true, cellH: 20}},
		"no cell height":         {c: drawConditions{kitty: true, cellW: 10}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.c.usable())
		})
	}
}

func TestDiagramFitLaysOutWithinTheChatAtTheTextScale(t *testing.T) {
	t.Parallel()

	// 40px cells draw Pele's 16px text at twice its size.
	fit := newDiagramFit(diagramRequest{cellW: 10, cellH: 40, maxCols: 80})
	require.Equal(t, 400, fit.maxWidth, "800px of chat at scale 2")
}

func TestDiagramFitScale(t *testing.T) {
	t.Parallel()

	inline := diagramRequest{cellW: 10, cellH: 40, maxCols: 80}
	full := inline
	full.key.kind = diagramFull
	// Big cells, so the pixel cap binds before the width and height do.
	bigCells := diagramRequest{key: diagramKey{kind: diagramFull}, cellW: 100, cellH: 400, maxCols: 80}

	tests := map[string]struct {
		req          diagramRequest
		w, h         float64
		scale        float64
		squeezed     bool
		wantEmptyErr bool
	}{
		"small, at the text scale":    {req: inline, w: 100, h: 100, scale: 2},
		"filling the layout width":    {req: inline, w: 399, h: 100, scale: 2, squeezed: true},
		"wider than the chat":         {req: inline, w: 1600, h: 100, scale: 0.5, squeezed: true},
		"taller than the chat allows": {req: inline, w: 100, h: 3200, scale: 0.5, squeezed: true},
		"tall in the drill-in":        {req: full, w: 100, h: 3200, scale: 2},
		"over the pixel cap":          {req: bigCells, w: 400, h: 400, scale: math.Sqrt(float64(diagramFullMaxPixels) / (400 * 400)), squeezed: true},
		"no width":                    {req: inline, w: 0, h: 100, wantEmptyErr: true},
		"no height":                   {req: inline, w: 100, h: 0, wantEmptyErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scale, squeezed, err := newDiagramFit(tc.req).scale(tc.w, tc.h)
			if tc.wantEmptyErr {
				require.EqualError(t, err, "empty diagram")
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tc.scale, scale, 1e-9)
			require.Equal(t, tc.squeezed, squeezed)
		})
	}
}

// heldDiagram puts a transmitted diagram of n bytes in f, last drawn at
// clock lastUsed.
func heldDiagram(f *mermaidFeature, hash uint64, width int, lastUsed uint64, n int) diagramKey {
	key := diagramKey{hash: hash, width: width, kind: diagramInline, cellW: 10, cellH: 20}
	f.entries[key] = &diagramEntry{state: diagramReady, cols: 1, rows: 1, bytes: n, lastUsed: lastUsed}
	return key
}

func TestMermaidFeatureEvictLocked(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		budget int
		// keepIdx is the index into the three diagrams, oldest first, that
		// was just shown.
		keepIdx int
		// oldWidth draws the oldest at a width no longer used.
		oldWidth bool
		want     []bool
	}{
		"all fit exactly":                 {budget: 30, keepIdx: 2, want: []bool{true, true, true}},
		"one over: the oldest goes":       {budget: 20, keepIdx: 2, want: []bool{false, true, true}},
		"the shown one stays over budget": {budget: 10, keepIdx: 0, want: []bool{true, false, false}},
		"an old width goes first":         {budget: 10, keepIdx: 2, oldWidth: true, want: []bool{false, false, true}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
			f.widths[diagramInline] = 80
			f.cacheBudget = tc.budget
			oldest := 80
			if tc.oldWidth {
				oldest = 40
			}
			keys := []diagramKey{
				heldDiagram(f, 1, oldest, 1, 10),
				heldDiagram(f, 2, 80, 2, 10),
				heldDiagram(f, 3, 80, 3, 10),
			}
			f.evictLocked(keys[tc.keepIdx])
			for i, key := range keys {
				_, held := f.entries[key]
				require.Equal(t, tc.want[i], held, "diagram %d", i)
			}
		})
	}
}

func TestMermaidFeatureEvictLockedOnlyDropsFailuresAtOldWidths(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	f.widths[diagramInline] = 80
	at := func(hash uint64, width int, state diagramState) diagramKey {
		key := diagramKey{hash: hash, width: width, kind: diagramInline, cellW: 10, cellH: 20}
		f.entries[key] = &diagramEntry{state: state}
		return key
	}
	oldFailure := at(1, 40, diagramFailed)
	failure := at(2, 80, diagramFailed)
	pending := at(3, 40, diagramPending)

	f.evictLocked(diagramKey{})
	require.NotContains(t, f.entries, oldFailure)
	require.Contains(t, f.entries, failure)
	require.Contains(t, f.entries, pending, "still rendering; dropped when it arrives")
}

// heldInline is a ready inline diagram of src, cols wide, that the terminal
// holds.
func heldInline(t *testing.T, f *mermaidFeature, src string, cols int) *diagramEntry {
	t.Helper()
	f.widths[diagramInline] = 80
	key := diagramKey{hash: f.imageHash(src, f.background()), width: 80, kind: diagramInline, cellW: 10, cellH: 20}
	e := &diagramEntry{state: diagramReady, cols: cols, rows: 1}
	f.entries[key] = e
	img := image.NewRGBA(image.Rect(0, 0, cols*10, 20))
	if cmd := fimage.EncodingKitty.Transmit(key.id(), img, fimage.CellSize{Width: 10, Height: 20}, cols, 1, false); cmd != nil {
		cmd()
	}
	return e
}

func TestMermaidFeatureInlineImageFitsTheDrillInExactly(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	const src = "flowchart LR\n Exactly --> Fits\n"
	e := heldInline(t, f, src, 60)
	f.clock = 5

	out, _ := f.inlineImage(src, 60)
	require.NotEmpty(t, out, "as wide as the drill-in")
	require.Equal(t, uint64(6), e.lastUsed, "counts as drawn now")

	out, _ = f.inlineImage(src, 59)
	require.Empty(t, out, "wider than the drill-in")
}

func TestMermaidFeatureCountsTheBytesTheTerminalHolds(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	key := diagramKey{hash: 0xb17e5, width: 80, kind: diagramInline, cellW: 10, cellH: 20}
	f.widths[diagramInline] = 80
	f.entries[key] = &diagramEntry{state: diagramPending}

	img := image.NewRGBA(image.Rect(0, 0, 30, 40))
	_, handled := f.HandleMsg(diagramReadyMsg{key: key, img: img, cols: 3, rows: 2, cellW: 10, cellH: 20})
	require.True(t, handled)
	require.Equal(t, 3*10*2*20*4, f.entries[key].bytes, "RGBA for every pixel of the cells")
}

func fullEntry(f *mermaidFeature) diagramKey {
	key := diagramKey{hash: 0xf011, width: 100, kind: diagramFull, cellW: 10, cellH: 20}
	f.entries[key] = &diagramEntry{state: diagramPending}
	return key
}

func TestMermaidFeatureObserveKeepsFullImagesWithoutAClosedDrillIn(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newIdleMermaidFeature(host, kittyCaps())
	key := fullEntry(f)
	f.Observe(nil)
	require.Contains(t, f.entries, key, "no drill-in was open")

	f.drill = NewChat(f.com)
	host.drills = []*Chat{f.drill}
	f.Observe(nil)
	require.Contains(t, f.entries, key, "its drill-in is still open")
}

func TestMermaidFeatureFirstDrillInKeepsFullImages(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, &common.Capabilities{})
	key := fullEntry(f)
	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n"})
	require.Contains(t, f.entries, key, "nothing to replace")

	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n C --> D\n"})
	require.NotContains(t, f.entries, key, "the replaced drill-in's image is freed")
}

// browserCmdMsg runs the command a diagramExportedMsg produced, skipping
// the listen it batches, which would block.
func browserCmdMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok)
	require.Len(t, batch, 2)
	return batch[1]()
}

func TestMermaidFeatureOpensAnExportedPage(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	var opened []string
	f.openURL = func(u string) error {
		opened = append(opened, u)
		return nil
	}
	cmd, handled := f.HandleMsg(diagramExportedMsg{path: "/tmp/d.html"})
	require.True(t, handled)
	require.Equal(t, util.NewInfoMsg("Opened diagram in browser"), browserCmdMsg(t, cmd))
	require.Equal(t, []string{"file:///tmp/d.html"}, opened)
}

func TestMermaidFeatureReportsAFailedExport(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	f.openURL = func(string) error { t.Fatal("a failed export opened the browser"); return nil }
	cmd, handled := f.HandleMsg(diagramExportedMsg{err: errors.New("disk full")})
	require.True(t, handled)
	require.Equal(t, util.NewErrorMsg(errors.New("open diagram: disk full")), browserCmdMsg(t, cmd))
}

func TestMermaidFeatureReportsABrowserThatWontOpen(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	require.Equal(t, util.NewErrorMsg(errors.New("open diagram: no browser in tests")), f.openInBrowser("/tmp/d.html")())
}

func TestPruneExportsKeepsDirectories(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.Mkdir(sub, 0o700))
	stale := time.Now().Add(-2 * diagramExportMaxAge)
	require.NoError(t, os.Chtimes(sub, stale, stale))

	pruneExports(dir)
	require.DirExists(t, sub)
}

func TestPruneExportsLooksPastRecentPages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	recent := filepath.Join(dir, "a-recent.html")
	old := filepath.Join(dir, "b-old.html")
	for _, p := range []string{recent, old} {
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
	}
	stale := time.Now().Add(-2 * diagramExportMaxAge)
	require.NoError(t, os.Chtimes(old, stale, stale))

	pruneExports(dir)
	require.FileExists(t, recent)
	require.NoFileExists(t, old)
}

// warnRecorder is a slog handler that keeps warning records.
type warnRecorder struct {
	mu    sync.Mutex
	warns []string
}

func (*warnRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *warnRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		r.mu.Lock()
		r.warns = append(r.warns, rec.Message)
		r.mu.Unlock()
	}
	return nil
}

func (r *warnRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *warnRecorder) WithGroup(string) slog.Handler      { return r }

// Not parallel: it swaps the default logger.
func TestPruneExportsWarnsNothingWhenItWorks(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dir := t.TempDir()
	old := filepath.Join(dir, "old.html")
	require.NoError(t, os.WriteFile(old, []byte("x"), 0o600))
	stale := time.Now().Add(-2 * diagramExportMaxAge)
	require.NoError(t, os.Chtimes(old, stale, stale))

	pruneExports(dir)
	require.NoFileExists(t, old)
	require.Empty(t, rec.warns)
}

func TestMermaidFeatureReportsEnginesThatWontLoad(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	loads := 0
	f.newRenderer = func() (*mermaid.Renderer, error) {
		loads++
		return nil, errors.New("no goja today")
	}
	for _, src := range []string{"flowchart LR\n Wont --> Load\n", "flowchart LR\n Still --> Wont\n"} {
		f.Diagram(src, 80)
		failed, ok := waitResult(t, f).(diagramReadyMsg)
		require.True(t, ok)
		require.ErrorIs(t, failed.err, errEnginesUnavailable)
		require.ErrorContains(t, failed.err, "no goja today")
	}
	require.Equal(t, 2, loads, "each request tries to load the engines again")
	select {
	case msg := <-f.results:
		t.Fatalf("one failure per request, got another %T", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMermaidFeatureInViewHoldsNothingWhenDiagramsCantDraw(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		caps  *common.Capabilities
		width int
	}{
		"no kitty":       {caps: &common.Capabilities{Columns: 100, Rows: 40, PixelX: 1000, PixelY: 800}, width: 80},
		"no cell width":  {caps: &common.Capabilities{KittyGraphics: true, Columns: 100, Rows: 40, PixelY: 800}, width: 80},
		"no cell height": {caps: &common.Capabilities{KittyGraphics: true, Columns: 100, Rows: 40, PixelX: 1000}, width: 80},
		"no width":       {caps: kittyCaps(), width: 0},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIdleMermaidFeature(&fakeDiagramHost{}, tc.caps)
			f.InView([]string{"flowchart LR\n A --> B\n"}, tc.width)
			require.Empty(t, f.inView)
			require.Empty(t, f.entries, "nothing is queued")
		})
	}
}
