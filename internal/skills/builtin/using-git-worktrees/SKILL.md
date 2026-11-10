---
name: using-git-worktrees
description: "Source of truth for where to make changes and how to manage git worktrees with wtp. Use before editing or committing in a git repository, when starting work that needs a feature branch, when executing implementation plans, when working on multiple branches at once or a stack of dependent PRs, or when finding, entering, or removing a worktree."
---

# Using Git Worktrees

## Rules

- **The root worktree is read-only.** It's the checkout where `git rev-parse --git-dir` equals `git rev-parse --git-common-dir`. It tracks the default branch so people can read the latest code there. Don't commit in it unless the user or a repository memory file explicitly says to. Small uncommitted tweaks there are fine.
- **Changes you'll commit go in a linked worktree on a feature branch.** Commit as you go there, as described in `<git_workflow>`.
- **Manage every worktree with `wtp`.** Never run `git worktree add`, `git worktree remove` or `git worktree move`, and never pick a worktree directory yourself. `wtp` owns the location, so people and agents always find worktrees in the same place, and the repository's `.wtp.yml` setup hooks run.
- **`wtp` is built into Anvil.** It runs in-process in the bash tool, so it's always available there, even when it isn't installed. People can run it as `anvil wtp`.

## Where Am I?

```bash
git rev-parse --git-dir --git-common-dir   # two equal paths: root worktree
git branch --show-current
wtp list                                   # all worktrees; * marks the current one
```

If you're already in a linked worktree for this task, keep working there.

## Create a Worktree

Reuse a worktree if one already exists for the branch (`wtp list`). Otherwise create one from anywhere in the repository:

```bash
wtp add -b <branch> --stay              # new branch from the current HEAD
wtp add -b <branch> --stay main         # new branch from main
wtp add <branch> --stay                 # existing local or remote branch
```

- Name branches `<type>/<short-kebab-description>`, e.g. `feat/embed-wtp` or `fix/login-timeout`, unless the repository uses a different convention.
- `--stay` stops `wtp` from trying to change the shell's directory.
- `wtp add` prints the new location. Get it at any time with `wtp cd <branch>`. `wtp cd @` prints the root worktree.

## Work in the Worktree

Each bash call runs in a fresh shell, so a `cd` doesn't carry over to the next call. Resolve the path once and use it explicitly:

```bash
path="$(wtp cd <branch>)"
```

- Pass absolute paths under `$path` to `view`, `edit`, `write` and the other file tools.
- Start each bash command with `cd "$path" &&`, or use `git -C "$path" ...`, or `wtp exec <branch> -- <command> [args...]`.
- Worktree paths can contain spaces. Always quote them.

## Set Up and Check the Baseline

`wtp add` runs the repository's `.wtp.yml` hooks. If there are none, install dependencies the way the project does (check memory files, `Taskfile`, `Makefile`, `package.json`, `go.mod`, ...).

Then run the project's tests once. If they already fail, note the failures, don't fix unrelated ones, and mention them in your final message so nobody blames your change for them.

## Stacks

A stack is a chain of branches managed with the `gh stack` extension, each with its own PR based on the branch below it. Use one when work ships as several dependent PRs, such as one per phase of a plan. Load the **gh-stack** skill for its commands and recovery; if it isn't installed, `gh stack <command> --help` is authoritative. If `gh stack` itself is missing, tell the user it needs `gh extension install github/gh-stack`.

Keep the whole stack in one worktree, named after its bottom layer, and move between layers inside it:

```bash
wtp add -b <bottom-branch> --stay main
path="$(wtp cd <bottom-branch>)"
cd "$path" && gh stack init <bottom-branch>   # adopts the checked-out branch
# ...commit the bottom layer...
cd "$path" && gh stack add <next-branch>      # next layer, checked out in the same worktree
```

- **Create the stack before writing code**, then commit each concern on the layer that owns it. To change a lower layer, `gh stack down` or `gh stack checkout <branch>` to it, commit, then `gh stack rebase --upstack` and `gh stack top`.
- **`wtp` names a worktree by the branch checked out in it**, so `wtp cd <bottom-branch>` fails once another layer is checked out. Keep `$path` from when you created the worktree, or find the current layer with `wtp list` and run `wtp cd <current-layer>`.
- **Remove a stack's worktree with `wtp remove --keep-branch <current-layer>`** until every PR in it has merged: the other layers' branches stay either way, but the current one is deleted without it.
- **`gh stack push`, `submit` and `sync` push.** Run them only when you'd be allowed to `git push`. `gh stack rebase` rewrites local branches without pushing.
- **You're on a stack layer** when `gh stack view --json | jq -e 'any(.branches[]; .isCurrent)'` succeeds. Exit 0 from `view` alone isn't enough: on the trunk it can still print a stack, with no layer current. Its `branches` list runs bottom first.

## Remove a Worktree

Run from outside the worktree being removed, e.g. `cd "$(wtp cd @)" && wtp remove ...`.

```bash
wtp remove <branch>                  # remove the worktree and delete its merged branch
wtp remove --keep-branch <branch>    # remove the worktree, keep the branch
wtp remove --force-branch <branch>   # also delete an unmerged branch
```

`wtp remove` deletes the branch by default. Use `--keep-branch` whenever the branch still matters, such as an open PR or work the user hasn't landed. Only use `-f`/`--force` (dirty worktree) or `--force-branch` (unmerged branch) after the user confirms, because they destroy work.

Don't remove worktrees on your own initiative. Leave them for the user unless they ask, or a skill they invoked says to.

## Quick Reference

| Situation | Action |
|---|---|
| Am I in the root worktree? | `git rev-parse --git-dir --git-common-dir` |
| List worktrees | `wtp list` |
| New branch | `wtp add -b <branch> --stay` |
| Existing branch | `wtp add <branch> --stay` |
| Path to a worktree | `wtp cd <branch>` |
| Path to the root worktree | `wtp cd @` |
| Run one command in a worktree | `wtp exec <branch> -- <cmd>` |
| Done, branch merged | `wtp remove <branch>` |
| Done, branch still needed | `wtp remove --keep-branch <branch>` |

## Common Mistakes

- **Using raw `git worktree` commands:** worktrees end up in ad-hoc directories that nobody can find, and setup hooks don't run.
- **Committing in the root worktree:** that checkout is for reading. Create a worktree and commit there.
- **Relying on `cd`:** the next bash call starts in the original directory. Resolve the path with `wtp cd` and pass it explicitly.
- **Deleting a branch that's still needed:** plain `wtp remove` deletes the branch. Add `--keep-branch`.
