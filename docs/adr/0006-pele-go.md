# 0006: Lay Mermaid out with pele-go instead of Pele in goja

**Status:** Accepted
**Date:** 2026-10-21
**Supersedes:** the Pele/goja part of ADR 0004. Its resvg, font and
licence decisions stand.

## Context

ADR 0004 ran Pele 1.7.0 (`pele.global.js`, 683 KB) in goja. Profiling put
Pele in goja at about 48 MB at load, settling to about 25 MB: the largest
share of what Anvil adds while diagrams are rendering, even with resvg's
compilation cache, which can't reduce it. People run many Anvil instances
at once (ADR 0005), so that cost is paid per instance.

`github.com/Broderick-Westrope/pele-go` is a pure-Go port of Pele 1.7.0
covering all 33 diagram types. It is checked byte for byte against
upstream (SVG, size, links and errors for the same source, options and
text measurer), has no dependencies outside the standard library in its
root package, and builds with `CGO_ENABLED=0`. Its `fontmeasure` package
is Anvil's old `measure.go` generalised.

## Decision

- Render with `pele.Render` and the `all` package, which registers every
  diagram type. Pass the options Anvil passed to Pele before (responsive
  off, images off, `maxWidth` always set), with a `fontmeasure` measurer
  over the DejaVu Sans faces already embedded in `internal/mermaid/fonts`.
- A context deadline (`renderTimeout`, 5 s) replaces goja's interrupt and
  its timer. pele-go checks the context between phases and inside layout
  loops, and a cancelled render leaves no goroutine behind. A deadline,
  whether ours or the caller's, is still reported as `ErrRenderTimeout`.
- Delete `pele.global.js`, `PELE_LICENSE` and `measure.go`. pele-go carries
  Pele's MIT notice. goja stays in `go.mod` for workflows.
- The idle teardown stays, for resvg. The renderer now holds only the
  parsed fonts, whose data stays in the binary.
- `go.mod` requires pele-go `v0.1.0`, its first tagged release.
- Later, the pipeline (`Renderer`, `Rasterizer`, `Theme`, the fonts and
  the resvg wrapper) moved out of `internal/mermaid` into pele-go's nested
  module `github.com/Broderick-Westrope/pele-go/raster`, so other programs
  can import it. Its API is the one Anvil used, unchanged: layout and
  rasterizing stay separate, and Anvil still owns the `Rasterizer`'s
  lifecycle. `internal/mermaid` keeps only `HTMLDocument`, whose page
  names the font family `Anvil DejaVu Sans`.

### Upgrading

- **Pele:** bump `github.com/Broderick-Westrope/pele-go/raster` to the
  release that requires the pele-go release porting the new Pele version.
  Run `go test ./internal/mermaid/...` here and `go test .` in pele-go's
  `raster/`, at the old raster version and then the new one, and compare
  the output from `MERMAID_TEST_OUT=<dir>`.
  Gantt marks the current time, so make the two runs back to back. Build
  both sides with the same Go toolchain (`GOTOOLCHAIN=<Anvil's go.mod
  version>` in pele-go's `raster/`): `image/png` compresses differently
  between Go releases, so PNGs with identical pixels can differ in bytes.
  `internal/mermaidbench/compare.sh` compares latency and memory between
  the two builds.
- **resvg or its fonts:** see `raster/README.md` in pele-go.

## Alternatives

- **Keep Pele in goja.** The memory above, which nothing else could cut.
- **Port only the common diagram types and fall back to the code block,
  or to goja, for the rest.** Lower fidelity, or the memory comes back and
  there are two code paths to keep.
- **Port inside Anvil (`internal/`).** Other Go programs couldn't use it,
  and its parity oracle would weigh on Anvil's tests.

## Consequences

Measured on an Apple Silicon Mac with Go 1.27.2, before and after the
switch, in separate processes:

| | Pele in goja | pele-go |
| --- | --- | --- |
| RSS added while rendering (4 runs) | 65–68 MB | 21–22 MB |
| Go heap in use added | 29–30 MB | 6 MB |
| Source to SVG, median (3 runs) | 529–569 µs | 34–39 µs |
| Source to SVG, p99 (3 runs) | 7.5–20.6 ms | 0.58–0.71 ms |

- Memory follows pele-go's `scripts/anvil-memory` method, which
  `internal/mermaidbench` now reimplements: render and
  rasterize `info` 20 times with resvg loaded from a warm compilation
  cache, keep the last image, and compare the settled process against its
  start. What's left is resvg and the image.
- Latency covers every source in pele-go's oracle corpus
  (`internal/oracle/testdata/corpus`, 761 sources, 59 of which both
  reject), at `MaxWidth` 900, five passes after a warm-up pass.
- The images and pages `MERMAID_TEST_OUT` writes are byte-identical
  before and after, run back to back.
- Render errors now read as pele-go's messages (`pele render: <message>`)
  rather than goja's wrapped JavaScript errors.
- The binary grows by about 5.9 MB (140.9 MB to 146.8 MB): the compiled
  port of all 33 types outweighs the 683 KB bundle it replaces. Importing
  single diagram types instead of `all` would trim it, at the cost of
  showing the rest as code blocks.
