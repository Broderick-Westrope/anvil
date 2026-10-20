package mermaid

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	res, err := r.Render(sampleFlowchart, Options{MaxWidth: 800})
	require.NoError(t, err)
	t.Logf("first render: %s (%vx%v)", time.Since(start), res.Width, res.Height)

	start = time.Now()
	_, err = r.Render(sampleFlowchart, Options{MaxWidth: 800})
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
	r.afterFunc = func(_ time.Duration, fire func()) *time.Timer {
		fire()
		return time.AfterFunc(time.Hour, func() {})
	}
	_, err = r.Render(sampleFlowchart, Options{})
	require.ErrorIs(t, err, ErrRenderTimeout)

	r.afterFunc = time.AfterFunc

	_, err = r.Render(sampleFlowchart, Options{})
	require.NoError(t, err, "the renderer still works after an interrupted render")
}

func TestRenderWaitsForALateDeadlineBeforeTheNextRender(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	// The deadline fires after layout finishes but before Stop.
	var pending func()
	r.afterFunc = func(_ time.Duration, fire func()) *time.Timer {
		pending = fire
		timer := time.AfterFunc(time.Hour, func() {})
		timer.Stop()
		go func() { time.Sleep(10 * time.Millisecond); pending() }()
		return timer
	}
	_, _ = r.Render(sampleFlowchart, Options{})
	r.afterFunc = time.AfterFunc
	// Long enough for an unawaited interrupt to land while idle, where it
	// would stop the next render.
	time.Sleep(50 * time.Millisecond)
	_, err = r.Render(sampleFlowchart, Options{})
	require.NoError(t, err, "the late interrupt doesn't stop the next render")
}

func TestRenderRefusesHugeSources(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	huge := "flowchart TD\n" + strings.Repeat("  A --> B\n", MaxSourceBytes/10+1)
	_, err = r.Render(huge, Options{})
	require.ErrorIs(t, err, ErrSourceTooLarge)
	_, err = r.Render(huge[:MaxSourceBytes], Options{})
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
		res, err := r.Render(src, Options{})
		require.NoError(t, err, name)
		require.Contains(t, res.SVG, "pele-shape-imageSquare", "%s: Pele parsed an image node", name)
		require.False(t, strings.Contains(res.SVG, "<image"), "%s: SVG has an <image>", name)
	}
}
