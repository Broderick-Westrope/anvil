package model

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// copyToolImage asks f to copy data and returns the worker's answer, and
// what reached the clipboard.
func copyToolImage(t *testing.T, f *graphicsFeature, data string) (diagramCopiedMsg, [][]byte) {
	t.Helper()
	clip := &clipboardRecorder{}
	f.writeImage = clip.write
	cmd, handled := f.HandleMsg(chat.ImageCopyMsg{Data: data})
	require.True(t, handled)
	require.Equal(t, util.NewInfoMsg("Copying image"), cmd())
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	return copied, clip.images()
}

func TestGraphicsFeatureCopiesAPNGToolImageAsItIs(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	// Uncompressed, so encoding it again would change its bytes.
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	require.NoError(t, enc.Encode(&buf, halves(30, 20)))
	data := base64.StdEncoding.EncodeToString(buf.Bytes())

	copied, written := copyToolImage(t, f, data)

	require.NoError(t, copied.err)
	raw, err := base64.StdEncoding.DecodeString(data)
	require.NoError(t, err)
	require.Equal(t, [][]byte{raw}, written, "the bytes the model saw")
	require.Empty(t, f.entries, "copying caches nothing")
}

func TestGraphicsFeatureCopiesOtherToolImagesAsPNG(t *testing.T) {
	t.Parallel()

	img := halves(40, 20)
	tests := map[string]string{
		"jpeg": encodeImage(t, img, "jpeg"),
		"gif":  encodeImage(t, img, "gif"),
		"webp": tinyWebP,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())

			copied, written := copyToolImage(t, f, data)

			require.NoError(t, copied.err)
			require.Len(t, written, 1)
			got, err := png.Decode(bytes.NewReader(written[0]))
			require.NoError(t, err, "written as PNG")
			raw, err := base64.StdEncoding.DecodeString(data)
			require.NoError(t, err)
			cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
			require.NoError(t, err)
			require.Equal(t, image.Rect(0, 0, cfg.Width, cfg.Height), got.Bounds(), "at its own size")
		})
	}
}

func TestGraphicsFeatureWontCopyAToolImageItCantDecode(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ data, want string }{
		"not an image":   {data: base64.StdEncoding.EncodeToString([]byte("<svg/>")), want: "not a PNG, JPEG, GIF or WebP image"},
		"too large":      {data: pngIHDROnly(9000, 10), want: "image too large (9000×10)"},
		"corrupt":        {data: pngIHDROnly(10, 10), want: "decode image:"},
		"invalid base64": {data: "!!!", want: "decode image data"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())

			copied, written := copyToolImage(t, f, tc.data)

			require.ErrorContains(t, copied.err, tc.want)
			require.Empty(t, written, "nothing reaches the clipboard")
		})
	}
}

func TestGraphicsFeatureReportsACopiedToolImage(t *testing.T) {
	t.Parallel()

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	cmd, _ := f.HandleMsg(diagramCopiedMsg{what: toolImageCopy})
	require.Equal(t, util.NewInfoMsg("Image copied to clipboard"), browserCmdMsg(t, cmd))

	cmd, _ = f.HandleMsg(diagramCopiedMsg{what: toolImageCopy, err: errors.New("no clipboard")})
	require.Equal(t, util.NewErrorMsg(errors.New("copy image: no clipboard")), browserCmdMsg(t, cmd))
}

func TestGraphicsFeatureWontCopyAToolImageWhereTheClipboardIsntUsable(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup func(*graphicsFeature)
		want  util.InfoMsg
	}{
		"over ssh": {
			setup: func(f *graphicsFeature) { f.caps.Env = uv.Environ{"SSH_TTY=x"} },
			want:  util.NewWarnMsg("Can't copy an image over SSH"),
		},
		"no clipboard": {
			setup: func(f *graphicsFeature) { f.clipboardReady = func() error { return errors.New("no clipboard tool") } },
			want:  util.NewErrorMsg(errors.New("copy image: no clipboard tool")),
		},
		"busy": {
			setup: func(f *graphicsFeature) { f.requests = make(chan diagramRequest) },
			want:  util.NewWarnMsg("Image decoder is busy; try again"),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
			stubClipboard(f)
			tc.setup(f)

			cmd, handled := f.HandleMsg(chat.ImageCopyMsg{Data: pngData(t, 2, 2)})

			require.True(t, handled)
			require.Equal(t, tc.want, cmd())
		})
	}
}

// Not parallel: it swaps the default logger.
func TestGraphicsFeatureLogsAFailedToolImageCopy(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	f.HandleMsg(diagramCopiedMsg{what: toolImageCopy, err: errors.New("no clipboard")})
	require.Equal(t, []string{"Failed to copy tool image"}, rec.warns)
}

func TestGraphicsFeatureReportsACrashWhileCopyingAToolImage(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	f.writeImage = func([]byte) error { panic("clipboard library bug") }
	req := diagramRequest{key: diagramKey{producer: producerBitmap}, data: pngData(t, 2, 2), job: diagramJobCopy}

	msg, ok := f.runRequest(nil, req)

	require.False(t, ok)
	copied, isCopy := msg.(diagramCopiedMsg)
	require.True(t, isCopy)
	require.Equal(t, toolImageCopy, copied.what)
	require.ErrorContains(t, copied.err, "image decoder crashed: clipboard library bug")
}
