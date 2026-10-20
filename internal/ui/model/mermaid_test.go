package model

import (
	"fmt"
	"image/color"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
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
	_, ok := f.Diagram(src, width)
	require.False(t, ok, "first request only queues the render")
	ready := waitReady(t, f)
	_, handled := f.HandleMsg(ready)
	require.True(t, handled)

	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	raw := fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false)()
	require.IsType(t, tea.RawMsg{}, raw)

	evict, handled := f.HandleMsg(diagramShownMsg{key: ready.key})
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

	_, ok := f.Diagram(src, 80)
	require.False(t, ok, "first request only queues the render")
	_, ok = f.Diagram(src, 80)
	require.False(t, ok, "a pending render isn't queued twice")

	ready := waitReady(t, f)
	require.LessOrEqual(t, ready.cols, 80)
	require.LessOrEqual(t, ready.rows, diagramMaxRows)
	require.Equal(t, ready.cols*10, ready.img.Bounds().Dx(), "padded to whole cells")
	require.Equal(t, ready.rows*20, ready.img.Bounds().Dy(), "padded to whole cells")

	_, handled := f.HandleMsg(ready)
	require.True(t, handled)
	_, ok = f.Diagram(src, 80)
	require.False(t, ok, "not shown until the image is transmitted")

	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false)()
	_, handled = f.HandleMsg(diagramShownMsg{key: ready.key})
	require.True(t, handled)
	require.Equal(t, 1, host.invalidations)

	out, ok := f.Diagram(src, 80)
	require.True(t, ok)
	require.Contains(t, out, string(kitty.Placeholder))
}

func TestMermaidFeatureWithoutKittyKeepsCodeBlock(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, &common.Capabilities{PixelX: 1000, PixelY: 800, Columns: 100, Rows: 40})
	_, ok := f.Diagram("flowchart LR\n A --> B\n", 80)
	require.False(t, ok)
	require.Empty(t, f.entries, "nothing is queued")
}

func TestMermaidFeatureReleasesEnginesWhenIdle(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	f.idleTimeout = 50 * time.Millisecond
	require.False(t, f.enginesLoaded.Load(), "nothing loads before the first diagram")

	f.Diagram("flowchart LR\n A --> B\n", 80)
	waitReady(t, f)
	require.True(t, f.enginesLoaded.Load())
	require.Eventually(t, func() bool { return !f.enginesLoaded.Load() }, 5*time.Second, 10*time.Millisecond)

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
	_, ok := f.Diagram(srcs[0], 80)
	require.True(t, ok)

	third, evict := showDiagram(t, f, srcs[2], 80)
	require.Len(t, rawMsgs(evict), 1)
	require.Contains(t, f.entries, first.key)
	require.NotContains(t, f.entries, second.key)
	require.Contains(t, f.entries, third.key)

	out, ok := f.Diagram(srcs[1], 80)
	require.False(t, ok, "an evicted diagram renders again")
	require.Empty(t, out)
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
	require.NotEqual(t, sourceHash(src, f.theme().With("pele-bg", "#0c0e14")), sourceHash(src, f.theme()),
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
	f.HandleMsg(diagramShownMsg{key: full.key})
	require.Contains(t, item.RawRender(80), string(kitty.Placeholder))

	// Closing the drill-in frees the full-size image.
	host.drills = nil
	deletes := rawMsgs(f.Observe(nil))
	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, full.key)
	require.Nil(t, f.drill)
}

func TestMermaidFeatureExportsForTheBrowser(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		caps     *common.Capabilities
		external bool
	}{
		"asked to":             {caps: kittyCaps(), external: true},
		"terminal lacks kitty": {caps: &common.Capabilities{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			host := &fakeDiagramHost{}
			f := newTestMermaidFeature(t, host, tc.caps)
			cmd, handled := f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n", External: tc.external})
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
