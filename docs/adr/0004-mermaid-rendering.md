# 0004: Render Mermaid in-process with vendored Pele and resvg

**Status:** Accepted; running Pele in goja is superseded by
[ADR 0006](0006-pele-go.md). The resvg wrapper, the fonts and their
licences moved to pele-go's `raster` module, as described in ADR 0006.
**Date:** 2026-10-20

## Context

Agents write ```` ```mermaid ```` fences, and Anvil shows them inline as
images (`plans/completed/design-2026-10-20-mermaid-diagrams.md`). Turning
Mermaid into pixels needs a layout engine and an SVG rasterizer. Anvil
builds with `CGO_ENABLED=0` and ships as one binary, so neither can be a
native library, a browser, or a separate program the user has to install.

The spike (`plans/completed/spike-2026-10-20-mermaid-rendering.md`) found:

- Pele (MIT, a Mermaid-compatible layout library in JavaScript) runs in
  goja, which Anvil already uses for workflows, and renders a diagram in
  a few milliseconds.
- resvg rasterizes Pele's SVG once `var()`, `color-mix()` and the root
  `font:` shorthand are resolved in Go. `github.com/boolean-maybe/go-resvg`
  ships it compiled to WebAssembly with DejaVu Sans built in, run by
  wazero (pure Go).
- That Go wrapper leaked a WebAssembly instance (about 5 MB) on every GC
  and kept its runtime for the life of the process, so it couldn't be used
  as a dependency.

## Decision

- Embed Pele 1.7.0 as `internal/mermaid/pele.global.js` and run it in goja.
- Add `github.com/tetratelabs/wazero` as a dependency.
- Vendor go-resvg v0.0.2's `resvg.wasm` and rewrite its Go side in
  `internal/mermaid/resvg`, so one `Engine` owns its runtime and its single
  instance and `Close` frees all of it.
- Embed DejaVu Sans in `internal/mermaid/fonts` for measuring labels, the
  same font that is compiled into `resvg.wasm`, so labels fit their boxes.
- Ship each licence next to its files: `PELE_LICENSE` (MIT),
  `resvg/LICENSE` (Apache-2.0), `fonts/LICENSE` (Bitstream Vera).

### Upgrading

- **Pele:** see ADR 0006; Pele no longer runs in goja.
- **resvg or its fonts:** they live in pele-go's `raster` module now.
  Follow "Upgrading resvg or the fonts" and its release order in
  `raster/README.md` there, then bump
  `github.com/Broderick-Westrope/pele-go/raster` in `go.mod`.

## Alternatives

- **Mermaid's own renderer in goja.** It lays out with a browser DOM,
  which goja doesn't have.
- **Shelling out to `mmdc` (mermaid-cli) or a headless browser.** It needs
  Node and a browser on the user's machine.
- **go-resvg as a dependency.** The leak above, and no way to free its
  memory when diagrams go idle.
- Pure-Go SVG rasterizers weren't evaluated; resvg worked once its gaps
  were patched in Go.

## Consequences

- The binary grows by about 7.6 MB. Loading the engines peaks at about
  95 MB of process memory and settles near 30 MB. They load on the first
  diagram and are dropped after 20 seconds idle, and nothing loads when
  `options.tui.diagrams` is off.
- Compiling resvg takes about 300 ms and half of that peak, so the
  compiled code is kept in wazero's compilation cache in the user cache
  directory, where every Anvil process loads it in about 11 ms. Pele
  (about 48 MB at load) is the peak after that. The cache holds native
  code, so it never falls back to a shared temp directory, and an unusable
  or corrupt cache compiles without it. Each wazero or `resvg.wasm`
  upgrade leaves about 5 MB of old entries behind.
- We own the resvg Go wrapper and its ABI assumptions. A new resvg build
  needs the steps above, not a `go get`.
- Pele implements a subset of Mermaid. Diagrams it rejects fall back to
  their code block with a note saying why.
