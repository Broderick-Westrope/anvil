package mermaid

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var spikeDiagrams = map[string]string{
	"flowchart": sampleFlowchart,
	"sequence": `sequenceDiagram
  participant U as User
  participant A as Anvil
  participant M as Model
  U->>A: prompt
  A->>M: request
  M-->>A: stream with mermaid
  A-->>U: rendered diagram`,
	"state": `stateDiagram-v2
  [*] --> Idle
  Idle --> Working: submit
  Working --> Blocked: permission
  Blocked --> Working: approve
  Working --> Idle: done`,
	"class": `classDiagram
  class Coordinator {
    +Run(ctx, prompt) error
    +IsStepRunning(id) bool
  }
  class SessionAgent
  Coordinator --> SessionAgent : owns`,
	"pie": `pie title Token spend
  "Cache read" : 62
  "Cache write" : 18
  "Input" : 12
  "Output" : 8`,
	"subgraph": `flowchart LR
  subgraph ui [UI]
    chat --> image
  end
  subgraph engine [Engine]
    goja --> pele --> resvg
  end
  chat --> goja
  resvg --> image`,
	"nested": `flowchart TB
  subgraph app [internal/app]
    subgraph agent [internal/agent]
      coordinator[Coordinator] --> session[SessionAgent]
    end
    workflowhost --> coordinator
  end
  ui[internal/ui] --> workflowhost`,
	"gantt": `gantt
  title Mermaid rendering
  dateFormat YYYY-MM-DD
  section Spike
  Pele in goja      :done, a1, 2026-10-20, 1d
  resvg via wazero  :done, a2, after a1, 1d
  section Build
  Chat integration  :active, b1, after a2, 3d
  Resize handling   :b2, after b1, 2d`,
}

func TestRasterizeDiagrams(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)

	start := time.Now()
	rast, err := NewRasterizer(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	t.Logf("resvg init: %s", time.Since(start))

	out := os.Getenv("MERMAID_SPIKE_OUT")
	for name, src := range spikeDiagrams {
		start := time.Now()
		res, err := r.Render(src, Options{MaxWidth: 900})
		require.NoError(t, err, name)
		peleTime := time.Since(start)

		start = time.Now()
		img, err := rast.Rasterize(res, TokyoNight, 2)
		require.NoError(t, err, name)
		t.Logf("%-9s pele %8s  raster %8s  %vx%v -> %v", name, peleTime, time.Since(start), res.Width, res.Height, img.Bounds().Size())
		require.NotContains(t, ResolveVars(res.SVG, TokyoNight), "var(", name)
		require.NotContains(t, resolveColorMix(ResolveVars(res.SVG, TokyoNight)), "color-mix", name)

		if out != "" {
			f, err := os.Create(filepath.Join(out, name+".png"))
			require.NoError(t, err)
			require.NoError(t, png.Encode(f, img))
			require.NoError(t, f.Close())
		}
	}
}

func TestRasterizeRejectsOversizedDiagram(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res := Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg" width="4096" height="2048"><rect width="10" height="10"/></svg>`, Width: 4096, Height: 2048}

	_, err = rast.Rasterize(res, TokyoNight, 1)
	require.NoError(t, err, "exactly MaxPixels is allowed")
	_, err = rast.Rasterize(res, TokyoNight, 1.001)
	require.ErrorContains(t, err, "exceeds")
	_, err = rast.Rasterize(Result{SVG: res.SVG}, TokyoNight, 1)
	require.ErrorContains(t, err, "empty")
}

func TestResolveVars(t *testing.T) {
	t.Parallel()

	svg := `<svg style="--_bg:var(--pele-bg,#fff);--_fg:var(--pele-fg,#000)" fill="var(--_fg)"><rect fill="var(--_bg)" stroke="var(--missing, var(--_fg))"/></svg>`
	got := ResolveVars(svg, Theme{"pele-bg": "#111"})
	require.Equal(t, `<svg style="--_bg:#111;--_fg:#000" fill="#000"><rect fill="#111" stroke="#000"/></svg>`, got)
}
