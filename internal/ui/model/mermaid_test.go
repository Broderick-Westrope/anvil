package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/require"
)

type fakeDiagramHost struct{ invalidations int }

func (h *fakeDiagramHost) InvalidateChat() { h.invalidations++ }

func kittyCaps() *common.Capabilities {
	return &common.Capabilities{KittyGraphics: true, Columns: 100, Rows: 40, PixelX: 1000, PixelY: 800}
}

func waitReady(t *testing.T, f *mermaidFeature) diagramReadyMsg {
	t.Helper()
	select {
	case ready := <-f.results:
		require.NoError(t, ready.err)
		return ready
	case <-time.After(10 * time.Second):
		t.Fatal("diagram never rendered")
		return diagramReadyMsg{}
	}
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
	f := newMermaidFeature(host, kittyCaps())
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

	f := newMermaidFeature(&fakeDiagramHost{}, &common.Capabilities{PixelX: 1000, PixelY: 800, Columns: 100, Rows: 40})
	_, ok := f.Diagram("flowchart LR\n A --> B\n", 80)
	require.False(t, ok)
	require.Empty(t, f.entries, "nothing is queued")
}

func TestMermaidFeatureReleasesEnginesWhenIdle(t *testing.T) {
	t.Parallel()

	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps())
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

	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps())
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

	f := newMermaidFeature(&fakeDiagramHost{}, kittyCaps())
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
