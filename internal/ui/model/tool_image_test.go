package model

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/require"
)

// pngData is a w×h PNG, base64-encoded as tool results carry it.
func pngData(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestMermaidFeatureDrawsAToolImage(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	data := pngData(t, 35, 30)

	require.Equal(t, chat.DiagramPending, f.FullImage(data, "image/png", 80).State)
	ready := waitReady(t, f)
	require.Equal(t, diagramFull, ready.key.kind)
	// Cells are 10×20 px: actual size, padded to whole cells.
	require.Equal(t, 4, ready.cols)
	require.Equal(t, 2, ready.rows)
	require.Equal(t, image.Rect(0, 0, 40, 40), ready.img.Bounds())

	_, handled := f.HandleMsg(ready)
	require.True(t, handled)
	transmit(ready)
	f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})

	view := f.FullImage(data, "image/png", 80)
	require.Equal(t, chat.DiagramReady, view.State)
	require.Contains(t, view.Image, string(kitty.Placeholder))
}

func TestToolDrillInDrawsImagesWithDiagramsOff(t *testing.T) {
	t.Parallel()

	ws := newWorkflowTestWorkspace()
	ws.diagramsOff = true
	u := New(common.DefaultCommon(ws), "", false)
	u.caps = *kittyCaps()
	sty := uistyles.TokyoNight()
	result := &message.ToolResult{ToolCallID: "tc-1", Data: pngData(t, 4, 4), MIMEType: "image/png"}
	item := chat.NewToolMessageItem(&sty, "msg-1", message.ToolCall{ID: "tc-1", Name: "view", Input: `{"file_path":"a.png"}`, Finished: true}, result, false, chat.ToolItemOptions{})

	u.Update(util.ToolDrillInMsg{ToolItem: item, Label: "View"})

	require.True(t, u.isDrilledIn())
	items := u.drillStack[len(u.drillStack)-1].chat.MessageItems()
	require.Contains(t, ansi.Strip(items[len(items)-1].Render(80)), "open in viewer")
}
