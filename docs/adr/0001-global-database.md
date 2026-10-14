# 0001: Store all data in one global SQLite database

**Status:** Accepted
**Date:** 2025-05-30

## Context

Crush stores sessions, messages, file snapshots and MCP OAuth tokens in a per-project database at `.anvil/anvil.db`. That meant MCP OAuth had to be repeated in every project and every git worktree, sessions couldn't be found or resumed from another directory, and nothing could search across projects. Anvil runs as many as 10 to 20 processes at once across projects, each with subagents, so any shared store has to cope with concurrent writers.

## Decision

All data lives in one SQLite database at `~/.local/share/anvil/anvil.db`. Sessions record the `working_dir` they started in, and views filter by it by default. Each process opens the database in WAL mode with `busy_timeout=30000` and a single open connection. Existing per-project databases were migrated on first startup and left in place.

## Alternatives

- **Keep per-project databases.** Leaves the re-auth and discovery problems in place.
- **A global index plus per-project databases.** Fixes OAuth and discovery, but cross-project search would still have to open every project database. Rejected because cross-project search was a main motivation.

## Consequences

- OAuth tokens, session resume (`anvil -s <id>`) and pinned sessions work from any directory.
- Every query that used to be implicitly per project must filter by `working_dir` when it means "this project". Forgetting the filter shows other projects' sessions.
- All processes contend for one write lock. Individual writes are under a millisecond, so this has held so far. Revisit if write latency shows up under heavy parallel agent use.
- Upstream Crush changes to database access assume per-project scoping and need checking when synced.
