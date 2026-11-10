# Stacks

A stack is a chain of branches managed with the `gh stack` extension, each with its own PR based on the branch below it. Use one when work ships as several dependent PRs, such as one per phase of a plan. Load the **gh-stack** skill for its commands and recovery; if it isn't installed, `gh stack <command> --help` is authoritative. If `gh stack` itself is missing, tell the user it needs `gh extension install github/gh-stack`.

## Rules

- **You're on a stack layer** when `gh stack view --json | jq -e 'any(.branches[]; .isCurrent)'` succeeds. Exit 0 from `view` alone isn't enough: on the trunk it can still print a stack, with no layer current. Its `branches` list runs bottom first.
- **Stack only what depends on what's below it.** Each layer must build and pass its tests without the layers above. Unrelated changes are separate PRs or separate stacks, so reviewers can take them in any order.
- **`wtp` names a worktree by the branch checked out in it**, so `wtp cd <branch>` fails once another layer is checked out there. Keep `$path` from when you resolved it, or find the current layer with `wtp list` and run `wtp cd <current-layer>`.
- **Remove a stack's worktree with `wtp remove --keep-branch <current-layer>`** until every PR in it has merged: the other layers' branches stay either way, but the current one is deleted without it.
- **`gh stack push`, `submit` and `sync` push.** Run them only when you'd be allowed to `git push`. `gh stack rebase` rewrites local branches without pushing; a layer that already has a PR then needs a force push, which `gh stack push` does.
- **To change a lower layer**, `gh stack down` or `gh stack checkout <branch>` to it, commit, then `gh stack rebase --upstack` and `gh stack top`. `gh stack add` only runs on the top layer.

## Start a New Stack

Create the stack before writing code. Keep it in one worktree, named after its bottom layer, and move between layers inside it:

```bash
wtp add -b <bottom-branch> --stay main
path="$(wtp cd <bottom-branch>)"
cd "$path" && gh stack init <bottom-branch>   # adopts the checked-out branch
# ...commit the bottom layer...
cd "$path" && gh stack add <next-branch>      # next layer, checked out in the same worktree
```

## Turn Existing Work Into a Stack

`gh stack init <branch>...` takes branches bottom first and adopts any that exist, including branches checked out in other worktrees, without checking them out. Pick the case that matches.

### Follow-on Work on an Existing Branch

The existing branch, and its PR if it has one, becomes the bottom layer:

```bash
cd "$path" && gh stack init <existing-branch>
cd "$path" && gh stack add <follow-on-branch>
```

`gh stack submit` later links the existing PR into the stack instead of opening a new one.

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

If concerns are mixed across commits, rebuild the layers from the saved tree, bottom up, in the same worktree:

```bash
cd "$path" && gh stack init <layer-1>   # new branch from the trunk, checked out here
cd "$path" && git restore --source <branch>-presplit --staged --worktree -- <layer-1 paths>
# For part of a file, apply an edited `git diff <trunk> <branch>-presplit -- <file>` with `git apply --index`.
cd "$path" && git commit -m "..."       # then run the tests
cd "$path" && gh stack add <layer-2>    # repeat for each middle layer
# Top layer: move the large branch here and take everything that's left.
cd "$path" && git branch -f <branch> HEAD && gh stack add <branch>
cd "$path" && git restore --source <branch>-presplit --staged --worktree -- . && git commit -m "..."
```

`git restore --source ... -- .` also removes files the original deleted. Interactive commands (`git add -p`, `git rebase -i`) need a terminal, so select by path or apply edited patches instead.

### Several Worktrees Holding Parts of One Change

Leave each branch in its worktree; gh-stack updates the worktree that owns each branch. Order them by dependency, bottom first, then rebase each onto the one below:

```bash
cd "$path" && gh stack init <branch-1> <branch-2> <branch-3>
cd "$path" && gh stack rebase --no-trunk
```

- Check `git status` in every one of those worktrees first: the rebase stops at one with uncommitted changes. If one has work that isn't yours to commit, ask the user.
- A conflict pauses the rebase in the worktree it names. Resolve it there, `git add`, then `gh stack rebase --continue`.
- `gh stack up`, `down`, `top` and `bottom` fail for a layer checked out in another worktree. Add `--print-path` to get that worktree's path instead, and run the next commands there; `gh stack add` runs in the top layer's worktree.
