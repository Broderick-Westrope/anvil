# Stacks

A stack is a chain of branches managed with the `gh stack` extension, each with its own PR based on the branch below it. Use one when work ships as several dependent PRs, such as one per phase of a plan. Load the **gh-stack** skill for its commands and recovery; if it isn't installed, `gh stack <command> --help` is authoritative. If `gh stack` itself is missing, tell the user it needs `gh extension install github/gh-stack`.

## Rules

- **Every layer has its own worktree, and a worktree stays on the branch it was made for.** Move between layers by changing worktree, never by changing branch: another agent or the user may be reading or writing in the worktree you'd switch, and a switch moves their files and their next commit onto your layer. gh-stack handles a stack spread across worktrees: `rebase` and `sync` update each clean worktree that owns a layer.
- **Reach a layer through `wtp`:** `wtp cd <layer>` prints its worktree, and `wtp add <layer> --stay` creates one when it has none. `gh stack up`, `down`, `top`, `bottom`, `checkout` and `switch` check a layer that has no worktree out in the current one, so leave navigation to `wtp`.
- **You're on a stack layer** when `gh stack view --json | jq -e 'any(.branches[]; .isCurrent)'` succeeds. Exit 0 from `view` alone isn't enough: on the trunk it can still print a stack, with no layer current. Its `branches` list runs bottom first.
- **Stack only what depends on what's below it.** Each layer must build and pass its tests without the layers above. Unrelated changes are separate PRs or separate stacks, so reviewers can take them in any order.
- **Remove a layer's worktree with `wtp remove --keep-branch <layer>`** until its PR has merged, since plain `wtp remove` deletes the branch.
- **`gh stack push`, `submit` and `sync` push.** Run them only when you'd be allowed to `git push`. `gh stack rebase` rewrites local branches without pushing; a layer that already has a PR then needs a force push, which `gh stack push` does.

## Add a Layer

`gh stack add` only runs on the top layer. Create the new layer's branch and worktree from the top layer with `wtp`, then adopt it from the top layer's worktree; `gh stack add` adopts an existing branch checked out elsewhere and leaves the current checkout unchanged:

```bash
wtp add -b <next-branch> --stay <top-branch>
cd "$(wtp cd <top-branch>)" && gh stack add <next-branch>
path="$(wtp cd <next-branch>)"                # work on the new layer here
```

## Start a New Stack

Create the stack before writing code. Create the bottom branch from the latest default branch as in `anvil://skills/using-git-worktrees/SKILL.md` (Create a Worktree), which fetches and fast-forwards the root first:

```bash
wtp add -b <bottom-branch> --stay main      # after the fetch and fast-forward
path="$(wtp cd <bottom-branch>)"
cd "$path" && gh stack init <bottom-branch>   # adopts the checked-out branch
# ...commit the bottom layer...
```

Then [add each further layer](#add-a-layer) in its own worktree.

## Change a Lower Layer

Commit in the lower layer's own worktree, then replay the layers above onto it from there:

```bash
lower="$(wtp cd <layer>)" || { wtp add <layer> --stay && lower="$(wtp cd <layer>)"; }
cd "$lower" && git commit ...
cd "$lower" && gh stack rebase --upstack
```

Check `git status` in each worktree above first: the rebase stops at one with uncommitted changes. If one has work that isn't yours to commit, ask the user. A conflict pauses the rebase in the worktree it names; resolve it there, `git add`, then `gh stack rebase --continue`.

## Turn Existing Work Into a Stack

`gh stack init <branch>...` takes branches bottom first and adopts any that exist, including branches checked out in other worktrees, without checking them out. Pick the case that matches.

### Follow-on Work on an Existing Branch

The existing branch, and its PR if it has one, becomes the bottom layer:

```bash
cd "$path" && gh stack init <existing-branch>
```

Then [add the follow-on layer](#add-a-layer). `gh stack submit` later links the existing PR into the stack instead of opening a new one.

### Splitting One Large Branch

The large branch keeps its name, worktree and any PR, and becomes the top layer; once submitted, its PR shows only the top layer's diff. Done when `git diff <branch>-presplit <branch>` prints nothing and every layer passes its tests on its own.

First save the original, and tell the user about the `<branch>-presplit` branch in your final message:

```bash
cd "$path" && git branch <branch>-presplit <branch>
```

If the commits are already grouped by concern, bottom first, branch at each boundary. No history changes:

```bash
cd "$path" && git branch <layer-1> <last-commit-of-layer-1>
cd "$path" && gh stack init <layer-1> <layer-2> ... <branch>
```

If concerns are mixed across commits, rebuild the layers from the saved tree, bottom up, each in its own worktree branched from the one below (the trunk for the first):

```bash
wtp add -b <layer-1> --stay <trunk>
cd "$(wtp cd <layer-1>)" && git restore --source <branch>-presplit --staged --worktree -- <layer-1 paths>
# For part of a file, apply an edited `git diff <trunk> <branch>-presplit -- <file>` with `git apply --index`.
cd "$(wtp cd <layer-1>)" && git commit -m "..."   # then run the tests
# Repeat for each middle layer: wtp add -b <layer-2> --stay <layer-1>, and so on.
# Top layer: reset the large branch onto the layer below and take everything that's left.
cd "$path" && git reset --hard <layer-below> && git restore --source <branch>-presplit --staged --worktree -- . && git commit -m "..."
cd "$path" && gh stack init <layer-1> <layer-2> ... <branch>
```

`git restore --source ... -- .` also removes files the original deleted. Interactive commands (`git add -p`, `git rebase -i`) need a terminal, so select by path or apply edited patches instead.

### Several Worktrees Holding Parts of One Change

Leave each branch in its worktree. Order them by dependency, bottom first, then rebase each onto the one below:

```bash
cd "$path" && gh stack init <branch-1> <branch-2> <branch-3>
cd "$path" && gh stack rebase --no-trunk
```

As in [Change a Lower Layer](#change-a-lower-layer), check `git status` in every one of those worktrees first, and resolve a conflict in the worktree it names.
