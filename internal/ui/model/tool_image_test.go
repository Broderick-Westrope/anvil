package model

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	fimage "github.com/Broderick-Westrope/anvil/internal/ui/image"
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

// encodeImage is img in format, base64-encoded.
func encodeImage(t *testing.T, img image.Image, format string) string {
	t.Helper()
	var buf bytes.Buffer
	switch format {
	case "png":
		require.NoError(t, png.Encode(&buf, img))
	case "jpeg":
		require.NoError(t, jpeg.Encode(&buf, img, nil))
	case "gif":
		require.NoError(t, gif.Encode(&buf, img, nil))
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// pngIHDROnly is a PNG that is only a signature and an IHDR claiming w×h,
// so decoding its config works and decoding its pixels fails.
func pngIHDROnly(w, h uint32) string {
	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	_ = binary.Write(&ihdr, binary.BigEndian, w)
	_ = binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(ihdr.Len()-4))
	buf.Write(ihdr.Bytes())
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(ihdr.Bytes()))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// tinyWebP is a 1×1 lossless WebP.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

// requestImage asks f for an image and returns the worker's answer.
func requestImage(t *testing.T, f *mermaidFeature, data, mimeType string, width int) diagramReadyMsg {
	t.Helper()
	require.Equal(t, chat.DiagramPending, f.FullImage(data, mimeType, width).State)
	ready, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)
	return ready
}

func TestMermaidFeatureDecodesEachSupportedFormat(t *testing.T) {
	t.Parallel()

	square := image.NewRGBA(image.Rect(0, 0, 20, 20))
	tests := map[string]struct {
		data, mimeType string
		cols, rows     int
	}{
		"png":  {data: encodeImage(t, square, "png"), mimeType: "image/png", cols: 2, rows: 1},
		"jpeg": {data: encodeImage(t, square, "jpeg"), mimeType: "image/jpeg", cols: 2, rows: 1},
		"gif":  {data: encodeImage(t, square, "gif"), mimeType: "image/gif", cols: 2, rows: 1},
		"webp": {data: tinyWebP, mimeType: "image/webp", cols: 1, rows: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
			ready := requestImage(t, f, tc.data, tc.mimeType, 80)
			require.NoError(t, ready.err)
			require.Equal(t, tc.cols, ready.cols)
			require.Equal(t, tc.rows, ready.rows)
		})
	}
}

func TestMermaidFeatureShowsTheFirstFrameOfAnAnimatedGIF(t *testing.T) {
	t.Parallel()

	frame := func(c color.Color) *image.Paletted {
		img := image.NewPaletted(image.Rect(0, 0, 10, 20), color.Palette{c})
		return img
	}
	anim := &gif.GIF{
		Image: []*image.Paletted{frame(color.RGBA{R: 255, A: 255}), frame(color.RGBA{B: 255, A: 255})},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, anim))
	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())

	ready := requestImage(t, f, base64.StdEncoding.EncodeToString(buf.Bytes()), "image/gif", 80)

	require.NoError(t, ready.err)
	r, _, b, _ := ready.img.At(5, 5).RGBA()
	require.Equal(t, uint32(0xffff), r)
	require.Zero(t, b)
}

func TestMermaidFeatureFitsToolImagesToTheDrillIn(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		w, h, width int
		cols, rows  int
	}{
		"never enlarges a small image":      {w: 15, h: 15, width: 80, cols: 2, rows: 1},
		"shrinks a wide image to the width": {w: 1600, h: 400, width: 80, cols: 80, rows: 10},
		"extreme aspect stays visible":      {w: 8000, h: 1, width: 80, cols: 80, rows: 1},
		"caps a tall image's rows":          {w: 10, h: 8000, width: 80, cols: 1, rows: diagramMaxFullRows},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
			img := image.NewGray(image.Rect(0, 0, tc.w, tc.h))
			ready := requestImage(t, f, encodeImage(t, img, "png"), "image/png", tc.width)
			require.NoError(t, ready.err)
			require.Equal(t, tc.cols, ready.cols)
			require.Equal(t, tc.rows, ready.rows)
			require.Equal(t, image.Rect(0, 0, tc.cols*10, tc.rows*20), ready.img.Bounds())
		})
	}
}

func TestMermaidFeatureCapsAPaddedToolImageAtTheDrillInPixels(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	// Shrunk to exactly the cap, then padded to whole cells, it would pass
	// it.
	img := image.NewGray(image.Rect(0, 0, 4100, 4100))

	ready := requestImage(t, f, encodeImage(t, img, "png"), "image/png", 2000)

	require.NoError(t, ready.err)
	b := ready.img.Bounds()
	require.LessOrEqual(t, b.Dx()*b.Dy(), diagramFullMaxPixels)
	require.Equal(t, ready.cols*10, b.Dx())
	require.Equal(t, ready.rows*20, b.Dy())
}

func TestMermaidFeatureRefusesToolImagesItCantShow(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		data, mimeType string
		want           string
	}{
		"unsupported type":       {data: base64.StdEncoding.EncodeToString([]byte("<svg/>")), mimeType: "image/svg+xml", want: "unsupported image type image/svg+xml"},
		"too wide to decode":     {data: pngIHDROnly(8193, 10), mimeType: "image/png", want: "image too large (8193×10)"},
		"too many pixels":        {data: pngIHDROnly(8000, 4200), mimeType: "image/png", want: "image too large (8000×4200)"},
		"corrupt data":           {data: base64.StdEncoding.EncodeToString([]byte("not a png")), mimeType: "image/png", want: "decode image"},
		"truncated after header": {data: pngIHDROnly(10, 10), mimeType: "image/png", want: "decode image"},
		"invalid base64":         {data: "!!!", mimeType: "image/png", want: "decode image data"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
			ready := requestImage(t, f, tc.data, tc.mimeType, 80)
			require.ErrorContains(t, ready.err, tc.want)

			_, handled := f.HandleMsg(ready)
			require.True(t, handled)
			view := f.FullImage(tc.data, tc.mimeType, 80)
			require.Equal(t, chat.DiagramFailed, view.State)
			require.Contains(t, view.Err, tc.want)
		})
	}
}

func TestMermaidFeatureWarnsOnlyAboutImagesThatShouldHaveDecoded(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	for _, tc := range []struct{ data, mimeType string }{
		{data: base64.StdEncoding.EncodeToString([]byte("<svg/>")), mimeType: "image/svg+xml"},
		{data: pngIHDROnly(9000, 10), mimeType: "image/png"},
	} {
		f.HandleMsg(requestImage(t, f, tc.data, tc.mimeType, 80))
	}
	require.Empty(t, rec.warns, "expected, so not worth a warning")

	f.HandleMsg(requestImage(t, f, base64.StdEncoding.EncodeToString([]byte("not a png")), "image/png", 80))
	require.Len(t, rec.warns, 1)
}

// showToolImage drives a tool image owned by c from request to shown.
func showToolImage(t *testing.T, f *mermaidFeature, c *Chat, data string) diagramReadyMsg {
	t.Helper()
	images := f.ImagesFor(c)
	require.Equal(t, chat.DiagramPending, images.FullImage(data, "image/png", 80).State)
	ready := waitReady(t, f)
	_, handled := f.HandleMsg(ready)
	require.True(t, handled)
	transmit(ready)
	f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
	require.Equal(t, chat.DiagramReady, images.FullImage(data, "image/png", 80).State)
	return ready
}

// requireNothingQueued fails if f's worker was asked for anything.
func requireNothingQueued(t *testing.T, f *mermaidFeature) {
	t.Helper()
	select {
	case msg := <-f.results:
		t.Fatalf("the worker was asked for something: %T", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMermaidFeatureFreesAToolImageWhenItsDrillInLeavesTheTop(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	ready := showToolImage(t, f, drill, pngData(t, 30, 30))

	invalidations := host.invalidations
	deletes := rawMsgs(f.DrillInChanged(nil))

	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, ready.key)
	require.Empty(t, fimage.EncodingKitty.Render(ready.key.id(), ready.cols, ready.rows))
	require.Greater(t, host.invalidations, invalidations, "chats re-render without it")
}

func TestMermaidFeatureACoveredDrillInQueuesNothing(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	covered, top := NewChat(f.com), NewChat(f.com)
	f.DrillInChanged(top)

	data := pngData(t, 30, 30)
	require.Equal(t, chat.DiagramPending, f.ImagesFor(covered).FullImage(data, "image/png", 80).State)
	require.Equal(t, chat.DiagramPending, f.ForChat(covered).FullDiagram("flowchart LR\n A --> B\n", 80).State)
	requireNothingQueued(t, f)

	// Back on top, it renders again.
	f.DrillInChanged(covered)
	showToolImage(t, f, covered, data)
}

func TestMermaidFeatureDropsAResultFromAnEarlierOpening(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	data := pngData(t, 30, 30)
	f.DrillInChanged(drill)
	f.ImagesFor(drill).FullImage(data, "image/png", 80)
	stale := waitReady(t, f)

	// Closed and opened again while the first render was in flight.
	f.DrillInChanged(nil)
	f.DrillInChanged(drill)
	require.Equal(t, chat.DiagramPending, f.ImagesFor(drill).FullImage(data, "image/png", 80).State)
	fresh := waitReady(t, f)
	require.NotEqual(t, stale.key.id(), fresh.key.id(), "each opening has its own terminal image")

	_, handled := f.HandleMsg(stale)
	require.True(t, handled)
	require.NotContains(t, f.entries, stale.key)
	require.Equal(t, chat.DiagramPending, f.ImagesFor(drill).FullImage(data, "image/png", 80).State, "the stale result shows nothing")

	// The fresh one is shown, then the stale one's late transmit lands and
	// is freed again, which leaves the fresh one in the terminal.
	f.HandleMsg(fresh)
	transmit(fresh)
	f.HandleMsg(diagramShownMsg{key: fresh.key, cols: fresh.cols, rows: fresh.rows})
	transmit(stale)
	cmd, _ := f.HandleMsg(diagramShownMsg{key: stale.key, cols: stale.cols, rows: stale.rows})
	require.Len(t, rawMsgs(cmd), 1)
	require.Equal(t, chat.DiagramReady, f.ImagesFor(drill).FullImage(data, "image/png", 80).State)
	require.NotEmpty(t, fimage.EncodingKitty.Render(fresh.key.id(), fresh.cols, fresh.rows))
}

func TestMermaidFeatureADiagramDrillInFreesAToolImage(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestMermaidFeature(t, host, kittyCaps())
	tool := NewChat(f.com)
	f.DrillInChanged(tool)
	ready := showToolImage(t, f, tool, pngData(t, 30, 30))

	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n"})
	deletes := rawMsgs(f.DrillInChanged(host.drills[0]))

	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, ready.key)
}
