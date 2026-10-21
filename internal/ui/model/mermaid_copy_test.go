package model

import (
	"bytes"
	"errors"
	"image/png"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/stretchr/testify/require"
)

func TestMermaidFeatureCopiesADiagramAsAPNG(t *testing.T) {
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

			f := newTestMermaidFeature(t, &fakeDiagramHost{}, tc.caps)
			cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "flowchart LR\n A --> B\n"})
			require.True(t, handled)
			require.Equal(t, util.NewInfoMsg("Copying diagram image"), cmd())

			copied, ok := waitResult(t, f).(diagramCopiedMsg)
			require.True(t, ok)
			require.NoError(t, copied.err)
			img, err := png.Decode(bytes.NewReader(copied.png))
			require.NoError(t, err)
			require.Greater(t, img.Bounds().Dx(), 100, "drawn at more than the layout's size")
			require.Empty(t, f.entries, "copying caches nothing")

			r, g, b, a := img.At(0, 0).RGBA()
			require.Equal(t, uint32(0xffff), a, "opaque, so it reads on any background")
			require.Equal(t, f.theme()["pele-bg"], hexColor(rgba{r, g, b}))
		})
	}
}

// rgba is a colour from RGBA's 16-bit channels.
type rgba struct{ r, g, b uint32 }

func (c rgba) RGBA() (uint32, uint32, uint32, uint32) { return c.r, c.g, c.b, 0xffff }

func TestMermaidFeatureWritesACopiedImageToTheClipboard(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	var written [][]byte
	f.writeImage = func(png []byte) error {
		written = append(written, png)
		return nil
	}
	cmd, handled := f.HandleMsg(diagramCopiedMsg{png: []byte("PNG")})
	require.True(t, handled)
	require.Equal(t, util.NewInfoMsg("Diagram image copied to clipboard"), browserCmdMsg(t, cmd))
	require.Equal(t, [][]byte{[]byte("PNG")}, written)
}

func TestMermaidFeatureReportsAFailedCopy(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		msg   diagramCopiedMsg
		write error
		want  string
	}{
		"render fails":    {msg: diagramCopiedMsg{err: errors.New("bad diagram")}, want: "copy diagram image: bad diagram"},
		"clipboard fails": {msg: diagramCopiedMsg{png: []byte("PNG")}, write: errors.New("no clipboard"), want: "copy diagram image: no clipboard"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
			f.writeImage = func([]byte) error {
				if tc.write == nil {
					t.Fatal("a failed render reached the clipboard")
				}
				return tc.write
			}
			cmd, handled := f.HandleMsg(tc.msg)
			require.True(t, handled)
			require.Equal(t, util.NewErrorMsg(errors.New(tc.want)), browserCmdMsg(t, cmd))
		})
	}
}

func TestMermaidFeatureWarnsWhenTooBusyToCopy(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	f.requests = make(chan diagramRequest)
	cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "A"})
	require.True(t, handled)
	require.Equal(t, util.NewWarnMsg("Diagram renderer is busy; try again"), cmd())
}
