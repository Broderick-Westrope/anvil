package model

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

// pngData is a w×h PNG, base64-encoded as tool results carry it. Its
// pixels depend on the test's name: the Kitty image cache is global, so
// parallel tests showing the same image would free each other's.
func pngData(t *testing.T, w, h int) string {
	t.Helper()
	name := crc32.ChecksumIEEE([]byte(t.Name()))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: uint8(name), A: uint8(name >> 8)})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestGraphicsFeatureDrawsAToolImage(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	data := pngData(t, 35, 30)

	require.Equal(t, chat.DiagramPending, f.For(nil).FullImage(data, "image/png", 80).State)
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

	view := f.For(nil).FullImage(data, "image/png", 80)
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

// pngIHDROnly is a PNG that is only a signature and an IHDR claiming w×h
// RGBA, so decoding its config works and decoding its pixels fails.
func pngIHDROnly(w, h uint32) string { return pngIHDROnlyDepth(w, h, 8) }

// pngIHDROnlyDepth is pngIHDROnly with depth bits per channel.
func pngIHDROnlyDepth(w, h uint32, depth byte) string { return pngIHDROnlyType(w, h, depth, 6) }

// pngIHDROnlyType is pngIHDROnly with depth bits per channel of PNG colour
// type colorType (0 grey, 6 RGBA).
func pngIHDROnlyType(w, h uint32, depth, colorType byte) string {
	return pngIHDROnlyInterlace(w, h, depth, colorType, 0)
}

// pngIHDROnlyInterlace is pngIHDROnlyType with interlace method interlace
// (1 for Adam7).
func pngIHDROnlyInterlace(w, h uint32, depth, colorType, interlace byte) string {
	var ihdr bytes.Buffer
	ihdr.WriteString("IHDR")
	_ = binary.Write(&ihdr, binary.BigEndian, w)
	_ = binary.Write(&ihdr, binary.BigEndian, h)
	ihdr.Write([]byte{depth, colorType, 0, 0, interlace})
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	_ = binary.Write(&buf, binary.BigEndian, uint32(ihdr.Len()-4))
	buf.Write(ihdr.Bytes())
	_ = binary.Write(&buf, binary.BigEndian, crc32.ChecksumIEEE(ihdr.Bytes()))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// gifHeaderOnly is a GIF that is only a header claiming w×h with a
// two-colour palette, so it decodes to one byte a pixel.
func gifHeaderOnly(w, h uint16) string {
	var buf bytes.Buffer
	buf.WriteString("GIF89a")
	_ = binary.Write(&buf, binary.LittleEndian, w)
	_ = binary.Write(&buf, binary.LittleEndian, h)
	buf.Write([]byte{0x80, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff})
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// jpegHeaderOnly is a JPEG that is only a frame header claiming w×h in
// three components, progressive or baseline.
func jpegHeaderOnly(w, h uint16, progressive bool) string {
	return jpegHeaderOnlyComponents(w, h, progressive, 3)
}

// jpegHeaderOnlyComponents is jpegHeaderOnly with comps components (4 for
// CMYK, which an Adobe marker says isn't YCCK).
func jpegHeaderOnlyComponents(w, h uint16, progressive bool, comps byte) string {
	sof := byte(0xc0)
	if progressive {
		sof = 0xc2
	}
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xd8})
	if comps == 4 {
		// An Adobe header with no transform: CMYK.
		buf.Write([]byte{0xff, 0xee, 0x00, 0x0e, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0})
	} else {
		// A JFIF header, so the colour model is known from the frame
		// header.
		buf.Write([]byte{0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0})
	}
	buf.Write([]byte{0xff, sof, 0x00, byte(8 + 3*int(comps)), 0x08})
	_ = binary.Write(&buf, binary.BigEndian, h)
	_ = binary.Write(&buf, binary.BigEndian, w)
	buf.WriteByte(comps)
	for c := range comps {
		buf.Write([]byte{c + 1, 0x11, 0x00})
	}
	// Without JFIF, the config is only settled when a scan starts.
	buf.Write([]byte{0xff, 0xda, 0x00, 0x02})
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// webpLosslessHeaderOnly is a lossless WebP that is only a header claiming
// w×h.
func webpLosslessHeaderOnly(w, h uint32) string {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(4+8+6))
	buf.WriteString("WEBPVP8L")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(5))
	buf.WriteByte(0x2f)
	_ = binary.Write(&buf, binary.LittleEndian, (w-1)|(h-1)<<14)
	buf.WriteByte(0)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// tinyWebP is a 1×1 lossless WebP.
const tinyWebP = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

// requestImage asks f for an image and returns the worker's answer.
func requestImage(t *testing.T, f *graphicsFeature, data, mimeType string, width int) diagramReadyMsg {
	t.Helper()
	require.Equal(t, chat.DiagramPending, f.For(nil).FullImage(data, mimeType, width).State)
	ready, ok := waitResult(t, f).(diagramReadyMsg)
	require.True(t, ok)
	return ready
}

func TestGraphicsFeatureDecodesEachSupportedFormat(t *testing.T) {
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
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
			ready := requestImage(t, f, tc.data, tc.mimeType, 80)
			require.NoError(t, ready.err)
			require.Equal(t, tc.cols, ready.cols)
			require.Equal(t, tc.rows, ready.rows)
		})
	}
}

func TestGraphicsFeatureShowsTheFirstFrameOfAnAnimatedGIF(t *testing.T) {
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
	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())

	ready := requestImage(t, f, base64.StdEncoding.EncodeToString(buf.Bytes()), "image/gif", 80)

	require.NoError(t, ready.err)
	r, _, b, _ := ready.img.At(5, 5).RGBA()
	require.Equal(t, uint32(0xffff), r)
	require.Zero(t, b)
}

func TestGraphicsFeatureFitsToolImagesToTheDrillIn(t *testing.T) {
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
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
			img := image.NewGray(image.Rect(0, 0, tc.w, tc.h))
			ready := requestImage(t, f, encodeImage(t, img, "png"), "image/png", tc.width)
			require.NoError(t, ready.err)
			require.Equal(t, tc.cols, ready.cols)
			require.Equal(t, tc.rows, ready.rows)
			require.Equal(t, image.Rect(0, 0, tc.cols*10, tc.rows*20), ready.img.Bounds())
		})
	}
}

func TestGraphicsFeatureCapsAPaddedToolImageAtTheDrillInPixels(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
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

func TestGraphicsFeatureRefusesToolImagesItCantShow(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		data, mimeType string
		want           string
	}{
		"unsupported type":                                             {data: base64.StdEncoding.EncodeToString([]byte("<svg/>")), mimeType: "image/svg+xml", want: "unsupported image type image/svg+xml"},
		"too wide to decode":                                           {data: pngIHDROnly(8193, 10), mimeType: "image/png", want: "image too large (8193×10)"},
		"too many bytes once decoded":                                  {data: pngIHDROnlyDepth(6000, 4000, 16), mimeType: "image/png", want: "image too large (6000×4000)"},
		"16-bit grey, which transparency makes 16-bit RGBA":            {data: pngIHDROnlyType(6000, 4000, 16, 0), mimeType: "image/png", want: "image too large (6000×4000)"},
		"8-bit grey, which transparency makes RGBA":                    {data: pngIHDROnlyType(8000, 4200, 8, 0), mimeType: "image/png", want: "image too large (8000×4200)"},
		"too many pixels, though few bytes":                            {data: gifHeaderOnly(8000, 8000), mimeType: "image/gif", want: "image too large (8000×8000)"},
		"progressive JPEG, whose coefficients outweigh its pixels":     {data: jpegHeaderOnly(4000, 3000, true), mimeType: "image/jpeg", want: "image too large (4000×3000)"},
		"lossless WebP, which decodes through whole-image buffers":     {data: webpLosslessHeaderOnly(4000, 3000), mimeType: "image/webp", want: "image too large (4000×3000)"},
		"interlaced PNG, which holds a pass besides the image":         {data: pngIHDROnlyInterlace(6000, 4000, 8, 6, 1), mimeType: "image/png", want: "image too large (6000×4000)"},
		"baseline CMYK JPEG, which holds its planes besides the image": {data: jpegHeaderOnlyComponents(6000, 4000, false, 4), mimeType: "image/jpeg", want: "image too large (6000×4000)"},
		"too many pixels":                                              {data: pngIHDROnly(8000, 4200), mimeType: "image/png", want: "image too large (8000×4200)"},
		"corrupt data":                                                 {data: base64.StdEncoding.EncodeToString([]byte("not a png")), mimeType: "image/png", want: "decode image"},
		"truncated after header":                                       {data: pngIHDROnly(10, 10), mimeType: "image/png", want: "decode image"},
		"invalid base64":                                               {data: "!!!", mimeType: "image/png", want: "decode image data"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
			ready := requestImage(t, f, tc.data, tc.mimeType, 80)
			require.ErrorContains(t, ready.err, tc.want)

			_, handled := f.HandleMsg(ready)
			require.True(t, handled)
			view := f.For(nil).FullImage(tc.data, tc.mimeType, 80)
			require.Equal(t, chat.DiagramFailed, view.State)
			require.Contains(t, view.Err, tc.want)
		})
	}
}

func TestGraphicsFeatureWarnsOnlyAboutImagesThatShouldHaveDecoded(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
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
func showToolImage(t *testing.T, f *graphicsFeature, c *Chat, data string) diagramReadyMsg {
	t.Helper()
	images := f.For(c)
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
func requireNothingQueued(t *testing.T, f *graphicsFeature) {
	t.Helper()
	select {
	case msg := <-f.results:
		t.Fatalf("the worker was asked for something: %T", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestGraphicsFeatureFreesAToolImageWhenItsDrillInLeavesTheTop(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestGraphicsFeature(t, host, kittyCaps())
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

func TestGraphicsFeatureACoveredDrillInQueuesNothing(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	covered, top := NewChat(f.com), NewChat(f.com)
	f.DrillInChanged(top)

	data := pngData(t, 30, 30)
	require.Equal(t, chat.DiagramPending, f.For(covered).FullImage(data, "image/png", 80).State)
	require.Equal(t, chat.DiagramPending, f.For(covered).FullDiagram("flowchart LR\n A --> B\n", 80).State)
	requireNothingQueued(t, f)

	// Back on top, it renders again.
	f.DrillInChanged(covered)
	showToolImage(t, f, covered, data)
}

func TestGraphicsFeatureDropsAResultFromAnEarlierOpening(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	data := pngData(t, 30, 30)
	f.DrillInChanged(drill)
	f.For(drill).FullImage(data, "image/png", 80)
	stale := waitReady(t, f)

	// Closed and opened again while the first render was in flight.
	f.DrillInChanged(nil)
	f.DrillInChanged(drill)
	require.Equal(t, chat.DiagramPending, f.For(drill).FullImage(data, "image/png", 80).State)
	fresh := waitReady(t, f)
	require.NotEqual(t, stale.key.id(), fresh.key.id(), "each opening has its own terminal image")

	_, handled := f.HandleMsg(stale)
	require.True(t, handled)
	require.NotContains(t, f.entries, stale.key)
	require.Equal(t, chat.DiagramPending, f.For(drill).FullImage(data, "image/png", 80).State, "the stale result shows nothing")

	// The fresh one is shown, then the stale one's late transmit lands and
	// is freed again, which leaves the fresh one in the terminal.
	f.HandleMsg(fresh)
	transmit(fresh)
	f.HandleMsg(diagramShownMsg{key: fresh.key, cols: fresh.cols, rows: fresh.rows})
	transmit(stale)
	cmd, _ := f.HandleMsg(diagramShownMsg{key: stale.key, cols: stale.cols, rows: stale.rows})
	require.Len(t, rawMsgs(cmd), 1)
	require.Equal(t, chat.DiagramReady, f.For(drill).FullImage(data, "image/png", 80).State)
	require.NotEmpty(t, fimage.EncodingKitty.Render(fresh.key.id(), fresh.cols, fresh.rows))
}

func TestGraphicsFeatureADiagramDrillInFreesAToolImage(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestGraphicsFeature(t, host, kittyCaps())
	tool := NewChat(f.com)
	f.DrillInChanged(tool)
	ready := showToolImage(t, f, tool, pngData(t, 30, 30))

	f.HandleMsg(chat.DiagramOpenMsg{Source: "flowchart LR\n A --> B\n"})
	deletes := rawMsgs(f.DrillInChanged(host.drills[0]))

	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, ready.key)
}

func TestGraphicsFeatureOpensAToolImageInTheViewer(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mimeType, ext string
	}{
		"png":               {mimeType: "image/png", ext: ".png"},
		"jpeg":              {mimeType: "image/jpeg", ext: ".jpg"},
		"svg it can't draw": {mimeType: "image/svg+xml", ext: ".svg"},
		"unknown type":      {mimeType: "image/x-made-up", ext: ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
			f.exportDir = t.TempDir()
			var opened string
			f.openURL = func(u string) error { opened = u; return nil }
			data := base64.StdEncoding.EncodeToString([]byte("the bytes the model saw"))

			cmd, handled := f.HandleMsg(chat.ImageOpenMsg{Data: data, MIMEType: tc.mimeType})
			require.True(t, handled)
			require.Equal(t, util.NewInfoMsg("Opened image in viewer"), cmd())

			entries, err := os.ReadDir(f.exportDir)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			path := filepath.Join(f.exportDir, entries[0].Name())
			require.Equal(t, tc.ext, filepath.Ext(path))
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "the bytes the model saw", string(got))
			require.Equal(t, fileURL(path), opened)
		})
	}
}

func TestGraphicsFeatureReportsAToolImageItCantOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		data   string
		setup  func(f *graphicsFeature)
		reason string
	}{
		"invalid data": {data: "!!!", setup: func(*graphicsFeature) {}, reason: "decode image data"},
		"no viewer": {
			data:   base64.StdEncoding.EncodeToString([]byte("x")),
			setup:  func(f *graphicsFeature) { f.openURL = func(string) error { return errors.New("no viewer") } },
			reason: "no viewer",
		},
		"export dir under a file": {
			data: base64.StdEncoding.EncodeToString([]byte("x")),
			setup: func(f *graphicsFeature) {
				file := filepath.Join(f.exportDir, "file")
				_ = os.WriteFile(file, nil, 0o600)
				f.exportDir = filepath.Join(file, "images")
			},
			reason: "open image",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
			f.exportDir = t.TempDir()
			f.openURL = func(string) error { return nil }
			tc.setup(f)

			cmd, _ := f.HandleMsg(chat.ImageOpenMsg{Data: tc.data, MIMEType: "image/png"})
			msg, ok := cmd().(util.InfoMsg)
			require.True(t, ok)
			require.Equal(t, util.InfoTypeError, msg.Type)
			require.Contains(t, msg.Msg, tc.reason)
		})
	}
}

func TestGraphicsFeatureRedrawsAToolImageUnderANewTerminalID(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{bg: color.RGBA{R: 0x0c, G: 0x0e, B: 0x14, A: 0xff}}
	f := newTestGraphicsFeature(t, host, kittyCaps())
	f.Observe(nil)
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	data := pngData(t, 30, 30)
	old := showToolImage(t, f, drill, data)

	// Everything is forgotten, and the image asked for again at once, while
	// the delete for the old one is still on its way to the terminal.
	host.bg = nil
	f.Observe(tea.BackgroundColorMsg{Color: color.RGBA{R: 0x05, G: 0x00, B: 0x14, A: 0xff}})
	fresh := showToolImage(t, f, drill, data)

	require.NotEqual(t, old.key.id(), fresh.key.id(), "the late delete can't hit the new image")
}

func TestGraphicsFeatureAToolDrillInFreesADiagramImage(t *testing.T) {
	t.Parallel()

	host := &fakeDiagramHost{}
	f := newTestGraphicsFeature(t, host, kittyCaps())
	f.HandleMsg(chat.DiagramOpenMsg{Source: tallFlowchart(30)})
	f.DrillInChanged(host.drills[0])
	item := host.drills[0].MessageItems()[0].(*chat.DiagramDetailItem)
	item.RawRender(80)
	full := waitReady(t, f)
	f.HandleMsg(full)
	transmit(full)
	f.HandleMsg(diagramShownMsg{key: full.key, cols: full.cols, rows: full.rows})
	require.Contains(t, f.entries, full.key)

	tool := NewChat(f.com)
	deletes := rawMsgs(f.DrillInChanged(tool))

	require.Len(t, deletes, 1)
	require.NotContains(t, f.entries, full.key)
	showToolImage(t, f, tool, pngData(t, 30, 30))
}

func TestGraphicsFeatureDecodesAnEightBitImageThatSixteenBitsWouldRefuse(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	ready := requestImage(t, f, pngIHDROnly(6000, 4000), "image/png", 80)
	require.ErrorContains(t, ready.err, "decode image:", "it got past the size check to the pixels")
}

func TestDecodedBytesPerPixel(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		model color.Model
		want  int
	}{
		"16-bit RGBA":  {model: color.RGBA64Model, want: 8},
		"16-bit NRGBA": {model: color.NRGBA64Model, want: 8},
		"16-bit grey, which transparency can make 16-bit RGBA": {model: color.Gray16Model, want: 8},
		"grey, which transparency can make RGBA":               {model: color.GrayModel, want: 4},
		"paletted":                                             {model: color.Palette{color.Black}, want: 1},
		"8-bit RGBA":                                           {model: color.RGBAModel, want: 4},
		"YCbCr, as JPEG":                                       {model: color.YCbCrModel, want: 4},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, decodedBytesPerPixel(tc.model))
		})
	}
}

func TestBitmapLimits(t *testing.T) {
	t.Parallel()

	require.Equal(t, 8192, bitmapMaxSide)
	require.Equal(t, 33_554_432, bitmapMaxPixels)
	require.Equal(t, 134_217_728, bitmapMaxDecodedBytes)
}

func TestGraphicsFeatureGivesAWidthItReturnsToANewTerminalID(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	images := f.For(drill)
	data := pngData(t, 900, 30)

	show := func(width int) diagramReadyMsg {
		t.Helper()
		require.Equal(t, chat.DiagramPending, images.FullImage(data, "image/png", width).State)
		ready := waitReady(t, f)
		f.HandleMsg(ready)
		transmit(ready)
		f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
		return ready
	}
	first := show(80)
	show(60) // Its shown message evicts the first, whose delete may land late.
	again := show(80)

	require.NotEqual(t, first.key.id(), again.key.id())
}

func TestGraphicsFeatureKeepsOnlyTheCurrentWidthsImageThroughAResize(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	images := f.For(drill)
	data := pngData(t, 900, 30)

	// Resized 80 → 60 → 80 while all three renders are in flight.
	for _, width := range []int{80, 60, 80} {
		require.Equal(t, chat.DiagramPending, images.FullImage(data, "image/png", width).State)
	}
	var results []diagramReadyMsg
	for range 3 {
		results = append(results, waitReady(t, f))
	}
	for _, ready := range results {
		f.HandleMsg(ready)
		transmit(ready)
		cmd, _ := f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
		if ready != results[2] {
			require.Len(t, rawMsgs(cmd), 1, "a stale image that reached the terminal is freed")
		}
	}

	require.Equal(t, chat.DiagramReady, images.FullImage(data, "image/png", 80).State)
	var full []diagramKey
	for key := range f.entries {
		if key.kind == diagramFull {
			full = append(full, key)
		}
	}
	require.Equal(t, []diagramKey{results[2].key}, full, "one full-size image, the current one")
}

func TestGraphicsFeatureFreesAShownImageWhenItsWidthComesBackDuringARender(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	images := f.For(drill)
	data := pngData(t, 900, 30)
	show := func(ready diagramReadyMsg) tea.Cmd {
		f.HandleMsg(ready)
		transmit(ready)
		cmd, _ := f.HandleMsg(diagramShownMsg{key: ready.key, cols: ready.cols, rows: ready.rows})
		return cmd
	}

	images.FullImage(data, "image/png", 80)
	first := waitReady(t, f)
	show(first)
	// Resized to 60 and back to 80 before the 60 render came back.
	images.FullImage(data, "image/png", 60)
	images.FullImage(data, "image/png", 80)
	narrow, current := waitReady(t, f), waitReady(t, f)
	show(narrow)

	evict := show(current)

	require.Len(t, rawMsgs(evict), 1, "the first image is freed")
	require.NotContains(t, f.entries, first.key)
	require.Contains(t, f.entries, current.key)
}

func TestGraphicsFeatureDecodesImagesWithinTheirDecodersBudget(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ data, mimeType string }{
		"baseline JPEG the progressive limit would refuse":     {data: jpegHeaderOnly(4000, 3000, false), mimeType: "image/jpeg"},
		"non-interlaced PNG the interlaced limit would refuse": {data: pngIHDROnly(6000, 4000), mimeType: "image/png"},
		"YCbCr JPEG the CMYK limit would refuse":               {data: jpegHeaderOnly(6000, 4000, false), mimeType: "image/jpeg"},
		"lossless WebP under its limit":                        {data: webpLosslessHeaderOnly(3000, 3000), mimeType: "image/webp"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := base64.StdEncoding.DecodeString(tc.data)
			require.NoError(t, err)
			_, _, err = image.DecodeConfig(bytes.NewReader(raw))
			require.NoError(t, err, "the header alone is a valid config")
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
			ready := requestImage(t, f, tc.data, tc.mimeType, 80)
			require.Error(t, ready.err)
			require.NotContains(t, ready.err.Error(), "too large", "it got past the size check to the pixels")
		})
	}
}

func TestJPEGProgressive(t *testing.T) {
	t.Parallel()

	soi := []byte{0xff, 0xd8}
	app0 := []byte{0xff, 0xe0, 0x00, 0x04, 0xaa, 0xbb}
	frame := func(sof byte) []byte { return []byte{0xff, sof, 0x00, 0x02} }
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	tests := map[string]struct {
		raw  []byte
		want bool
	}{
		"baseline":                       {raw: join(soi, frame(0xc0)), want: false},
		"extended sequential":            {raw: join(soi, frame(0xc1)), want: false},
		"progressive":                    {raw: join(soi, frame(0xc2)), want: true},
		"after another segment":          {raw: join(soi, app0, frame(0xc0)), want: false},
		"after fill bytes":               {raw: join(soi, []byte{0xff}, frame(0xc0)), want: false},
		"a scan before any frame header": {raw: join(soi, []byte{0xff, 0xda, 0x00, 0x02}), want: true},
		"not a marker":                   {raw: join(soi, []byte{0x00, 0xc0, 0x00, 0x02}), want: true},
		"no frame header":                {raw: join(soi, app0), want: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, jpegProgressive(tc.raw))
		})
	}
}

func TestPNGInterlaced(t *testing.T) {
	t.Parallel()

	header := func(interlace byte) []byte {
		raw, _ := base64.StdEncoding.DecodeString(pngIHDROnlyInterlace(10, 10, 8, 6, interlace))
		return raw
	}
	require.True(t, pngInterlaced(header(1)))
	require.False(t, pngInterlaced(header(0)))
	require.False(t, pngInterlaced(header(1)[:28]), "too short to say")
}

func TestGraphicsFeatureKeepsOnlyTheLatestImageAtOneWidth(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	drill := NewChat(f.com)
	f.DrillInChanged(drill)
	images := f.For(drill)
	shown := showToolImage(t, f, drill, pngData(t, 20, 30))

	// The result changed twice at the same width, the second time while the
	// first change still rendered.
	older, latest := pngData(t, 30, 30), pngData(t, 40, 30)
	images.FullImage(older, "image/png", 80)
	images.FullImage(latest, "image/png", 80)
	stale, current := waitReady(t, f), waitReady(t, f)

	_, handled := f.HandleMsg(stale)
	require.True(t, handled)
	require.NotContains(t, f.entries, stale.key, "the older render is dropped")
	f.HandleMsg(current)
	transmit(current)
	evict, _ := f.HandleMsg(diagramShownMsg{key: current.key, cols: current.cols, rows: current.rows})

	require.Len(t, rawMsgs(evict), 1, "the image shown before is freed")
	var full []diagramKey
	for key := range f.entries {
		if key.kind == diagramFull {
			full = append(full, key)
		}
	}
	require.Equal(t, []diagramKey{current.key}, full, "one full-size image, the latest asked for")
	require.NotContains(t, f.entries, shown.key)
}
