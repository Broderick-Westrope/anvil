---
name: using-git-worktrees
description: "Source of truth for where to make changes and how to manage git worktrees with wtp. Use before editing or committing in a git repository, when starting work that needs a feature branch, when executing implementation plans, when working on multiple branches at once, or when finding, entering, or removing a worktree."
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
wtp list --no-sync                         # all worktrees; * marks the current one
```

If you're already in a linked worktree for this task, keep working there.

## Create a Worktree

Reuse a worktree if one already exists for the branch (`wtp list --no-sync`). Otherwise create one from anywhere in the repository:

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
| List worktrees | `wtp list --no-sync` |
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
