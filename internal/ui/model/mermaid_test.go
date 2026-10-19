package model

import (
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

func TestMermaidFeatureRendersKittyPlaceholders(t *testing.T) {
	t.Parallel()

	caps := &common.Capabilities{KittyGraphics: true, Columns: 100, Rows: 40, PixelX: 1000, PixelY: 800}
	host := &fakeDiagramHost{}
	f := newMermaidFeature(host, caps)
	src := "flowchart LR\n  A[Prompt] --> B[Pele] --> C[Kitty]\n"

	_, ok := f.Diagram(src, 80)
	require.False(t, ok, "first request only queues the render")
	_, ok = f.Diagram(src, 80)
	require.False(t, ok, "a pending render isn't queued twice")

	var ready diagramReadyMsg
	select {
	case ready = <-f.results:
	case <-time.After(10 * time.Second):
		t.Fatal("diagram never rendered")
	}
	require.NoError(t, ready.err)
	require.LessOrEqual(t, ready.cols, 80)
	require.LessOrEqual(t, ready.rows, diagramMaxRows)
	require.Equal(t, ready.cols*10, ready.img.Bounds().Dx(), "padded to whole cells")
	require.Equal(t, ready.rows*20, ready.img.Bounds().Dy(), "padded to whole cells")
	t.Logf("diagram: %dx%d cells", ready.cols, ready.rows)

	_, handled := f.HandleMsg(ready)
	require.True(t, handled)
	_, ok = f.Diagram(src, 80)
	require.False(t, ok, "not shown until the image is transmitted")

	// What the transmit command in HandleMsg's sequence does.
	cs := fimage.CellSize{Width: ready.cellW, Height: ready.cellH}
	raw := fimage.EncodingKitty.Transmit(ready.key.id(), ready.img, cs, ready.cols, ready.rows, false)()
	require.IsType(t, tea.RawMsg{}, raw)

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
