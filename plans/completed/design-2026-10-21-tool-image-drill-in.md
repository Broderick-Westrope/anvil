# Tool Image Drill-In Design Spec

**Problem:** When a tool returns an image (`view` on a PNG, an MCP tool
returning a screenshot), Anvil shows only "Loaded Image → image/png 42 KB"
(`toolOutputImageContent`, `internal/ui/chat/tools.go`), both in the chat
and in the tool's drill-in. There is no way to see what the model saw
without leaving Anvil. The Mermaid work already built everything needed to
draw images in the chat with Kitty graphics, but it is tied to Mermaid.

**Goal:** Opening the drill-in of a tool result that carries image data
shows the image, drawn with Kitty graphics, as the drill-in's output. `o`
opens the exact bytes the model saw in the OS default viewer. Terminals
without Kitty graphics, and images Anvil can't decode, keep today's line
with a note saying why. The image lifecycle is shared with Mermaid diagrams
rather than duplicated.

**Scope:**

In:

- Every tool result whose `Result.Data` is set and whose `MIMEType` starts
  with `image/`: `view`, the generic renderer, and MCP renderers (the
  current callers of `toolOutputImageContent`).
- Drawing the image as the Output item of the existing tool drill-in
  (`BuildToolDetailItems`).
- `o` in that item: open the result's bytes in the OS default viewer.
- Extracting the image lifecycle out of `mermaidFeature` into a shared
  graphics cache with pluggable producers, Mermaid being one and bitmaps
  the other.

Out:

- Inline thumbnails in the chat. The chat keeps the "Loaded Image" line.
- Images in user attachments (`internal/ui/chat/user.go`).
- Block-character (non-Kitty) drawing.
- SVG, AVIF, BMP or any format beyond PNG, JPEG, GIF and WebP; GIF
  animation.
- Copying the image or its path.
- A config switch.

**Constraints:**

- Mermaid behaviour must not change: every existing test in
  `internal/ui/model/mermaid_test.go`, `chat_diagrams_test.go` and
  `internal/ui/chat/diagrams_test.go` passes, adjusted only for renamed
  types or moved code, never for changed expectations.
- Anvil holds no pixels once an image is in the terminal (as for
  diagrams). Decoding happens on the cache's worker goroutine, never during
  rendering or in `Update`.
- At most one full-size (drill-in) image exists at a time, across both
  producers, capped like the diagram drill-in: 297 rows
  (`diagramMaxFullRows`) and 16 Mpx (`diagramFullMaxPixels`), checked
  after padding to whole cells. It belongs to the top drill-in and is
  freed in the terminal when that drill-in is popped or covered by
  another (see Drill-in ownership).
- Decoding memory is bounded before decoding: `image.DecodeConfig` reads
  the dimensions first, and a source over 8192 px on either side or over
  32 Mpx (128 MB of RGBA while decoding) is not decoded. That transient
  allocation is separate from the terminal-storage budget, which counts
  only what was transmitted. The decoded source is dropped as soon as the
  fitted image is built, and the fitted image once transmitted.
- The shared cache exists even when `options.tui.diagrams` is off; that
  option gates only the Mermaid producer, message splitting and the
  system-prompt line, as today.
- Package layers, `gocognit` (30) and the mutation gate apply. No new
  `gocognit` exclusions; no map-ordered output; no capacity hints.

**Success Criteria:**

- [ ] Opening the drill-in of a `view` call on a PNG, JPEG, GIF or WebP
      shows the image under the Output section in a Kitty terminal,
      scrolling with the list when taller than the view.
- [ ] Images are drawn at one image pixel per screen pixel, shrunk (aspect
      kept) to the drill-in width, 297 rows and 16 Mpx; never enlarged.
- [ ] An animated GIF shows its first frame, decoded with `gif.Decode`
      (first frame only), not `gif.DecodeAll`.
- [ ] A source over the decode limit shows "Can't display: image too
      large (W×H)" without decoding it, and `o` still opens it. Tests
      cover a header claiming huge dimensions, an extreme aspect ratio,
      and an image whose padded size crosses the 16 Mpx cap.
- [ ] An MCP tool result with image data gets the same drill-in output.
- [ ] While decoding, the item shows a pending placeholder. When the image
      arrives, the items that show images (diagram items and tool image
      outputs, via the shared marker interface that replaces
      `DiagramShower`) re-render, as diagrams do today; other items don't.
- [ ] The image output reads its source's result whenever it renders,
      exactly as `toolDetailOutputItem` does for text today
      (`tool_detail.go:517-532`). No new plumbing makes tool drill-ins
      update live (no source-aware `Version()`): a result arriving after
      opening shows when something else re-renders the item (resize, an
      image invalidation) or on the next opening. A test pins that a late
      result with image data renders correctly once the item re-renders,
      and that `o` uses the result read at that render.
- [ ] Tool image outputs work in tool drill-ins nested under a subagent
      drill-in.
- [ ] Without Kitty graphics or a reported cell size, the item shows the
      "Loaded Image" line plus a note that the terminal can't display
      images.
- [ ] An unsupported MIME type or a decode failure shows the "Loaded
      Image" line plus "Can't display: <reason>". Unsupported types are not
      logged at warn level; decode failures are.
- [ ] `o` writes the result's bytes, with an extension matching the MIME
      type, to the export directory (`diagramExportDir`, pruned after
      24 h) and opens it with the OS default handler, including when
      display failed. The hint shows `o open in viewer`.
- [ ] Popping the drill-in (back key, with no other event after it),
      or pushing another drill-in on top of it, deletes its full-size
      image from the terminal in the same `Update`. Returning to a
      covered drill-in renders its image again.
- [ ] Stale completions are rejected: a ready or shown message for a
      full-size request from an earlier opening (closed and reopened
      while it was in flight) never marks the new entry ready or leaves
      an image in the terminal. Tested for tool → diagram and diagram →
      tool transitions, resize while rendering, and close/reopen with
      messages in flight.
- [ ] A covered drill-in never queues or transmits a full-size image:
      re-rendering it (through `InvalidateDiagrams`, which visits every
      chat on the stack, or `ClampOffset`) leaves it pending. Tested.
- [ ] Forgetting an earlier opening's image never deletes the current
      opening's image of the same content and size. Tested with the old
      forget landing after the new transmit.
- [ ] A change of cell size, Kitty support or (for diagrams) background
      forgets and redraws images as diagrams do today.
- [ ] With `options.tui.diagrams` off, tool images still display.
- [ ] Mermaid behaviour is unchanged (see Constraints).
- [ ] Verified end to end in a real session via the `terminal` MCP
      (`tui-manual-testing` skill).
- [ ] `AGENTS.md` describes the shared graphics cache and its producers
      instead of a Mermaid-only feature; this spec moves to
      `plans/completed/` in the PR that ships it.

**Design Decisions:**

- **Drill-in only.** Inline thumbnails were declined to keep this change
  to one image on screen at a time; they become cheap once the shared
  cache exists.
- **Shared cache, not a stand-alone feature.** A stand-alone one-image
  feature looked simpler but would re-implement every edge case
  `mermaidFeature` already handles: a transmit finishing after its
  drill-in closed (`diagramShownMsg` forgetting again), results arriving
  for a width no longer used, invalidation on draw-condition changes,
  re-rendering when the image cache lost an entry, drill-in replacement,
  and failure/retry state. Two copies would drift. The split:
  - *Graphics cache* (generic): entries keyed by content hash, producer,
    width, kind and cell size; pending/ready/failed with transient retry;
    the worker goroutine and queue; transmit-then-shown sequencing and
    forget races; the eviction budget and `InView` pinning;
    `drawConditions` invalidation; tracking the open drill-in that holds a
    full-size image and freeing it on close.
  - *Producer* (per kind): turns a request into a padded `image.Image`
    with cols, rows and squeezed. The Mermaid producer keeps Pele, resvg,
    idle unload, theme/palette and browser export. The bitmap producer
    decodes and fits to cells. Background is part of the key only for
    diagrams.
  - The renderer interface the chat items use generalises
    `chat.DiagramRenderer` so items ask by content and producer; exact
    naming is left to implementation, but `GLOSSARY.md` gets the new
    terms.
- **No block-character fallback.** The cache assumes the terminal holds
  the pixels; block drawing keeps decoded images in Anvil and needs its own
  memory policy. Diagrams already fall back to text the same way.
- **`o` opens the result's bytes, not the original path.** It shows exactly
  what the model saw, works for MCP results without a path, and survives
  the file changing or being deleted. Unlike diagrams, `o` stays available
  when display fails, since the OS viewer often handles formats Anvil
  can't. It only opens raster types that carry no code (PNG, JPEG, GIF,
  WebP, BMP), judged by sniffing the bytes rather than trusting the
  result's MIME type: tool results are untrusted, the OS opens SVG or
  HTML in a browser where scripts in it would run, and some openers
  (xdg-open) go by content. Anything else gets a warning.
- **Actual size, shrunk to fit.** Mostly screenshots and mocks: fitting to
  the view height makes tall screenshots unreadable, and enlarging blurs
  icons. Same caps as the diagram drill-in so "one full-size image, at
  most 64 MB, freed on close" stays a single rule.
- **Text the tool returned with an image shows above it.** MCP tools send
  notes beside a screenshot, and the Docker MCP drill-in showed them
  before; the placeholder the agent writes for a bare image is skipped.
- **Image is the Output item, not a second drill-in.** The image is the
  output; a nested drill-in adds a keypress and breadcrumb for nothing.
  `BuildToolDetailItems` takes the renderer. The existing
  `toolDetailOutputItem` decides at render time, from its source's
  result, whether to draw an image. Tool drill-ins gain no live updates, so the
  item's list version changes only when the image's view state does: the
  shared marker interface lets `InvalidateDiagrams` clear its cache and
  bump its version like the diagram items. It reports `Finished() ==
  false` while it draws an image (as `DiagramDetailItem` does) so the list
  never freezes it, and its own render cache keys on the renderer's view
  state too. `ToolMessageItem` gains whatever result accessor this needs.
- **Drill-in ownership is reconciled synchronously, not polled.**
  Today `mermaidFeature.Observe` notices a closed diagram drill-in by
  polling `DrillInOpen`, but `features.observe` skips input messages and
  runs before `update`, so after a back-key pop the image stays in the
  terminal until some later non-input message. `DrillInOpen` also checks
  membership, not whether the drill-in is on top. Instead, every place
  the UI changes `drillStack` (push for tool, agent, thinking and diagram
  drill-ins, and pop) tells the cache which chat is now on top, and the
  cache frees any full-size image not owned by it. Each drill-in's items
  get a renderer scoped to their chat (a handle bound to the owner), so
  the cache knows who asks: full-size lookups from a chat that isn't on
  top return pending and queue nothing. Full-size requests carry a
  generation that increments on every ownership change; results and
  shown messages from an older generation are dropped (and a shown one
  forgotten again). The generation is part of a full-size image's
  terminal ID, so forgetting an old opening can't delete a new opening's
  image of the same content. This changes Mermaid's drill-in only in
  freeing sooner, which no existing test should observe. Producer
  identity and
  kind (inline/full) are separate key fields, and the per-kind width
  tracking (`mermaidFeature.widths`) stays per kind: the one full-size
  slot is shared by both producers, so a tool image and a diagram
  drill-in never coexist.
- **No config switch.** Unlike diagrams, drill-in images change neither the
  system prompt nor the chat's structure, cost nothing until opened and
  fall back on their own.
- **No SVG via the vendored resvg.** It would put the WASM engine on the
  bitmap path for a rare case.

**Context Files:**

- `internal/ui/model/mermaid.go`, `internal/ui/model/mermaid_render.go`:
  the lifecycle to extract, and the Mermaid producer.
- `internal/ui/model/feature_host.go`: `PushDrillIn`, `DrillInOpen`,
  `InvalidateDiagrams`.
- `internal/ui/model/ui.go`: feature wiring (gated on `DiagramsEnabled`
  around line 481) and the `util.ToolDrillInMsg` handler.
- `internal/ui/chat/diagrams.go`, `internal/ui/chat/diagram_detail.go`:
  renderer interface, view states, `DiagramShower`, the drill-in item to
  mirror.
- `internal/ui/chat/tool_detail.go`: `BuildToolDetailItems`.
- `internal/ui/chat/tools.go`, `file.go`, `generic.go`, `docker_mcp.go`:
  `toolOutputImageContent` and its callers.
- `internal/ui/image/image.go`: Kitty transmit, render, forget.
- `internal/agent/tools/view.go`: supported image types and sniffing.
- `plans/completed/design-2026-10-20-mermaid-diagrams.md`,
  `docs/adr/0004-mermaid-rendering.md`,
  `docs/adr/0005-cross-instance-sharing.md`.
