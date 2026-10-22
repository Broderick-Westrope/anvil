package model

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"log/slog"
	"math"
	"sync"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// clipboardRecorder stands in for the system clipboard. The worker writes
// to it, so it locks.
type clipboardRecorder struct {
	mu      sync.Mutex
	written [][]byte
	err     error
}

func (c *clipboardRecorder) write(png []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, png)
	return c.err
}

func (c *clipboardRecorder) images() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

func TestGraphicsFeatureCopiesADiagramAsAPNG(t *testing.T) {
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

			f := newTestGraphicsFeature(t, &fakeDiagramHost{}, tc.caps)
			clip := &clipboardRecorder{}
			f.writeImage = clip.write
			const src = "flowchart LR\n A --> B\n"
			cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: src})
			require.True(t, handled)
			require.Equal(t, util.NewInfoMsg("Copying diagram image"), cmd())

			copied, ok := waitResult(t, f).(diagramCopiedMsg)
			require.True(t, ok)
			require.NoError(t, copied.err)
			require.Empty(t, f.entries, "copying caches nothing")
			written := clip.images()
			require.Len(t, written, 1, "the worker wrote it")
			img, err := png.Decode(bytes.NewReader(written[0]))
			require.NoError(t, err)

			pele, err := mermaid.NewRenderer()
			require.NoError(t, err)
			layout, err := pele.Render(context.Background(), src, mermaid.Options{})
			require.NoError(t, err)
			require.Equal(t, int(math.Ceil(2*layout.Width)), img.Bounds().Dx(), "twice the layout's size")
			require.Equal(t, int(math.Ceil(2*layout.Height)), img.Bounds().Dy(), "twice the layout's size")

			// The copy is the chat's transparent image drawn over pele-bg.
			raster, err := mermaid.NewRasterizer(context.Background(), f.compileCacheDir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = raster.Close() })
			theme := f.theme()
			drawn, err := raster.Rasterize(layout, theme, mermaid.RasterOptions{Scale: 2})
			require.NoError(t, err)
			want := image.NewRGBA(drawn.Bounds())
			draw.Draw(want, want.Bounds(), image.NewUniform(lipgloss.Color(theme["pele-bg"])), image.Point{}, draw.Src)
			draw.Draw(want, want.Bounds(), drawn, image.Point{}, draw.Over)
			partial := 0
			for i := 0; i < len(drawn.Pix); i += 4 {
				a := drawn.Pix[i+3]
				require.LessOrEqual(t, max(drawn.Pix[i], drawn.Pix[i+1], drawn.Pix[i+2]), a, "resvg's pixels are premultiplied")
				if a != 0 && a != 0xff {
					partial++
				}
			}
			require.NotZero(t, partial, "the edges are antialiased, so the blend is tested")
			for y := range want.Bounds().Dy() {
				for x := range want.Bounds().Dx() {
					wr, wg, wb, wa := want.At(x, y).RGBA()
					gr, gg, gb, ga := img.At(x, y).RGBA()
					for c, pair := range [][2]uint32{{wr, gr}, {wg, gg}, {wb, gb}, {wa, ga}} {
						require.InDelta(t, pair[0]>>8, pair[1]>>8, 1, "pixel %d,%d channel %d", x, y, c)
					}
				}
			}
		})
	}
}

func TestGraphicsFeatureCopiesInTheOrderAsked(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	clip := &clipboardRecorder{}
	f.writeImage = clip.write
	sources := []string{"flowchart LR\n A --> B\n", "flowchart LR\n A --> B --> C --> D\n"}
	for _, src := range sources {
		f.HandleMsg(chat.DiagramCopyImageMsg{Source: src})
	}
	for range sources {
		copied, ok := waitResult(t, f).(diagramCopiedMsg)
		require.True(t, ok)
		require.NoError(t, copied.err)
	}
	written := clip.images()
	require.Len(t, written, 2)
	first, err := png.Decode(bytes.NewReader(written[0]))
	require.NoError(t, err)
	last, err := png.Decode(bytes.NewReader(written[1]))
	require.NoError(t, err)
	require.Less(t, first.Bounds().Dx(), last.Bounds().Dx(), "the newest copy is written last")
}

func TestGraphicsFeatureReportsACopiedImage(t *testing.T) {
	t.Parallel()

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	cmd, handled := f.HandleMsg(diagramCopiedMsg{what: diagramCopy})
	require.True(t, handled)
	require.Equal(t, util.NewInfoMsg("Diagram image copied to clipboard"), browserCmdMsg(t, cmd))
}

func TestGraphicsFeatureReportsAFailedCopy(t *testing.T) {
	t.Parallel()

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	cmd, handled := f.HandleMsg(diagramCopiedMsg{what: diagramCopy, err: errors.New("no clipboard")})
	require.True(t, handled)
	require.Equal(t, util.NewErrorMsg(errors.New("copy diagram image: no clipboard")), browserCmdMsg(t, cmd))
}

func TestGraphicsFeatureReportsAClipboardThatWontTakeTheImage(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	clip := &clipboardRecorder{err: errors.New("clipboard is busy")}
	f.writeImage = clip.write
	f.HandleMsg(chat.DiagramCopyImageMsg{Source: "flowchart LR\n A --> B\n"})
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	require.EqualError(t, copied.err, "clipboard is busy")
}

func TestGraphicsFeatureReportsADiagramThatWontRenderForCopying(t *testing.T) {
	t.Parallel()

	f := newTestGraphicsFeature(t, &fakeDiagramHost{}, kittyCaps())
	clip := &clipboardRecorder{}
	f.writeImage = clip.write
	f.HandleMsg(chat.DiagramCopyImageMsg{Source: "notADiagram at all\n"})
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	require.ErrorContains(t, copied.err, "No diagram type detected")
	require.Empty(t, clip.images(), "nothing reaches the clipboard")
}

func TestGraphicsFeatureWarnsWhenTooBusyToCopy(t *testing.T) {
	t.Parallel()

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	f.requests = make(chan diagramRequest)
	cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "A"})
	require.True(t, handled)
	require.Equal(t, util.NewWarnMsg("Diagram renderer is busy; try again"), cmd())
}

func TestGraphicsFeatureWontCopyAnImageOverSSH(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"SSH_CONNECTION", "SSH_TTY"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			caps := kittyCaps()
			caps.Env = uv.Environ{name + "=x"}
			f := newIdleGraphicsFeature(&fakeDiagramHost{}, caps)
			cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "A"})
			require.True(t, handled)
			require.Equal(t, util.NewWarnMsg("Can't copy a diagram image over SSH; c/y copies its source"), cmd())
			require.Empty(t, f.requests, "nothing renders")
		})
	}
}

func TestGraphicsFeatureWontRenderForAClipboardItCantUse(t *testing.T) {
	t.Parallel()

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	f.clipboardReady = func() error { return errors.New("no clipboard tool") }
	cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "A"})
	require.True(t, handled)
	require.Equal(t, util.NewErrorMsg(errors.New("copy diagram image: no clipboard tool")), cmd())
	require.Empty(t, f.requests, "nothing renders")
}

// Not parallel: it swaps the default logger.
func TestGraphicsFeatureLogsAFailedCopy(t *testing.T) {
	rec := &warnRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f := newIdleGraphicsFeature(&fakeDiagramHost{}, kittyCaps())
	f.HandleMsg(diagramCopiedMsg{what: diagramCopy, err: errors.New("no clipboard")})
	require.Equal(t, []string{"Failed to copy mermaid diagram image"}, rec.warns)
}
