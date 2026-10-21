package model

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"math"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mermaid"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
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
			const src = "flowchart LR\n A --> B\n"
			cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: src})
			require.True(t, handled)
			require.Equal(t, util.NewInfoMsg("Copying diagram image"), cmd())

			copied, ok := waitResult(t, f).(diagramCopiedMsg)
			require.True(t, ok)
			require.NoError(t, copied.err)
			require.Equal(t, uint64(1), copied.seq, "the first copy")
			img, err := png.Decode(bytes.NewReader(copied.png))
			require.NoError(t, err)
			pele, err := mermaid.NewRenderer()
			require.NoError(t, err)
			layout, err := pele.Render(context.Background(), src, mermaid.Options{})
			require.NoError(t, err)
			require.Equal(t, int(math.Ceil(2*layout.Width)), img.Bounds().Dx(), "twice the layout's size")
			require.Equal(t, int(math.Ceil(2*layout.Height)), img.Bounds().Dy(), "twice the layout's size")
			require.Empty(t, f.entries, "copying caches nothing")

			corner := img.At(0, 0)
			_, _, _, a := corner.RGBA()
			require.Equal(t, uint32(0xffff), a, "opaque, so it reads on any background")
			require.Equal(t, f.theme()["pele-bg"], hexColor(corner))
		})
	}
}

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

func TestMermaidFeatureWontCopyAnImageOverSSH(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"SSH_CONNECTION", "SSH_TTY"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			caps := kittyCaps()
			caps.Env = uv.Environ{name + "=x"}
			f := newIdleMermaidFeature(&fakeDiagramHost{}, caps)
			cmd, handled := f.HandleMsg(chat.DiagramCopyImageMsg{Source: "A"})
			require.True(t, handled)
			require.Equal(t, util.NewWarnMsg("Can't copy a diagram image over SSH; c/y copies its source"), cmd())
			require.Empty(t, f.requests, "nothing renders")
		})
	}
}

func TestMermaidFeatureLeavesTheNewestCopyOnTheClipboard(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		order []int
		want  [][]byte
	}{
		"in order":          {order: []int{0, 1}, want: [][]byte{[]byte("A"), []byte("B")}},
		"older one is late": {order: []int{1, 0}, want: [][]byte{[]byte("B")}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
			var written [][]byte
			f.writeImage = func(png []byte) error {
				written = append(written, png)
				return nil
			}
			var writes []tea.Cmd
			for _, source := range []string{"A", "B"} {
				f.HandleMsg(chat.DiagramCopyImageMsg{Source: source})
				req := <-f.requests
				require.Equal(t, diagramJobCopy, req.job)
				cmd, _ := f.HandleMsg(diagramCopiedMsg{png: []byte(req.source), seq: req.seq})
				writes = append(writes, cmd)
			}
			// The two writes are batched separately, so Bubble Tea may run
			// them in either order.
			for _, i := range tc.order {
				browserCmdMsg(t, writes[i])
			}
			require.Equal(t, tc.want, written)
		})
	}
}

func TestMermaidFeatureReportsADiagramThatWontRenderForCopying(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	f.writeImage = func([]byte) error { t.Fatal("a failed render reached the clipboard"); return nil }
	f.HandleMsg(chat.DiagramCopyImageMsg{Source: "notADiagram at all\n"})
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	require.ErrorContains(t, copied.err, "No diagram type detected")
	require.Nil(t, copied.png)

	cmd, _ := f.HandleMsg(copied)
	info, ok := browserCmdMsg(t, cmd).(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
}
