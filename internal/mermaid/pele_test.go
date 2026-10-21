package mermaid

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pele "github.com/Broderick-Westrope/pele-go"
	"github.com/stretchr/testify/require"
)

const sampleFlowchart = `flowchart TD
  A[Mermaid text] --> B{Flowchart?}
  B -- Yes --> C[Pele]
  B -- No --> D[Mermaid]
  C --> E([SVG])
  D --> E`

func TestRenderFlowchart(t *testing.T) {
	t.Parallel()

	start := time.Now()
	r, err := NewRenderer()
	require.NoError(t, err)
	t.Logf("load: %s", time.Since(start))

	start = time.Now()
	res, err := r.Render(t.Context(), sampleFlowchart, Options{MaxWidth: 800})
	require.NoError(t, err)
	t.Logf("first render: %s (%vx%v)", time.Since(start), res.Width, res.Height)

	start = time.Now()
	_, err = r.Render(t.Context(), sampleFlowchart, Options{MaxWidth: 800})
	require.NoError(t, err)
	t.Logf("second render: %s", time.Since(start))

	require.Contains(t, res.SVG, "<svg")
	require.Contains(t, res.SVG, `class="pele pele-flowchart"`)

	if dir := os.Getenv("MERMAID_TEST_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "flowchart.svg"), []byte(res.SVG), 0o644))
	}
}

func TestRenderStopsAtItsDeadline(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	// The deadline passes before layout starts.
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	_, err = r.Render(ctx, sampleFlowchart, Options{})
	require.Equal(t, ErrRenderTimeout, err, "the timeout is returned unwrapped")
	require.Equal(t, 5*time.Second, renderTimeout)

	_, err = r.Render(t.Context(), sampleFlowchart, Options{})
	require.NoError(t, err, "the renderer still works after a render that ran out of time")
}

func TestRenderStopsWhenCanceled(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.Render(ctx, sampleFlowchart, Options{})
	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, ErrRenderTimeout, "a canceled render didn't run out of time")
}

func TestRenderRefusesHugeSources(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	huge := "flowchart TD\n" + strings.Repeat("  A --> B\n", MaxSourceBytes/10+1)
	_, err = r.Render(t.Context(), huge, Options{})
	require.ErrorIs(t, err, ErrSourceTooLarge)
	_, err = r.Render(t.Context(), huge[:MaxSourceBytes], Options{})
	require.NotErrorIs(t, err, ErrSourceTooLarge)
}

func TestRenderDropsImages(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https with port":  "https://attacker.example:443/a.png",
		"colon in query":   "https://attacker.example/a.png?u=a:b",
		"relative to page": "../secret.png",
	}
	r, err := NewRenderer()
	require.NoError(t, err)
	for name, img := range tests {
		src := "flowchart TD\n  A@{ img: \"" + img + "\", label: \"pic\" }"
		res, err := r.Render(t.Context(), src, Options{})
		require.NoError(t, err, name)
		require.Contains(t, res.SVG, "pele-shape-imageSquare", "%s: Pele parsed an image node", name)
		require.False(t, strings.Contains(res.SVG, "<image"), "%s: SVG has an <image>", name)
	}
}

func TestRenderFitsMaxWidth(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	src := sampleDiagrams["subgraph"]
	wide, err := r.Render(t.Context(), src, Options{})
	require.NoError(t, err)
	narrow, err := r.Render(t.Context(), src, Options{MaxWidth: 300})
	require.NoError(t, err)
	require.Greater(t, wide.Width, 300.0)
	require.LessOrEqual(t, narrow.Width, 300.0)
}

func TestRenderSizesLabelsForResvg(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	src := "flowchart LR\n  A[Mermaid text] --> B[SVG]"
	res, err := r.Render(t.Context(), src, Options{})
	require.NoError(t, err)
	require.NotContains(t, res.SVG, "max-width:100%", "the SVG has a fixed size, not one that scales to a page")

	arial, err := pele.Render(t.Context(), src, pele.Options{Responsive: pele.Ptr(false), MaxWidth: pele.Ptr(0.0)})
	require.NoError(t, err)
	require.NotEqual(t, arial.Width, res.Width, "labels are measured in DejaVu Sans, not Pele's default Arial table")
}
