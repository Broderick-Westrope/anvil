package chat

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
)

// buildOpenFenceDoc generates a document that ends inside an open code
// fence: a short prose preamble followed by a fence that never closes.
// While the fence is open, findBoundaryAfter can never find a safe
// boundary past the fence start, so every streaming flush falls back
// to a full glamour render of the entire accumulated document
// (streaming_markdown.go Render, boundary < 0 branch).
func buildOpenFenceDoc(codeLines int) string {
	var b strings.Builder
	b.WriteString("Here is the review of the diff. The change looks mostly fine but consider the following code:\n\n")
	b.WriteString("```go\n")
	for i := range codeLines {
		fmt.Fprintf(&b, "func process%d(ctx context.Context, in *Input) (*Output, error) { return nil, errTodo }\n", i)
	}
	return b.String()
}

// BenchmarkStreamingOpenFence measures the allocation cost of one
// streaming flush while a code fence is open, at several accumulated
// document sizes. This reproduces the memory blowup observed when
// reviewer subagents stream long fenced code: with a 33ms flush
// debounce, per-flush allocations of tens of MB translate to GB/s of
// garbage across concurrent streams.
func BenchmarkStreamingOpenFence(b *testing.B) {
	sty := styles.TokyoNight()
	width := 120

	for _, codeLines := range []int{50, 200, 800} {
		doc := buildOpenFenceDoc(codeLines)
		b.Run(fmt.Sprintf("docKB=%d", len(doc)/1024), func(b *testing.B) {
			renderer, err := glamour.NewTermRenderer(
				glamour.WithStyles(sty.Markdown),
				glamour.WithWordWrap(width),
			)
			if err != nil {
				b.Fatal(err)
			}
			var sm streamingMarkdown
			// Seed the cache with the prose preamble so we measure the
			// steady-state fence-open path, not the cold start.
			_ = sm.Render(doc, width, renderer)

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				// One more delta arrives inside the open fence; the
				// whole document is re-rendered.
				_ = sm.Render(doc+"x := 1\n", width, renderer)
			}
		})
	}
}
