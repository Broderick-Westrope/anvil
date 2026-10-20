package mermaid

import (
	"os"
	"path/filepath"
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
	t.Logf("first render: %s (%vx%v %s)", time.Since(start), res.Width, res.Height, res.Type)

	start = time.Now()
	_, err = r.Render(sampleFlowchart, Options{MaxWidth: 800})
	require.NoError(t, err)
	t.Logf("second render: %s", time.Since(start))

	require.Contains(t, res.SVG, "<svg")
	require.Equal(t, "flowchart", res.Type)

	if dir := os.Getenv("MERMAID_TEST_OUT"); dir != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "flowchart.svg"), []byte(res.SVG), 0o644))
	}
}
