package chat

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newFenceTestRenderer(t *testing.T, width int) *glamour.TermRenderer {
	t.Helper()
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(styles.TokyoNight().Markdown),
		glamour.WithWordWrap(width),
	)
	require.NoError(t, err)
	return renderer
}

// TestStreamingOpenFence_ForceAdvanceBoundsTrail verifies the fix for
// the streaming memory blowup: while a code fence stays open, the
// stable prefix must force-advance so the freshly rendered trailing
// segment stays bounded instead of growing with the document.
func TestStreamingOpenFence_ForceAdvanceBoundsTrail(t *testing.T) {
	t.Parallel()
	width := 120
	renderer := newFenceTestRenderer(t, width)

	var sm streamingMarkdown
	var doc strings.Builder
	doc.WriteString("Reviewing the diff. Consider this code:\n\n```go\n")

	line := "func handler(w http.ResponseWriter, r *http.Request) { doSomethingReasonablyLong(w, r) }\n"
	for i := range 500 {
		fmt.Fprintf(&doc, "// line %d\n%s", i, line)
		_ = sm.Render(doc.String(), width, renderer)

		trail := len(doc.String()) - len(sm.stablePrefix)
		require.LessOrEqual(t, trail, maxUnsafeTrailBytes,
			"trailing segment must stay bounded while the fence is open (iteration %d)", i)
	}

	require.True(t, sm.forced, "long open fence must trigger a force-advance")
	require.NotEmpty(t, sm.openFence, "boundary inside an open fence must record the fence opener")
	require.Contains(t, sm.openFence, "```go")
}

// TestStreamingOpenFence_RenderFinalMatchesMonolithic verifies that
// once the stream completes, RenderFinal discards the seamed
// force-advanced output and produces the same bytes as a monolithic
// render of the full document.
func TestStreamingOpenFence_RenderFinalMatchesMonolithic(t *testing.T) {
	t.Parallel()
	width := 120
	renderer := newFenceTestRenderer(t, width)

	var doc strings.Builder
	doc.WriteString("Some prose first.\n\n```go\n")
	for i := range 800 {
		fmt.Fprintf(&doc, "value%d := compute(%d)\n", i, i)
	}
	doc.WriteString("```\n")
	content := doc.String()

	// Stream it in chunks to trigger force-advances.
	var sm streamingMarkdown
	for i := 1024; i < len(content); i += 1024 {
		_ = sm.Render(content[:i], width, renderer)
	}
	streamed := sm.Render(content, width, renderer)
	require.True(t, sm.forced, "test must exercise the force-advance path")

	final := sm.RenderFinal(content, width, renderer)
	require.False(t, sm.forced, "RenderFinal must clear the forced state")

	monoRenderer := newFenceTestRenderer(t, width)
	mono, err := monoRenderer.Render(content)
	require.NoError(t, err)
	require.Equal(t, strings.TrimSuffix(mono, "\n"), final,
		"RenderFinal must match a monolithic render")
	require.NotEqual(t, streamed, final,
		"sanity: the seamed streaming output should differ from the clean final render")
}

// TestStreamingOpenFence_ContentPreserved verifies the force-advanced
// streaming output still contains every code line (nothing is dropped,
// only split across synthetic fence blocks).
func TestStreamingOpenFence_ContentPreserved(t *testing.T) {
	t.Parallel()
	width := 200
	renderer := newFenceTestRenderer(t, width)

	var doc strings.Builder
	doc.WriteString("Prose intro.\n\n```\n")
	for i := range 300 {
		fmt.Fprintf(&doc, "UNIQUEMARKER%04d padpadpadpadpadpadpadpadpadpadpadpad\n", i)
	}

	var sm streamingMarkdown
	content := doc.String()
	var out string
	for i := 512; i < len(content); i += 512 {
		out = sm.Render(content[:i], width, renderer)
	}
	out = sm.Render(content, width, renderer)
	require.True(t, sm.forced)

	for _, i := range []int{0, 100, 150, 299} {
		require.Contains(t, out, fmt.Sprintf("UNIQUEMARKER%04d", i),
			"force-advanced output must preserve all fence content")
	}
}

// TestStreamingOpenFence_RecoversAfterFenceCloses verifies that once
// the fence closes and a real safe boundary appears, the normal
// boundary-advance path resumes and clears the synthetic fence state.
func TestStreamingOpenFence_RecoversAfterFenceCloses(t *testing.T) {
	t.Parallel()
	width := 120
	renderer := newFenceTestRenderer(t, width)

	var doc strings.Builder
	doc.WriteString("Intro.\n\n```go\n")
	for i := range 400 {
		fmt.Fprintf(&doc, "someVariable%d := computeTheValueCarefully(%d, opts)\n", i, i)
	}

	var sm streamingMarkdown
	content := doc.String()
	for i := 1024; i < len(content); i += 1024 {
		_ = sm.Render(content[:i], width, renderer)
	}
	_ = sm.Render(content, width, renderer)
	require.True(t, sm.forced)
	require.NotEmpty(t, sm.openFence)

	// Close the fence and add a paragraph followed by more prose so a
	// real safe boundary exists past the fence.
	closed := content + "```\n\nThe fence is now closed.\n\nMore prose follows here.\n"
	_ = sm.Render(closed, width, renderer)
	require.Empty(t, sm.openFence, "a real safe boundary must clear the synthetic fence state")
	require.Equal(t, 0, sm.baseFenceCount%2, "fence parity at a safe boundary must be even")
}

// TestClosingFence covers the synthetic closer construction.
func TestClosingFence(t *testing.T) {
	t.Parallel()
	require.Equal(t, "```", closingFence("```go"))
	require.Equal(t, "````", closingFence("````markdown"))
	require.Equal(t, "~~~", closingFence("~~~python"))
	require.Equal(t, "```", closingFence("   ```go"))
	require.Equal(t, "```", closingFence("not a fence"))
}

// TestStreamingOpenFence_GiantLineBounded verifies that a single
// giant line (minified JSON, base64) inside an open fence cannot
// defeat the trail bound: force-advance falls back to a raw byte
// offset when no newline exists after the stable prefix.
func TestStreamingOpenFence_GiantLineBounded(t *testing.T) {
	t.Parallel()
	width := 120
	renderer := newFenceTestRenderer(t, width)

	var sm streamingMarkdown
	doc := "Intro prose.\n\n```json\n" + strings.Repeat(`{"key":"value","n":12345},`, 200)
	for i := range 20 {
		doc += strings.Repeat(`{"more":"data"},`, 100)
		_ = sm.Render(doc, width, renderer)

		trail := len(doc) - len(sm.stablePrefix)
		require.LessOrEqual(t, trail, maxUnsafeTrailBytes,
			"trailing segment must stay bounded for a single giant line (iteration %d)", i)
	}
	require.True(t, sm.forced)
}

// TestRenderFinal_DropsCacheWithoutReseeding verifies RenderFinal
// leaves no streaming cache state behind: the stream is complete, so
// re-seeding (which costs extra glamour renders) must not happen.
func TestRenderFinal_DropsCacheWithoutReseeding(t *testing.T) {
	t.Parallel()
	width := 120
	renderer := newFenceTestRenderer(t, width)

	var sm streamingMarkdown
	content := "Paragraph one.\n\n```go\n" + strings.Repeat("x := compute()\n", 800)
	_ = sm.Render(content, width, renderer)
	require.True(t, sm.forced)

	_ = sm.RenderFinal(content+"```\n", width, renderer)
	require.False(t, sm.forced, "RenderFinal must clear the forced state")
	require.Empty(t, sm.stablePrefix, "RenderFinal must not re-seed the streaming cache")
	require.Empty(t, sm.openFence)
}
