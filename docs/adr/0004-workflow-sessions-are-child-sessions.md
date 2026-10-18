# 0004: A workflow run's sessions are child sessions of the session that started it

**Status:** Accepted
**Date:** 2026-10-15

## Context

A workflow run makes sessions: orchestrator sessions such as `/build`'s builders, and subagent sessions for `agent()`. Subagent sessions were already children of the invoking session; orchestrator sessions were top-level, so they cluttered the session list, `--continue` could pick one, and nothing tied them to the run that made them. The run's node and run view need every session a run made to be reachable from the run, and the user needs to keep talking to a builder after the run hands over.

Until now, "child session" meant "a subagent's session". Several places relied on that: continuing a session (`resolveSession` refused children), the job waker (skipped children), the permission bouncer (followed parents to the root for the user's intent), and cache-usage tracking (forgot a child's history after each run and recorded it as a subagent).

## Decision

Every session a run makes is a child of the invoking session and records the run, the seq of the event that made it, and its kind: `session` for orchestrator sessions, `agent` for subagents. `Session.Conversational` (top-level, or a run's orchestrator session) is the one answer to "does the user talk to this session directly", and the places above use it. A builder's own cost stays on the builder; the run's total is the sum over its sessions.

## Alternatives

- **Keep orchestrator sessions top-level and link them only by run id.** No change to what a child means, but they stay in the session list and `--continue`, and the run isn't their container.
- **Infer the kind from the creating event instead of storing it.** No extra column, but every guard would need the workflow log to answer a simple question about a session.
- **Roll builder cost into the invoking session, as subagents do.** Talking to a builder after hand-over would keep growing the invoking session's cost indefinitely.

## Consequences

- A child session is no longer necessarily a subagent's. New code that asks "is this a subagent session" must use `Conversational`, not `ParentSessionID`.
- Builders are invisible in the session list; they're reached through the run's node. Losing the node's marker would leave them reachable only by id.
- The bouncer takes a workflow `agent()` subagent's intent from its own session (the script's prompt), not from the session that started the run, which is a sibling conversation by then.
- A workflow `agent()` subagent's surviving background jobs stay in its own session instead of passing to its parent (the user's session, which the run never works in); `workflow.exclusive` waits for them or stops them with the held sessions' jobs. Nobody is woken when one finishes outside `exclusive`: the job waker only wakes sessions the user talks to.
- Deleting a session deletes the sessions workflow runs made from it, since builders hold a copy of its conversation and nothing else reaches them; it's refused while one of its runs is going (in any Anvil sharing the database) or while a session a run made from it is working or has background jobs running.
- The jobs sidebar still lists a builder's background jobs under the invoking session, marked like a subagent's: they're work started from that session.
- Revisit if sessions other than workflow builders need to be conversational children (for example, forks the user makes by hand).
