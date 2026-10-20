package model

import (
	"fmt"
	"image/color"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/require"
)

type fakeDiagramHost struct {
	invalidations int
	bg            color.Color
	drills        []*Chat
	labels        []string
}

func (h *fakeDiagramHost) InvalidateChat()            { h.invalidations++ }
func (h *fakeDiagramHost) AppBackground() color.Color { return h.bg }
func (h *fakeDiagramHost) PushDrillIn(c *Chat, label string) {
	h.drills = append(h.drills, c)
	h.labels = append(h.labels, label)
}

func (h *fakeDiagramHost) DrillInOpen(c *Chat) bool { return slices.Contains(h.drills, c) }

func newTestMermaidFeature(t *testing.T, host *fakeDiagramHost, caps *common.Capabilities) *mermaidFeature {
	t.Helper()
	st := uistyles.TokyoNight()
	f := newMermaidFeature(host, caps, &common.Common{Styles: &st})
	f.exportDir = t.TempDir()
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
	case <-time.After(10 * time.Second):
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

	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	raw := fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false)()
	require.IsType(t, tea.RawMsg{}, raw)

	evict, handled := f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
	require.True(t, handled)
	return ready, evict
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

	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false)()
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
	require.Error(t, failed.err)

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

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	require.Equal(t, 20*time.Second, f.idleTimeout)
	f.idleTimeout = 50 * time.Millisecond
	var returned atomic.Int32
	f.returnMemory = func() { returned.Add(1) }
	require.False(t, f.enginesLoaded.Load(), "nothing loads before the first diagram")

	f.Diagram("flowchart LR\n A --> B\n", 80)
	waitReady(t, f)
	require.True(t, f.enginesLoaded.Load())
	require.Eventually(t, func() bool { return !f.enginesLoaded.Load() }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return returned.Load() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the freed memory is handed back to the OS")

	f.Diagram("flowchart LR\n C --> D\n", 80)
	waitReady(t, f)
	require.True(t, f.enginesLoaded.Load(), "the engines load again on demand")
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
	cs := fimage.CellSize{Width: full.cellW, Height: full.cellH}
	fimage.EncodingKitty.Transmit(full.key.id(), full.img, cs, full.cols, full.rows, false)()
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
	cs := fimage.CellSize{Width: full.cellW, Height: full.cellH}
	fimage.EncodingKitty.Transmit(full.key.id(), full.img, cs, full.cols, full.rows, false)()
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
