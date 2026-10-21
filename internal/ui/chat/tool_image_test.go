package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// fakeImages is an ImageRenderer that shows view and records what it was
// asked for.
type fakeImages struct {
	view     DiagramView
	data     string
	mimeType string
	width    int
}

func (f *fakeImages) FullImage(data, mimeType string, width int) DiagramView {
	f.data, f.mimeType, f.width = data, mimeType, width
	return f.view
}

func imageResult() *message.ToolResult {
	return &message.ToolResult{ToolCallID: "tc-test", Content: "Loaded image/png content", Data: "aGVsbG8=", MIMEType: "image/png"}
}

func outputItem(t *testing.T, items []MessageItem) *toolDetailOutputItem {
	t.Helper()
	for _, item := range items {
		if o, ok := item.(*toolDetailOutputItem); ok {
			return o
		}
	}
	t.Fatal("no output item")
	return nil
}

func TestToolDetailOutputDrawsAnImageResult(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)
	images := &fakeImages{view: DiagramView{State: DiagramReady, Image: "[[IMAGE]]"}}

	out := outputItem(t, BuildToolDetailItems(&sty, source, images)).Render(80)

	require.Contains(t, out, "[[IMAGE]]")
	require.Equal(t, "aGVsbG8=", images.data)
	require.Equal(t, "image/png", images.mimeType)
	require.Positive(t, images.width)
	require.Less(t, images.width, 80, "the focus bar takes some of the width")
	require.Contains(t, ansi.Strip(out), "open in viewer")
}
