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

func TestToolDetailOutputImageStates(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	tests := map[string]struct {
		view    DiagramView
		want    []string
		wantNot []string
	}{
		"pending says it is loading": {
			view:    DiagramView{State: DiagramPending},
			want:    []string{"Loading image"},
			wantNot: []string{"Loaded Image"},
		},
		"unsupported keeps the line and says why": {
			view: DiagramView{State: DiagramUnsupported},
			want: []string{"Loaded Image", "image/png", "This terminal can't display images"},
		},
		"failed keeps the line and says why": {
			view:    DiagramView{State: DiagramFailed, Err: "unsupported image type image/svg+xml"},
			want:    []string{"Loaded Image", "Can't display: unsupported image type image/svg+xml"},
			wantNot: []string{"Loading image"},
		},
		"ready shows only the image": {
			view:    DiagramView{State: DiagramReady, Image: "[[IMAGE]]"},
			want:    []string{"[[IMAGE]]"},
			wantNot: []string{"Loaded Image", "Loading image"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)
			out := ansi.Strip(outputItem(t, BuildToolDetailItems(&sty, source, &fakeImages{view: tc.view})).Render(80))
			for _, want := range tc.want {
				require.Contains(t, out, want)
			}
			for _, not := range tc.wantNot {
				require.NotContains(t, out, not)
			}
		})
	}
}

func TestToolDetailOutputWithAnImageRerendersWhenImagesChange(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, imageResult(), ToolStatusSuccess)
	out := outputItem(t, BuildToolDetailItems(&sty, source, &fakeImages{}))

	require.Implements(t, (*DiagramShower)(nil), out)
	require.False(t, out.Finished(), "the list must not freeze it before its image arrives")
	before := out.Version()
	ClearItemCaches([]MessageItem{out})
	require.Greater(t, out.Version(), before)
}

func TestToolDetailOutputWithoutAnImageIsUnchanged(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	tests := map[string]struct {
		result *message.ToolResult
		images ImageRenderer
	}{
		"text result":         {result: &message.ToolResult{Content: "hello"}, images: &fakeImages{}},
		"no renderer":         {result: imageResult(), images: nil},
		"non-image data":      {result: &message.ToolResult{Content: "x", Data: "aGVsbG8=", MIMEType: "application/pdf"}, images: &fakeImages{}},
		"image type, no data": {result: &message.ToolResult{Content: "x", MIMEType: "image/png"}, images: &fakeImages{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := helperToolItem(t, "view", `{"file_path":"/tmp/a.png"}`, tc.result, ToolStatusSuccess)
			out := outputItem(t, BuildToolDetailItems(&sty, source, tc.images))
			require.True(t, out.Finished())
			require.NotContains(t, ansi.Strip(out.Render(80)), "open in viewer")
		})
	}
}
