---
name: finishing-a-development-branch
description: "Lands or wraps up work on a feature branch in a linked worktree: verifies tests, then merges locally, opens a PR, keeps the branch, or discards it, and cleans up the worktree with wtp. Use when implementation is done and the user wants to merge, open a PR, or clean up a branch or worktree."
---

# Finishing a Development Branch

Worktree commands follow the **using-git-worktrees** skill. Load it if you haven't.

## 1. Verify

Run the project's full test suite and linters in the worktree. If anything fails, fix it before going further. Don't offer to merge or open a PR for failing work.

## 2. Find the Base Branch

Use the branch the user named. Otherwise use the branch checked out in the root worktree, which is normally the default branch:

```bash
git -C "$(wtp cd @)" branch --show-current
```

## 3. Pick an Option

If the user already said what they want, do that. Otherwise ask once, with exactly these options:

```
Work on <branch> is complete. What would you like to do?

1. Merge into <base> locally
2. Push and open a pull request
3. Keep the branch as-is
4. Discard this work
```

Choosing option 1, 2 or 4 is the user's explicit permission to commit to the base branch, push, or delete work, as that option describes. Nothing else is.

## 4. Do It

`$path` is the feature worktree (`wtp cd <branch>`). `$root` is the root worktree (`wtp cd @`).

### Option 1: Merge Locally

Keep history linear: rebase the branch onto the latest base, then fast-forward the base in the root worktree.

```bash
git -C "$root" pull --ff-only
git -C "$path" rebase <base>
<run the test suite in $path again>
git -C "$root" merge --ff-only <branch>
cd "$root" && wtp remove <branch>
```

If the rebase conflicts, resolve the conflicts in `$path` and continue. If the root worktree has uncommitted changes or isn't on `<base>`, stop and tell the user instead of touching it.

### Option 2: Push and Open a PR

```bash
git -C "$path" push -u origin <branch>
cd "$path" && gh pr create --title "<title>" --body "<body>"
```

Write the title and body as described in `<pull_requests>`, which defers to the repository's PR conventions. Keep the worktree: the PR may need more commits.

### Option 3: Keep As-Is

Report the branch name and worktree path. Change nothing.

### Option 4: Discard

Confirm first, listing exactly what will be lost:

```
This permanently deletes:
- Branch <branch> and its commits: <git log --oneline <base>..<branch>>
- Worktree at <path>, including uncommitted changes

Type 'discard' to confirm.
```

Only after the user types `discard`:

```bash
cd "$root" && wtp remove --force --force-branch <branch>
```

## Quick Reference

| Option | Base branch | Pushes | Worktree | Branch |
|---|---|---|---|---|
| 1. Merge locally | Fast-forwarded | No | Removed | Deleted |
| 2. Open PR | Unchanged | Yes | Kept | Kept |
| 3. Keep | Unchanged | No | Kept | Kept |
| 4. Discard | Unchanged | No | Removed | Deleted (forced) |
