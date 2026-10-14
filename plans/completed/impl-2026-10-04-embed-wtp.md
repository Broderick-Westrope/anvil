# Embed wtp in Anvil

## Goal

Ship `wtp` inside Anvil so the bundled `using-git-worktrees` skill can rely on
it without a separate install. Agents get `wtp` as an in-process shell
builtin (like `jq`); humans get `anvil wtp ...` and can alias
`wtp="anvil wtp"`. The wtp repo stays standalone.

## Phase 1: make wtp embeddable (wtp repo, `feat/embeddable`)

- Move the CLI from `cmd/wtp` (package `main`) into an importable package
  `github.com/Broderick-Westrope/wtp/v3/cli`. `cmd/wtp/main.go` becomes a
  thin wrapper.
- Public API:

  ```go
  type Env struct {
      Dir     string    // working directory; "" means os.Getwd()
      Stdin   io.Reader // nil means os.Stdin
      Stdout  io.Writer // nil means os.Stdout
      Stderr  io.Writer // nil means os.Stderr
      Environ []string  // nil means os.Environ()
      Self    []string  // argv prefix that re-invokes wtp; nil means [os.Executable()]
      Version string    // "" means "dev"
  }

  func Run(ctx context.Context, args []string, env Env) error // args[0] is the program name
  ```

- No per-run state in package globals: working directory, streams and
  environment flow through `ctx`, so concurrent `Run` calls in different
  directories are safe.
- Terminal detection uses the `Env` streams, never the process's.
- Every subprocess (`git`, `gh`, `fzf`, hooks) runs in `Env.Dir` with
  `Env.Environ`.

## Phase 2: embed in Anvil (anvil repo, `feat/embed-wtp`)

- `wtp` shell builtin in `internal/shell` using the handler context's dir,
  env and stdio, with `Self` set to `[anvil, wtp]`.
- `anvil wtp ...` cobra subcommand that passes its arguments straight
  through.
- Build against the local wtp checkout via a `replace` directive until wtp
  is pushed and tagged.

## Phase 3: single source of truth for worktrees

- Move `using-git-worktrees` and `finishing-a-development-branch` from
  dotagents into `internal/skills/builtin/`.
- Reduce `<git_workflow>` and the bash commit/PR recipes to the invariant
  rules plus a pointer to `using-git-worktrees`.
- Delete the moved skills from dotagents and point its references at the
  builtins.
