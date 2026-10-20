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
	var deadline time.Duration
	r.afterFunc = func(d time.Duration, fire func()) *time.Timer {
		deadline = d
		fire()
		return time.AfterFunc(time.Hour, func() {})
	}
	_, err = r.Render(sampleFlowchart, Options{})
	require.Equal(t, ErrRenderTimeout, err, "the timeout is returned unwrapped")
	require.Equal(t, 5*time.Second, deadline)

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

func TestNewRendererRejectsBrokenPele(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		source, wantErr string
	}{
		"syntax error":          {source: "var (", wantErr: "compile pele"},
		"throws while loading":  {source: "throw new Error('boom')", wantErr: "load pele"},
		"no pele global":        {source: "", wantErr: "pele global not defined"},
		"undefined pele global": {source: "var pele = undefined", wantErr: "pele global not defined"},
		"render isn't callable": {source: "var pele = {render: 1}", wantErr: "pele.render is not a function"},
	}
	for name, tc := range tests {
		_, err := newRenderer(tc.source)
		require.ErrorContains(t, err, tc.wantErr, name)
	}
}

func TestRenderFitsMaxWidth(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	src := sampleDiagrams["subgraph"]
	wide, err := r.Render(src, Options{})
	require.NoError(t, err)
	narrow, err := r.Render(src, Options{MaxWidth: 300})
	require.NoError(t, err)
	require.Greater(t, wide.Width, 300.0)
	require.LessOrEqual(t, narrow.Width, 300.0)
}
