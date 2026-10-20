package mermaid

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testTheme is a dark palette for tests. Anvil's real one comes from
// internal/ui/styles.
var testTheme = Theme{
	"pele-bg":      "#101010",
	"pele-fg":      "#f0f0f0",
	"pele-line":    "#c0c0c0",
	"pele-surface": "#303030",
	"pele-border":  "#a080f0",
	"pele-accent":  "#7090f0",
}

var sampleDiagrams = map[string]string{
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
  section Build
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
	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	t.Logf("resvg init: %s", time.Since(start))

	out := os.Getenv("MERMAID_TEST_OUT")
	for name, src := range sampleDiagrams {
		start := time.Now()
		res, err := r.Render(src, Options{MaxWidth: 900})
		require.NoError(t, err, name)
		peleTime := time.Since(start)

		start = time.Now()
		img, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: 2})
		require.NoError(t, err, name)
		t.Logf("%-9s pele %8s  raster %8s  %vx%v -> %v", name, peleTime, time.Since(start), res.Width, res.Height, img.Bounds().Size())
		doc, err := HTMLDocument(res, testTheme, name)
		require.NoError(t, err, name)
		require.NotContains(t, doc, "var(", name)

		if out != "" {
			f, err := os.Create(filepath.Join(out, name+".png"))
			require.NoError(t, err)
			require.NoError(t, png.Encode(f, img))
			require.NoError(t, f.Close())
		}
	}
}

func TestRasterizeShrinksOversizedDiagramToFit(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res := Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg" width="4096" height="2048"><rect width="10" height="10"/></svg>`, Width: 4096, Height: 2048}

	img, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: 1})
	require.NoError(t, err)
	require.Equal(t, image.Pt(4096, 2048), img.Bounds().Size(), "exactly MaxPixels is kept")

	img, err = rast.Rasterize(res, testTheme, RasterOptions{Scale: 1.5})
	require.NoError(t, err)
	size := img.Bounds().Size()
	require.LessOrEqual(t, size.X*size.Y, MaxPixels)
	require.Greater(t, size.X*size.Y, MaxPixels*99/100, "it shrinks only as far as it must")
}

// Callers fit a diagram to a pixel budget with sqrt(limit/area), which
// rounds up past the limit once each side is rounded to whole pixels.
func TestRasterizeFitsCallerPixelBudget(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res := Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg" width="120.3" height="830.7"><rect width="10" height="10"/></svg>`, Width: 120.3, Height: 830.7}
	const limit = 100_000
	scale := math.Sqrt(limit / (res.Width * res.Height))

	img, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: scale, MaxPixels: limit})
	require.NoError(t, err)
	size := img.Bounds().Size()
	require.LessOrEqual(t, size.X*size.Y, limit)
}

func TestRasterizeHugeScaleFitsThePixelCap(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res := Result{SVG: `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><rect width="10" height="10"/></svg>`, Width: 40, Height: 20}

	img, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: 1e300, MaxPixels: 10_000})
	require.NoError(t, err)
	size := img.Bounds().Size()
	require.LessOrEqual(t, size.X*size.Y, 10_000)
}

func TestRasterizeRejectsUnusableSize(t *testing.T) {
	t.Parallel()

	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><rect width="10" height="10"/></svg>`

	tests := map[string]struct {
		width, height, scale float64
	}{
		"zero size":      {width: 0, height: 0, scale: 1},
		"zero width":     {width: 0, height: 20, scale: 1},
		"zero height":    {width: 40, height: 0, scale: 1},
		"negative scale": {width: 40, height: 20, scale: -1},
		"NaN scale":      {width: 40, height: 20, scale: math.NaN()},
		"infinite scale": {width: 40, height: 20, scale: math.Inf(1)},
		"infinite width": {width: math.Inf(1), height: 20, scale: 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// A Rasterizer is not safe for concurrent use.
			rast, err := NewRasterizer(t.Context(), "")
			require.NoError(t, err)
			t.Cleanup(func() { _ = rast.Close() })
			_, err = rast.Rasterize(Result{SVG: svg, Width: tc.width, Height: tc.height}, testTheme, RasterOptions{Scale: tc.scale})
			require.ErrorContains(t, err, "rasterize: unusable size")
		})
	}
}

func TestRasterizeTransparentBackground(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res, err := r.Render(sampleFlowchart, Options{})
	require.NoError(t, err)

	clearImg, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: 1})
	require.NoError(t, err)
	_, _, _, a := clearImg.At(0, 0).RGBA()
	require.Zero(t, a, "the corner is transparent")
}

// explodingVarsSVG declares a chain of variables that each use the next
// twice, so substituting them doubles at every link. Twenty links expand
// to about 4 MB, past the 1 MiB cap but small enough that a test run with
// the cap broken (as mutation testing does) doesn't exhaust memory.
var explodingVarsSVG = func() string {
	const links = 20
	var decls strings.Builder
	for i := range links {
		fmt.Fprintf(&decls, "--v%d:var(--v%d)var(--v%d);", i, i+1, i+1)
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" style="%s--v%d:#fff"><rect width="10" height="10" fill="var(--v0)"/></svg>`, decls.String(), links)
}()

func TestRasterizeRejectsExplodingVariables(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })

	_, err = rast.Rasterize(Result{SVG: explodingVarsSVG, Width: 10, Height: 10}, testTheme, RasterOptions{Scale: 1})
	require.ErrorContains(t, err, "too large")
}

// A label is text, so a declaration written in one is neither collected
// nor expanded. The label is short so that code which wrongly expanded it
// still fails quickly rather than exhausting memory.
func TestRenderLabelDeclaringVariablesStaysSmall(t *testing.T) {
	t.Parallel()

	r, err := NewRenderer()
	require.NoError(t, err)
	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	res, err := r.Render(`flowchart LR
  A["--zz:var(--zz)var(--zz)"] --> B`, Options{})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, err := rast.Rasterize(res, testTheme, RasterOptions{Scale: 1})
		if err == nil {
			_, err = HTMLDocument(res, testTheme, "d")
		}
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("resolving the label's variables did not finish")
	}
}

func TestRasterizeResolvesColorMix(t *testing.T) {
	t.Parallel()

	rast, err := NewRasterizer(t.Context(), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = rast.Close() })
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10" style="--_a:#ff0000;--_bg:var(--pele-bg)"><rect width="10" height="10" fill="color-mix(in srgb,var(--_a) 50%,var(--_bg))"/></svg>`

	img, err := rast.Rasterize(Result{SVG: svg, Width: 10, Height: 10}, Theme{"pele-bg": "#0000ff"}, RasterOptions{Scale: 1})
	require.NoError(t, err)
	got := img.RGBAAt(5, 5)
	require.Equal(t, [4]uint8{0x80, 0, 0x80, 0xff}, [4]uint8{got.R, got.G, got.B, got.A})
}

func TestFitPixels(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		width, height, scale float64
		limit                int
		w, h                 int
	}{
		"scales each side":       {width: 40, height: 20, scale: 2, limit: 1e6, w: 80, h: 40},
		"rounds each side up":    {width: 10.2, height: 5.1, scale: 1, limit: 1000, w: 11, h: 6},
		"shrinks a square":       {width: 100, height: 100, scale: 1, limit: 5000, w: 70, h: 70},
		"shrinks a wide diagram": {width: 300, height: 7, scale: 1, limit: 1000, w: 187, h: 5},
		"shrinks a tall diagram": {width: 7, height: 300, scale: 1, limit: 1000, w: 5, h: 187},
		"shrinks by a little":    {width: 4096, height: 2048, scale: 1.5, limit: MaxPixels, w: 4095, h: 2048},
		// The bound is conservative: 2x1 would fit too.
		"shrinks a tiny diagram": {width: 3, height: 1, scale: 1, limit: 2, w: 1, h: 1},
	}
	for name, tc := range tests {
		w, h, err := fitPixels(tc.width, tc.height, tc.scale, tc.limit)
		require.NoError(t, err, name)
		require.Equal(t, [2]int{tc.w, tc.h}, [2]int{w, h}, name)
		require.LessOrEqual(t, w*h, tc.limit, name)
	}
}

func TestFitPixelsRejectsUnusableSize(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		width, height float64
		limit         int
	}{
		// Shrinking to fit takes the thin side below the smallest float.
		"width shrinks to zero":  {width: math.SmallestNonzeroFloat64, height: 1000, limit: 100},
		"height shrinks to zero": {width: 1000, height: math.SmallestNonzeroFloat64, limit: 100},
		// Past 2^53 the float area rounds down to the limit while the whole
		// pixel area is past it.
		"area past a huge limit": {width: 4, height: 1<<51 + 1, limit: 1<<53 + 3},
	}
	for name, tc := range tests {
		_, _, err := fitPixels(tc.width, tc.height, 1, tc.limit)
		require.ErrorContains(t, err, "unusable size", name)
	}
}

func TestResolveColorMix(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in, want string
	}{
		"weights each channel": {in: `color-mix(in srgb,#804020 25%,#4080c0)`, want: `#507098`},
		"short hex":            {in: `color-mix(in srgb, #f00 50% , #00f )`, want: `#800080`},
		"in a longer value":    {in: `url(#a) color-mix(in srgb,#ffffff 0%,#000000) x`, want: `url(#a) #000000 x`},
		"bad first colour":     {in: `color-mix(in srgb,#ffff 50%,#000000)`, want: `color-mix(in srgb,#ffff 50%,#000000)`},
		"bad second colour":    {in: `color-mix(in srgb,#000000 50%,#fffff)`, want: `color-mix(in srgb,#000000 50%,#fffff)`},
		"bad percentage":       {in: `color-mix(in srgb,#000000 1.2.3%,#ffffff)`, want: `color-mix(in srgb,#000000 1.2.3%,#ffffff)`},
		"both colours bad":     {in: `color-mix(in srgb,#ffff 50%,#fffff)`, want: `color-mix(in srgb,#ffff 50%,#fffff)`},
	}
	for name, tc := range tests {
		require.Equal(t, tc.want, resolveColorMix(tc.in), name)
	}
}
