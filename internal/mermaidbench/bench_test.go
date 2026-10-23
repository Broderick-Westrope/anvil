// Package mermaidbench measures the Mermaid pipeline's latency and memory
// through its exported API only, so a build before a change to the pipeline
// and a build after it differ only in the import below. It prints Go
// benchmark lines for benchstat. Every test skips unless PELE_CORPUS names
// pele-go's oracle corpus (internal/oracle/testdata/corpus).
//
// Compare two builds with compare.sh, which runs them interleaved on one
// machine. TestMemory must run in a process of its own.
package mermaidbench_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	pipeline "github.com/Broderick-Westrope/pele-go/raster"
	"github.com/stretchr/testify/require"
)

const (
	// maxWidth is what ADR 0006 measured layout at.
	maxWidth = 900
	// scale is twice the layout size, as a HiDPI chat or a copy draws it.
	scale  = 2
	passes = 5
	iters  = 20
)

// memSource is the diagram ADR 0006 measured memory with.
const memSource = "info\ntitle Memory check"

var theme = pipeline.Theme{
	"pele-bg":      "#101010",
	"pele-fg":      "#f0f0f0",
	"pele-line":    "#c0c0c0",
	"pele-surface": "#303030",
	"pele-border":  "#a080f0",
	"pele-accent":  "#7090f0",
}

// samples are internal/mermaid's sample diagrams before the switch, one of
// each type Anvil's tests draw.
var samples = map[string]string{
	"flowchart": `flowchart TD
  A[Mermaid text] --> B{Flowchart?}
  B -- Yes --> C[Pele]
  B -- No --> D[Mermaid]
  C --> E([SVG])
  D --> E`,
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

func corpusDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("PELE_CORPUS")
	if dir == "" {
		t.Skip("PELE_CORPUS is unset; set it to pele-go's internal/oracle/testdata/corpus")
	}
	return dir
}

// cacheDir is the compilation cache to load resvg from, MERMAIDBENCH_CACHE
// when it's set so that runs of two builds can share one, and warmed so
// that every load times a cache hit.
func cacheDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MERMAIDBENCH_CACHE")
	if dir == "" {
		dir = t.TempDir()
	}
	r, err := pipeline.NewRasterizer(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return dir
}

func loadCorpus(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no corpus files in %s", dir)
	slices.Sort(files)
	var sources []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		// An entry is a source or an object holding one in "src".
		var entries []json.RawMessage
		require.NoError(t, json.Unmarshal(data, &entries), f)
		for _, e := range entries {
			var src string
			if json.Unmarshal(e, &src) != nil {
				var obj struct{ Src string }
				require.NoError(t, json.Unmarshal(e, &obj), f)
				src = obj.Src
			}
			sources = append(sources, src)
		}
	}
	return sources
}

func benchLine(name string, d time.Duration) {
	fmt.Printf("Benchmark%s 1 %d ns/op\n", name, d.Nanoseconds())
}

func percentile(ds []time.Duration, p float64) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[min(len(s)-1, int(p*float64(len(s))))]
}

// TestLatency times layout over the corpus, loading resvg from a warm
// cache, and rasterizing each sample diagram.
func TestLatency(t *testing.T) {
	sources := loadCorpus(t, corpusDir(t))
	cache := cacheDir(t)
	ctx := t.Context()

	r, err := pipeline.NewRenderer()
	require.NoError(t, err)
	// The warm-up pass records which sources fail, so two builds can be
	// checked to fail the same ones the same way.
	var failures []string
	for i, src := range sources {
		if _, err := r.Render(ctx, src, pipeline.Options{MaxWidth: maxWidth}); err != nil {
			failures = append(failures, fmt.Sprintf("%d: %v", i, err))
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(failures, "\n")))
	fmt.Printf("# corpus %d sources, %d fail, failures sha256 %x\n", len(sources), len(failures), sum)

	var layout []time.Duration
	for range passes {
		for _, src := range sources {
			start := time.Now()
			_, _ = r.Render(ctx, src, pipeline.Options{MaxWidth: maxWidth})
			layout = append(layout, time.Since(start))
		}
	}
	benchLine("SourceToSVG/median", percentile(layout, 0.5))
	benchLine("SourceToSVG/p99", percentile(layout, 0.99))

	loads := make([]time.Duration, iters)
	for i := range loads {
		start := time.Now()
		rast, err := pipeline.NewRasterizer(ctx, cache)
		loads[i] = time.Since(start)
		require.NoError(t, err)
		require.NoError(t, rast.Close())
	}
	benchLine("NewRasterizer", percentile(loads, 0.5))

	rast, err := pipeline.NewRasterizer(ctx, cache)
	require.NoError(t, err)
	defer rast.Close()
	diagrams := map[string]string{"info": memSource}
	maps.Copy(diagrams, samples)
	for _, name := range slices.Sorted(maps.Keys(diagrams)) {
		res, err := r.Render(ctx, diagrams[name], pipeline.Options{MaxWidth: maxWidth})
		require.NoError(t, err, name)
		times := make([]time.Duration, iters)
		for i := range times {
			start := time.Now()
			_, err := rast.Rasterize(res, theme, pipeline.RasterOptions{Scale: scale})
			times[i] = time.Since(start)
			require.NoError(t, err, name)
		}
		benchLine("Rasterize/"+name, percentile(times, 0.5))
	}
}

func rssBytes(t *testing.T) int64 {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	require.NoError(t, err)
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	require.NoError(t, err)
	return kb << 10
}

func settle() runtime.MemStats {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m
}

// TestMemory follows ADR 0006: render and rasterize info 20 times with
// resvg loaded from a warm cache, keep the last image, and compare the
// settled process against its start.
func TestMemory(t *testing.T) {
	corpusDir(t)
	cache := cacheDir(t)
	base, baseRSS := settle(), rssBytes(t)

	r, err := pipeline.NewRenderer()
	require.NoError(t, err)
	rast, err := pipeline.NewRasterizer(context.Background(), cache)
	require.NoError(t, err)
	// The chat keeps the image it shows.
	var shown *image.RGBA
	for range iters {
		res, err := r.Render(context.Background(), memSource, pipeline.Options{MaxWidth: maxWidth})
		require.NoError(t, err)
		shown, err = rast.Rasterize(res, theme, pipeline.RasterOptions{Scale: scale})
		require.NoError(t, err)
	}
	m := settle()
	fmt.Printf("BenchmarkMemory 1 %d added-rss-B %d added-heap-inuse-B\n",
		rssBytes(t)-baseRSS, int64(m.HeapInuse)-int64(base.HeapInuse))
	runtime.KeepAlive(r)
	runtime.KeepAlive(rast)
	runtime.KeepAlive(shown)
}
