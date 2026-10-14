# 0003: Keep upstream-shaped TUI leaf code over the complexity cap, and redesign the dispatchers around fork features

**Status:** Accepted
**Date:** 2026-10-10

## Context

`gocognit` caps new functions at a cognitive complexity of 30. The 25
`internal/ui` functions that were already over it when the cap arrived are
listed as exclusions in `.golangci.yml`. The rest of the repo is working
through its list. For `ui` the cost is different, because it's the package
we share most with upstream Crush.

- `internal/ui/model/ui.go` changed 136 times in six months, more than any
  other Go file. It holds the two most complex functions in the repo:
  `(*UI).handleKeyPressMsg` (405) and `(*UI).update` (384). Every fork
  feature (drill-ins, branching, pins, jobs, Herdr, the MCP palette) added
  cases to them, and agents copy that pattern because it's the code next to
  where they work.
- We port Crush by cherry-picking and adapting. About half of a recent
  sync's picks touched `internal/ui/`. Every pick that touched `ui.go` needed
  adapting. The cause was fork behaviour at lines upstream also edits
  (`m.chat` becoming `activeChat()`, granular permissions, dropped
  bang-mode hunks), not the fork-only cases.
- Upstream hasn't restructured its dispatchers. Its `Update`,
  `handleDialogMsg` and `handleKeyPressMsg` are still single large
  switches. It extracts case bodies into helpers, not a handler table.
- Some over-limit functions are self-contained leaf code that is still
  close to upstream and that we rarely change: the diff renderers, list
  highlighting, the spinner, and two chat tool renderers.

## Decision

Treat the two kinds of `ui` code differently.

**Leaf code stays excluded.** These keep their exclusions, grouped in
`.golangci.yml` under a comment pointing at this ADR:

- `(*DiffView).renderSplit`, `(*DiffView).renderUnified`
- `HighlightBuffer`
- `anim.New`
- `(*DockerMCPToolRenderContext).RenderTool`
- `(*TodosToolRenderContext).RenderTool`
- `parseUnifiedDiff`

**Everything else is refactored**, starting with the dispatchers in
`ui/model/ui.go`, as one design:

- Move the fork's own features out of `update`, `handleKeyPressMsg` and
  `handleDialogMsg` into the sub-models that own their state. The
  dispatchers route to those sub-models through a table instead of gaining
  cases.
- Leave the cases that came from upstream in place and in their current
  line shape, so Crush's patches to them still apply.
- The rest of the list is fork-owned, or changed by us often enough that
  the cap pays for itself: dialogs, the sidebar, the MCP palette, the
  tree, `formatParametersForCopy`, and the remaining `ui.go` functions.

The dispatcher design is settled with the `grilling` skill and an
adversarial review before any code is written, because it becomes the
pattern for the whole TUI. Only one agent works in `ui.go` at a time.

## Alternatives

- **Freeze all of `ui` on the exclusion list.** This would lock in the most
  expensive code in the file we change most, and every new feature would
  add another case.
- **Refactor all of `ui`, including upstream-origin cases and leaf
  renderers.** Restructuring lines Crush also edits turns clean
  cherry-picks into manual ports, and the leaf renderers buy little because
  we rarely touch them.

## Consequences

- Fork features in the dispatchers become separate from upstream's cases.
  Crush's patches then touch only Crush's lines, which should lower
  cherry-pick conflicts rather than raise them.
- The `activeChat()` substitution and the other fork differences at shared
  lines stay. This decision doesn't remove that adaptation cost.
- Reconsider a frozen function when we start changing it ourselves (more
  than a couple of commits in a quarter), or when upstream restructures
  it. At that point, refactor it and delete its entry.
- The `gocognit` exclusion list won't reach zero while these entries
  remain.
- The table is the feature registry in `internal/ui/model/features.go`,
  described under "Feature Routing" in `internal/ui/AGENTS.md`. Workflows
  were the first feature moved onto it
  (`plans/design-2026-10-14-ui-feature-routing.md`); the other fork
  features move one change at a time, extending the surface when one
  needs more than it offers.
- The registry's interfaces (`feature`, `featureHost`, `panelView`) start
  with one real implementation each, plus test fakes. That breaks the usual
  rule against single-implementation interfaces on purpose: they are the
  seam every later fork feature plugs into, and the fake host is how a
  feature is tested without a whole `UI`. If no second feature moves onto
  the registry, collapse it back.
