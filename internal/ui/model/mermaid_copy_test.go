package model

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"math"
	"sync"
	"testing"

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

			corner := img.At(0, 0)
			_, _, _, a := corner.RGBA()
			require.Equal(t, uint32(0xffff), a, "opaque, so it reads on any background")
			require.Equal(t, f.theme()["pele-bg"], hexColor(corner))
		})
	}
}

func TestMermaidFeatureCopiesInTheOrderAsked(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
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

func TestMermaidFeatureReportsACopiedImage(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	cmd, handled := f.HandleMsg(diagramCopiedMsg{})
	require.True(t, handled)
	require.Equal(t, util.NewInfoMsg("Diagram image copied to clipboard"), browserCmdMsg(t, cmd))
}

func TestMermaidFeatureReportsAFailedCopy(t *testing.T) {
	t.Parallel()

	f := newIdleMermaidFeature(&fakeDiagramHost{}, kittyCaps())
	cmd, handled := f.HandleMsg(diagramCopiedMsg{err: errors.New("no clipboard")})
	require.True(t, handled)
	require.Equal(t, util.NewErrorMsg(errors.New("copy diagram image: no clipboard")), browserCmdMsg(t, cmd))
}

func TestMermaidFeatureReportsAClipboardThatWontTakeTheImage(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	clip := &clipboardRecorder{err: errors.New("clipboard is busy")}
	f.writeImage = clip.write
	f.HandleMsg(chat.DiagramCopyImageMsg{Source: "flowchart LR\n A --> B\n"})
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	require.EqualError(t, copied.err, "clipboard is busy")
}

func TestMermaidFeatureReportsADiagramThatWontRenderForCopying(t *testing.T) {
	t.Parallel()

	f := newTestMermaidFeature(t, &fakeDiagramHost{}, kittyCaps())
	clip := &clipboardRecorder{}
	f.writeImage = clip.write
	f.HandleMsg(chat.DiagramCopyImageMsg{Source: "notADiagram at all\n"})
	copied, ok := waitResult(t, f).(diagramCopiedMsg)
	require.True(t, ok)
	require.ErrorContains(t, copied.err, "No diagram type detected")
	require.Empty(t, clip.images(), "nothing reaches the clipboard")
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
