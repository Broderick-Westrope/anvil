# Continue the Anvil Upstream Sync — Batch 5 Onward

You are continuing an upstream sync for a fork of the Anvil project. A previous
agent completed batches 1–4; you are picking up at batch 5.

## Context

- **Repo/worktree**: `/Users/broderick.westrope/Library/Application Support/wtp/worktrees/github.com-personal/Broderick-Westrope/anvil/upstream-cherry-picking`
  (branch `upstream-cherry-picking`; main is checked out at
  `/Users/broderick.westrope/dev/helse/anvil` — never edit there)
- **Upstream remote**: `upstream` (charmbracelet/crush), already fetched
- **Fork module path**: `github.com/Broderick-Westrope/anvil`
- **Tracking doc**: `plans/upstream-sync-2026-08-10.md` — **read this first, in
  full**. It is the single source of truth: per-batch commit lists,
  statuses, dispositions (picked/deferred/skipped + why), local divergences,
  vendored prerequisites, review-round findings, and an
  upstream→local commit mapping for adapted picks.
- **Do not** update `upstream-tracking.json` until the entire sync is done.

## Current state (verified at handoff)

- HEAD: `7d727cf9`, tree clean, `go build ./...` + `go test ./...` green,
  `-race` green on config/shell/lock/ui-chat/ui-model.
- Batches 1–4 and the dep bump (batch 16, promoted) are **done** and
  reviewed. Batches 1–3 are merged to `main` and tagged
  `review/2026-08-10-sync-batches-1-3`. Batch 4 + its review fixes are on
  the branch but NOT yet merged/tagged.
- Deps: catwalk v0.51.6, fantasy v0.40.0, OpenAI SDK switched to
  `github.com/openai/openai-go/v3` (upstream 14483cac folded in).

## Known substitutions (apply to every cherry-pick)

| Upstream | Local |
|----------|-------|
| `github.com/charmbracelet/crush` | `github.com/Broderick-Westrope/anvil` |
| `styles.CharmtonePantera()` / `styles.HypercrushObsidiana()` | `styles.TokyoNight()` |
| `crush.json` / `.crush` dir / `CRUSH_*` env | `anvil.json` / `.anvil` / `ANVIL_*` |
| "Crush"/"crush" in comments, strings, style names | "Anvil"/"anvil" |
| Upstream ticket refs (e.g. `CHARM-1785`) | upstream commit SHA |

`attachments.NewRenderer` takes 5 args locally (extra `sty.Attachments.Skill`);
upstream takes 4.

## Local divergences that WILL bite you (hard-won; full list in the doc)

1. **Test fixtures named `crush.json` are silently ignored** by our loader —
   they load an empty config and the test passes weakly. This trap has been
   caught FOUR times. Check every imported test/bench fixture.
2. **Fork is single-theme.** `applyTheme`/`refreshStyles` were deleted;
   `common.ChromaStyle` memoizes on Styles-pointer identity (see its NOTE).
   Theme-related upstream hunks usually get dropped or inverted.
3. **No bang mode, no chat scrollbar, no client/server.** Upstream UI commits
   are heavily interleaved with all three. Batch 4 notes exactly what was
   dropped along the scrollbar seam (`resizing` flag, `Overflows` is
   caller-less, `chatWarmMsg` carries its owning `*Chat`) and what batch 12
   must restore.
4. **Yolo is granular** (`YoloLevel`/`cycleYoloLevel` from main's permission
   system), not upstream's boolean (`PermissionSkipRequests`/`toggleYoloMode`).
5. **Sessions are trees**: `CreateTaskSession` validates parent existence;
   `sessions.Rename` takes a 4th `titleIsCustom` arg.
6. **Parallel tool execution**: `OnToolCall`/`OnToolResult` run on parallel
   goroutines here (single-threaded upstream) — shared state between them
   needs a mutex.
7. **Anthropic OAuth is fork-only**: every upstream touch of
   `applyToken`/token-exchange switches needs the
   `catwalk.InferenceProviderAnthropic` case re-added.
8. **gopls diagnostics go stale** after dep changes/large edits in this
   worktree — trust `go build` / `go vet`, not the diagnostics pane.

## Procedure per batch (established rhythm)

1. Work sequentially, never parallel cherry-picks. For each commit:
   `git cherry-pick <hash> --no-commit`, resolve, `go build ./...`, targeted
   `go test`, then commit as `<original message> (cherry-pick <short-hash>)`.
   Heavily adapted picks get an `Adapted:` paragraph in the commit body.
2. After module-path conflicts, sweep:
   `grep -rl "charmbracelet/crush" internal/ | xargs sed -i '' 's|github.com/charmbracelet/crush|github.com/Broderick-Westrope/anvil|g'`
3. If a commit is too entangled (client/server, bang mode, boolean yolo),
   **abort and record it** in the doc's "Not applied" table with a reason —
   don't force it. Check whether a deferred commit REPAIRS one already
   taken (batch 2 shipped a panic this way; see the review-round section).
4. Never leave the tree dirty between commits. `git status` before each pick.
5. After the batch: full `go build ./...` + `go test ./...`, update the
   tracking doc (status table + per-batch section with "Not applied" and
   "Adaptations worth remembering"), commit the doc separately as `docs:`.
6. Multi-model review happens at the user's request, not automatically.
   When review findings arrive, audit each against the code before fixing
   (previous rounds: ~90% valid, but severities and dispositions needed
   correction).

## Immediate next task: Batch 5 — UI fixes (15 commits)

Commit list is in the doc under "## Batch 5 — UI fixes". Expectations:

- `ac4bd9c1`/`72069811` (scrolling) and `13501672` (pills) should be mostly
  clean.
- `6437f0d7`/`6d272018` (follow-scroll) — our `Chat.SetSize` uses a `follow`
  flag deliberately (comment explains why AtBottom was rejected); reconcile
  carefully, don't blindly take upstream's `AtBottom()`.
- `0d4c2bbc`/`80ce5834`/`74e725b3` (spinners/timers) — local
  `internal/ui/chat/agent.go` has fork-side elapsed-time handling.
- `1134a5dd` (permission-guard quiet period) — main's granular permission
  dialog diverges from upstream's; the constant change should port, the
  surrounding code may not.
- `b109e35f` (agent-model rebuild helper) — check whether our
  coordinator-facing UI code still matches upstream's shape post-batch-3.
- Watch for scrollbar/bang-mode hunks bleeding in; drop them per the
  batch-4 precedent and note it.

## After batch 5

Batch 6 (MCP fixes, 5 commits) — remember MCP OAuth and lazy MCP are
fork-local features; upstream MCP commits touching tool-list assembly or
auth need care (doc has details). Then the deferred `63dc1f01` remainder
(model pinning + peer-token borrowing; its auth-signal half was already
applied as `21260291`). Optional batches 7–15 need user sign-off on scope.

Between batches, ask the user whether to merge to main + tag a new
`review/<date>-sync-<scope>` checkpoint (the convention is documented at the
top of the tracking doc); don't merge unprompted.

## Working agreements

- Semantic commits, one-line unless context is needed. NEVER push. NEVER
  update git config. Don't revert unrelated changes.
- `gofmt -w` anything you touch (gofumpt is not on PATH in this worktree;
  plain gofmt has been the fallback all sync).
- Fix at root cause; if a fix is fork-specific, pin it with a regression
  test (see `internal/config/reload_hook_deadlock_test.go` and
  `auth_signal_test.go` for the established pattern).
- Record every non-obvious adaptation in the tracking doc as you go — the
  doc is deliberately the memory between agents and between batches.
