# 0009: Session cost only grows, through explicit deltas

**Status:** Accepted
**Date:** 2026-10-18

## Context

A parent session's cost includes what its subagents spent. Subagents can now be resumed and retried, so one child is rolled up into its parent many times, and sibling subagents finish at the same moment. Other code saves the parent session at any time from a copy it read earlier: the todos tool, every step's usage update, title generation. When `Session.Save` wrote cost as an absolute value, any of those stale copies could overwrite a rollup that landed after the copy was read, and adding a child's cumulative cost on each rollup would count it twice.

## Decision

`Session.Save` never writes cost. Cost only grows, through `AddCost(id, delta)` for model usage, `UpdateTitleAndUsage` for title generation, and `RollUpChildCost(child, call)` for subagents, each a single SQL update that adds to the stored value. Each child session keeps a `cost_rolled_up` watermark. A rollup adds `cost - cost_rolled_up` to the parent and to the task call it ran in, then advances the watermark, all in one transaction. Rollups run after every attempt and once more as the call ends, on a context that isn't cancelled.

## Alternatives

- **Absolute saves with a version column and retries.** Every writer would need conflict handling, and most of them only change todos or titles, not cost.
- **Lock the parent for the length of a subagent's run.** It would serialise sibling subagents, which defeats running them in parallel.
- **Compute cost from step usage rows when it's shown.** `step_usage` is observation only and is pruned after 90 days, so it can't be the source of truth.

## Consequences

- Concurrent rollups, repeated resumes and stale saves can't lose or double count cost.
- Writing a new cost onto a `Session` value and saving it does nothing. Callers must add the difference with `AddCost`.
- A binary from before this change still saves absolute cost, and can overwrite rollups. Running mixed versions against one database is unsupported.
- If cost ever needs to go down (a refund, a correction), it needs its own explicit operation.
