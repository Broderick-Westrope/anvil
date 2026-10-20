# Inline Mermaid Diagrams Design Spec

Turns the `spike/mermaid-render` branch into a shipped feature. The spike's
findings (pipeline, measurements, memory, resvg fixes, node ID rules) are in
`plans/spike-2026-10-20-mermaid-rendering.md`; this spec records only what
the feature adds to, removes from, or changes about the spike. Where this
spec is silent, the spike's behaviour stands.

**Problem:** Agents write ```` ```mermaid ```` fences that Anvil shows as
code. Reading a diagram from its source is slow, and the spike showed they
can be rendered inline in pure Go (Pele in goja, resvg in wazero, Kitty
graphics) at acceptable cost.

**Goal:** Every top-level Mermaid fence in a finished assistant message is
its own chat node showing the rendered diagram in Anvil's colours, with a
full-size drill-in, open-in-browser and copy-source. Terminals that can't
show images, and diagrams that fail to render, fall back to the code block,
and a failure says why.

**Scope:**

In:

- Everything the spike does, minus `v` (see below): inline Kitty images,
  diagrams as their own chat nodes, full-size drill-in, `o` export to the
  browser, `c`/`y` copy source, engine idle release, image cache eviction,
  pad-to-whole-cells for Herdr.
- An `anvil.json` off switch.
- A diagram palette derived from the TokyoNight theme definition.
- A failure note under the fallback code block.
- One system-prompt line telling agents diagrams may render.
- Explicit render states (pending, unsupported, failed, ready) for the
  node and the drill-in.
- Non-Kitty drill-in shows the code block instead of opening the browser.
- Help-bar hints for diagram keys.
- Fix glamour's blank padding lines inside the fallback code block's focus
  border.
- Production shape: no `SPIKE` markers, renderer passed through item
  constructors instead of the package-level `chat.Diagrams` hook, merge
  gates passing, docs landed.

Out:

- The `v` key (drill-in uses the standard `→`/`l`/click).
- `+`/`-` zoom and horizontal pan in the drill-in; the browser covers it.
- Rebuilding `resvg.wasm` from source or changing its fonts.
- A supported-diagram-types list for agents, or any way to render types
  Pele rejects (a later piece of work, informed by the failure notes).
- Rendering while a message streams; only finished messages split, as in
  the spike.
- Certifying tmux or terminals other than Ghostty and Herdr.
- Diagrams in `anvil run` (non-interactive) output.

**Constraints:**

- `CGO_ENABLED=0`; no new native dependencies.
- Package layers (`depguard`): `internal/mermaid` must not import
  `internal/ui/...` or `internal/config`. The UI layer maps styles to a
  `mermaid.Theme`.
- `gocognit` cap of 30 on new functions; no new exclusions in
  `.golangci.yml`. `internal/ui/model/mermaid.go` (~600 lines) should be
  split by concern if it trips this or reads as several concerns.
- Mutation CI must pass on changed lines (see `AGENTS.md` "Merge Gates").
  Avoid `make` capacity hints and map-ordered output in new code.
- Nothing loads (Pele, resvg) until the first diagram renders, and nothing
  loads at all when the option is off. Engines release after 2 minutes
  idle, as in the spike.
- `assistant.go` stays as close to upstream shape as the spike left it.
- Licences ship with the vendored code: `PELE_LICENSE` (MIT),
  `resvg/LICENSE` (Apache-2.0), DejaVu font licence.

**Success Criteria:**

- [ ] With the option on (default) in Ghostty and in Herdr, a finished
      message's top-level Mermaid fences show as diagram nodes in document
      order, coloured from the theme, with a transparent background.
- [ ] `options.tui.diagrams: false` makes fences render as ordinary code
      blocks in the assistant message (no split, no diagram nodes), and
      neither engine loads (no goja runtime or wazero compile happens).
- [ ] `→`/`l`/click on a diagram node opens the full-size drill-in; with no
      Kitty support the drill-in shows the code block and nothing opens a
      browser.
- [ ] `o` on a diagram node or in its drill-in opens the themed HTML page;
      `c`/`y` copies the Mermaid source. `v` does nothing special.
- [ ] A diagram Pele or resvg rejects shows its code block plus a muted
      one-line note `Diagram not rendered: <error>` (newlines collapsed,
      truncated to the width), in both the chat node and the drill-in. A
      terminal without Kitty support shows the code block with no note.
      The drill-in never shows "Rendering diagram…" for a failed or
      unsupported diagram.
- [ ] The help bar shows `o` and `c`/`y` hints when a diagram node is
      focused.
- [ ] The fallback code block has no blank glamour padding lines inside the
      focus border.
- [ ] No hex colour for diagrams is defined outside
      `internal/ui/styles/themes.go`; `mermaid.TokyoNight` is gone.
- [ ] With the option on (or omitted), the orchestrator and specialist
      system prompts include the diagram line; with it `false`, neither
      does. Tested for omitted, true and false on both prompt paths.
- [ ] Changing `options.tui.diagrams` in `anvil.json` while Anvil runs is
      reported as needing a restart, like `compact_mode`; the running
      session keeps its startup value.
- [ ] The package-level `chat.Diagrams` hook is gone.
- [ ] Node IDs stay stable and lookups keep working across: a message
      finishing, history reload, branching from any node, message removal,
      and re-splits when text changes. Covered by model tests.
- [ ] `go test ./...`, `task lint` (0 issues), mutation, build on all three
      OSes and `govulncheck` pass.
- [ ] The user runs the manual checklist (below) in Ghostty and Herdr and
      it passes.
- [ ] Docs landed in the same PR: this spec and the spike doc moved to
      `plans/completed/` with links updated, `AGENTS.md` architecture and
      key-patterns entries, `GLOSSARY.md` terms (e.g. diagram node, head
      node), and the option documented in the schema/README where TUI
      options are.

**Design Decisions:**

- **All four spike features ship.** Diagram nodes (the riskiest change:
  `List.SpliceItems`, `#n` IDs, `SyncAssistantSegments`, head rules) are
  what make a diagram focusable, so the drill-in, `o` and copy depend on
  them. Test effort concentrates there.
- **`v` is dropped.** The standard drill-in controls (`→`/`l`/click) work
  on diagram nodes, so a second key adds nothing.
- **On by default, with an off switch:** `TUIOptions.Diagrams *bool`
  (`json:"diagrams,omitempty"`, default true, like `Transparent`). Off
  means no split, code blocks only, engines never load. It's an escape
  hatch for memory (~95 MB RSS while loaded) and a kill switch for
  terminal-specific bugs. Declined: always on with no option; opt-in.
- **Theme comes from the one theme definition.** TokyoNight is the only
  theme (`styles.TokyoNight()` is called everywhere; `quickStyle` is just
  its builder), so no themes need removing. `quickStyle` derives a diagram
  palette from the same `quickStyleOpts` and exposes it on `Styles`; the UI
  maps it to `mermaid.Theme`. Suggested mapping: `fgBase`→`pele-fg`,
  `fgSubtle`→`pele-muted`, `fgMoreSubtle`→`pele-line`,
  `bgMostVisible`→`pele-surface`, `bgLessVisible`→`pele-surface-alt`,
  `primary`→`pele-border`, `accent`→`pele-accent`,
  `destructive`→`pele-critical`, `error`→`pele-error`, series 1–8 from
  `accent`, `warning`, `success`, `destructive`, `secondary`,
  `warningSubtle`, `primary`, `infoMoreSubtle`. This reproduces the spike's
  palette except `pele-surface-alt`: the spike used `#24283b`, which isn't
  in the theme, so it becomes `bgLessVisible` (`#1a1b26`). Check nested
  clusters still read on screen (manual checklist item 1). `pele-bg` keeps the spike's rule (app background, or
  the terminal's when transparent). `internal/mermaid` keeps only a
  neutral default for its own tests. Declined: a hand-written `Diagram`
  palette in `Styles` (duplicates colours); a fixed palette.
- **Failures are visible.** On render failure the node shows the code block
  and a muted note with Pele's or resvg's error, still logged with
  `slog.Warn`. Lack of Kitty support is not a failure and gets no note.
  This tells the user why, and records which Pele gaps matter. Declined:
  silent fallback; error without source.
- **Agents are told, not constrained.** When the option is on, the shared
  `internal/agent/templates/base.md.tpl` (prepended to both
  `orchestrator.md.tpl` and `specialist.md.tpl` in
  `internal/agent/prompts.go`) gets one line, gated through a nil-safe
  accessor on `PromptDat.Config` (`internal/agent/prompt/prompt.go`), saying Mermaid fences may be rendered as
  diagrams, so agents should pick the diagram type that best fits. No
  supported-types list, so agents don't quietly limit themselves; the
  failure notes show which unsupported types they actually want. "May"
  because the agent layer can't see terminal capabilities and `anvil run`
  has no UI. Gated only on the config option, which changes rarely, so it
  doesn't churn the prompt cache. Declined: no prompt change; a skill.
- **Option applies at startup.** `diagrams` joins `tuiRestartSettings`
  in `internal/config/restart_required.go` alongside `compact_mode` and
  `transparent`, so a config reload reports "restart required" instead
  of re-splitting live chats, cancelling queued renders, freeing images
  and changing the system prompt mid-session. Declined: live toggling
  (a lot of lifecycle for a setting changed rarely).
- **Render states are explicit.** The renderer the chat items call
  returns a state, not `(string, bool)`: pending (show the code block in
  the node, "Rendering diagram…" in the drill-in), unsupported (no Kitty
  or cell size unknown: code block, no note), failed (code block plus the
  note) and ready (the image). A failure result invalidates the chat so
  both views update.
- **Navigation keys never leave the terminal.** Without Kitty support,
  `→`/`l`/click open the drill-in with the code block. Only `o` opens the
  browser, which also fails or misfires over SSH. Declined: spike
  behaviour (they opened the browser); doing nothing.
- **Renderer passed in, not a global.** The diagram renderer reaches
  `NewAssistantMessageItems`/`ExtractMessageItems` and `DiagramItem`
  through their constructors (or a small interface held by the chat),
  replacing `chat.Diagrams`. This makes tests independent and removes
  hidden state.
- **Support matrix:** Ghostty, and Herdr in Ghostty, are supported and
  manually verified. tmux and other Kitty-capable terminals are best
  effort with no checks. Everything else falls back to code blocks.
  Declined: certifying tmux (placeholder passthrough is fiddly and unused);
  no manual gate.
- **Vendored wasm stays as is.** Record its provenance in
  `internal/mermaid/resvg` (upstream `github.com/boolean-maybe/go-resvg`
  v0.0.2, resvg 0.44, DejaVu Sans built in) and how to rebuild it from the
  upstream Rust crate, so a future font change isn't archaeology.
- **Base on the spike.** The spike's commits are focused and semantic, and
  no decision here diverges enough to start over. Build on
  `feat/mermaid-diagrams`, which branches from the spike's tip. The builder
  may rebase onto current `main` and squash or reword spike commits whose
  messages say "spike".

**Manual checklist (user, Ghostty and Herdr):**

1. A message with text, a diagram, text, two adjacent diagrams, then a tool
   call: nodes in order, no empty nodes, colours match the theme, no box
   behind the diagram.
2. A message that opens with a diagram (with and without thinking): no
   empty node above it.
3. `→`/`l` and click open the drill-in; it scrolls; `o` works inside it;
   leaving frees the image (no ghost image).
4. `o` opens the HTML page; `c`/`y` copies the source.
5. Resize the window: diagrams re-render to the new width.
6. Compact mode on: diagrams still render and nodes behave.
7. Select text across a diagram: no crash or corrupted image.
8. A deliberately unsupported diagram: code block plus failure note.
9. `options.tui.diagrams: false`: plain code blocks.
10. Restart Anvil and reopen the session: history loads split, images
    show.

**Context Files:**

- `plans/spike-2026-10-20-mermaid-rendering.md`: spike findings, the
  baseline for this spec.
- `internal/mermaid/` (`pele.go`, `measure.go`, `vars.go`, `raster.go`,
  `export.go`, `resvg/`).
- `internal/ui/model/mermaid.go`, `internal/ui/model/chat_diagrams.go`,
  `internal/ui/model/chat.go`, `internal/ui/model/ui.go`.
- `internal/ui/chat/diagrams.go`, `diagram_detail.go`, `assistant.go`,
  `messages.go`.
- `internal/ui/list/list.go` (`SpliceItems`), `internal/ui/image/image.go`
  (`Forget`).
- `internal/ui/styles/themes.go`, `internal/ui/styles/quickstyle.go`.
- `internal/config/config.go` (`TUIOptions`).
- `internal/agent/templates/base.md.tpl`, `internal/agent/prompts.go`,
  `internal/agent/prompt/prompt.go`.
- `internal/config/restart_required.go`.
- `internal/ui/AGENTS.md` ("Feature Routing"), `AGENTS.md` ("Merge Gates",
  "Committing"), `.golangci.yml`.
