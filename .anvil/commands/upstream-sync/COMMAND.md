---
description: Analyse and cherry-pick new commits from the upstream remote.
---

You are performing an upstream sync for a fork of the Anvil project.

## Context

- **Upstream remote**: `upstream` (charmbracelet/crush)
- **Fork module path**: `github.com/Broderick-Westrope/anvil`
- **Tracking file**: `upstream-tracking.json` in the repo root — contains the hash of the last upstream commit we checked.
- **Upstream module path**: `github.com/charmbracelet/crush` — cherry-picked code will reference this and must be rewritten.

## Known Substitutions

When cherry-picking, these adaptations are always needed:

| Upstream | Local |
|----------|-------|
| `github.com/charmbracelet/crush` | `github.com/Broderick-Westrope/anvil` |
| `styles.CharmtonePantera()` | `styles.TokyoNight()` |
| `styles.HypercrushObsidiana()` | `styles.TokyoNight()` |

The `attachments.NewRenderer` function locally takes 5 args (includes `sty.Attachments.Skill`); upstream takes 4.

## Procedure

### 1. Fetch and Discover

```
git fetch upstream
```

Read `upstream-tracking.json` to get the last checked commit hash.

List all new commits on `upstream/main` since that hash, excluding:
- Commits by `Brodie Westrope` or `Broderick Westrope`
- Commits by `dependabot[bot]`
- `chore(legal):` CLA commits
- `chore: auto-update` commits
- Version bump commits (e.g. `v0.69.0`)

### 2. Categorise and Prioritise

Group commits into:
- **Bug fixes** — race conditions, crashes, correctness issues
- **Features** — new capabilities, tools, UI improvements
- **Performance** — rendering, caching, resource usage
- **Chores** — formatting, refactoring, test fixes

Present the categorised list to the user and ask which groups/commits to cherry-pick.

### 3. Cherry-Pick

**IMPORTANT: Cherry-picks MUST be sequential, never parallel.** Multiple agents
writing to the same git worktree concurrently causes stash conflicts, race
conditions, and broken state. Process one batch at a time, wait for it to
complete, then start the next.

Group approved commits into batches of related changes (e.g. all bedrock fixes,
all LSP commits). Process each batch by delegating to a single `@fixer` agent
with this template:

> You are in the repo at /Users/broderick.westrope/dev/helse/anvil on the `main` branch.
>
> Cherry-pick these commits IN ORDER. Module path is `github.com/Broderick-Westrope/anvil`. Use `styles.TokyoNight()` not `styles.CharmtonePantera()` or `styles.HypercrushObsidiana()`.
>
> The upstream module path `github.com/charmbracelet/crush` must be rewritten to `github.com/Broderick-Westrope/anvil` in any cherry-picked code.
>
> The `attachments.NewRenderer` function locally takes 5 args (includes `sty.Attachments.Skill`); upstream takes 4. If a cherry-pick touches this call, add the extra arg.
>
> [list commits with full hashes and messages]
>
> For each:
> 1. Run `git cherry-pick <hash> --no-commit`
> 2. If conflicts: resolve, adapting to local naming/structure
> 3. Run `go build ./...` to verify compilation
> 4. Commit with `git commit -m "<original message> (cherry-pick <short-hash>)"`
> 5. If conflicts are too severe to resolve, abort with `git cherry-pick --abort`, skip it, and continue to the next.
>
> After all commits in this batch, run `go build ./...` and `go test` on affected packages.
> Report: for each commit, state success/skipped/conflicts-resolved.

After the `@fixer` completes, verify the result (`go build ./...`, `go test ./...`), then move to
the next batch. Do NOT start the next batch until the previous one is confirmed
clean.

### 4. Update Tracking

After all cherry-picks are complete, update `upstream-tracking.json` with the current `upstream/main` HEAD:

```
git log upstream/main -1 --format="%H%n%an%n%aI%n%s"
```

### 5. Skip the client/server commits

The upstream has a client/server architecture feature (multi-client workspace sharing via `crush serve`). These commits are a large interconnected feature we don't use. Skip all commits with `(server)`, `(client)`, or `(api)` scope that relate to this feature unless explicitly asked.
