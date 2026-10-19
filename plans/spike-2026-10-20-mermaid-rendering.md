# Spike: rendering Mermaid diagrams inline

Branch `spike/mermaid-render`. Throwaway code to answer whether Anvil can
turn the Mermaid fences agents write into images in the chat.

## Answer

Yes. The pipeline works end to end in pure Go (CGO off):

```mermaid
flowchart LR
  md[finished assistant message] --> split[split at closed mermaid fences]
  split --> pele[Pele in goja]
  pele --> fix[resolve var and color-mix]
  fix --> resvg[resvg wasm in wazero]
  resvg --> kitty[Kitty placeholders]
  kitty --> chat[chat item]
```

## What's in the branch

- `internal/mermaid`: Pele 1.7.0 (`pele.global.js`, MIT) runs in goja.
  It measures text with DejaVu Sans read via `x/image/font/sfnt`. resvg 0.44
  rasterizes, through `github.com/boolean-maybe/go-resvg` (Apache-2.0,
  wazero, DejaVu Sans built in).
- `internal/ui/model/mermaid.go`: a feature that renders on one worker
  goroutine and sends the image to the terminal with the existing
  `internal/ui/image` Kitty path. Once the terminal has it, the feature
  invalidates the chat.
- `internal/ui/chat/diagrams.go`: when a finished assistant message has a
  diagram ready, it is rendered in segments with the image in place of the
  fence. Any other case keeps the normal render, so the code block stays.

## Measurements (M-series Mac)

| Step | Cost |
| --- | --- |
| Load Pele into goja | ~80 ms, once |
| Compile resvg wasm | ~290 ms, once, on the first diagram |
| Pele render | 0.6–2.5 ms |
| Rasterize at 2x | 6–30 ms |
| Binary size | +7.6 MB (136.1 to 143.7 MB) |

## Problems found and how the spike handles them

- **resvg ignores CSS `var()`.** Pele uses it for every colour.
  `ResolveVars` substitutes them from a `Theme` map before rasterizing.
  Because the colours are fixed at render time, that is also where Anvil's
  theming happens.
- **resvg ignores the root `font:` shorthand,** so every label fell back to
  12px. The spike adds an explicit `font-size` to the root element.
- **resvg can't parse `color-mix(in srgb, ...)`,** which Pele uses for
  nested cluster titles. The spike evaluates it in Go.
- **Translucent subgraph fills** picked up white from the transparent canvas.
  The spike paints the theme background behind the diagram first.
- **Pele's default width table made labels overflow their boxes** in
  DejaVu. Passing a `measurer` backed by the same font fixes it.
- **Herdr stretches Unicode-placeholder images to their grid**
  (herdrdev/herdr#4858). The spike pads each image to whole cells so the
  aspect ratio survives.

## Not verified

- **Seeing the image in a real Kitty-capable terminal.** The terminal MCP's
  emulator can't draw images. It confirmed the fallback: without Kitty
  support the code block renders as before. A model test checks the Kitty
  path with faked capabilities, but it only goes as far as the placeholder
  output. Run `/tmp/anvil-spike` in Ghostty and inside Herdr and ask for a
  diagram.
- **Behaviour on resize** (it should re-render per width; the cache key
  includes width), scrolling, compact mode and selection over an image.

## If this goes ahead

- Pass the renderer through the item constructors instead of the
  package-level `chat.Diagrams` hook.
- Build the theme from `styles.Styles` instead of the hard-coded TokyoNight
  map.
- Cap and evict the per-diagram cache. The image package's cache is
  global and is only cleared on session switch.
- Decide what happens with large diagrams. They are currently scaled down to
  40 rows. A drill-in at full size or "open SVG in browser" may be better.
- Reconsider the `go-resvg` dependency. It is young (v0.0.2) and its fonts
  are fixed at build time. Forking it to supply our own fonts, or pinning a
  vendored wasm, would remove the risk.
- Check how `gocognit`, `depguard` and mutation testing treat the new
  package before building on it.
