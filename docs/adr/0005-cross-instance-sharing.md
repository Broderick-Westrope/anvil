# 0005: Share memory across Anvil instances with the cheapest mechanism that works

**Status:** Proposed
**Date:** 2026-10-20

## Context

People run many Anvil instances at once, one per pane or worktree. On
2026-10-20 one machine had 19, averaging about 97 MB RSS each. Anything an
instance holds for itself is paid once per instance, so memory grows with
the instance count rather than with the work being done.

Measured on that machine (macOS, physical footprint unless noted):

| Resource | Per instance | Today |
| --- | --- | --- |
| gopls | not measured | shared by gopls's own daemon (`-remote=auto`) |
| `muninn mcp` (stdio MCP server) | 55–60 MB, plus memory-mapped index pages the OS already shares | 17 copies, about 1 GB not shared |
| Mermaid engines (ADR 0004) | peak 95 MB at load, about 30 MB while loaded, held 20 s after the last render | per instance |

Pending in #122: wazero's on-disk compilation cache lets resvg load in
11 ms instead of 300 ms and lowers the warm-cache peak to 48 MB (101 MB on
the very first, cold load). It avoids repeated compile work and its
short-lived allocations; it doesn't share resident memory, because wazero
copies the cached code into each process.

Each case was handled on its own, and more will come: other stdio MCP
servers, other language servers, and engines Anvil runs in-process. A
shared process that Anvil manages is the most expensive way to share
memory. It brings discovery, start-up races, version identity, a secure
transport on three OSes, reconnects, and one crash affecting every
instance. Without a rule, each feature picks its own mechanism, and that
expensive one gets built more than once, each time differently.

## Decision

A resource that costs memory per instance is shared through the first of
these that meets its measured need, and a design that skips a step says
why:

1. **Don't hold it.** Load on first use and release when idle.
2. **Let the OS share it.** Memory-map read-only data, whose pages the OS
   shares between processes. On-disk caches (such as #122) belong here
   too, but they only cut load time and peaks, not memory held.
3. **Use the resource's own sharing.** A daemon or forwarder mode the
   resource provides, or one server reached over HTTP. When we own the
   resource, build the mode there, so every client benefits rather than
   only Anvil.
4. **An Anvil-managed shared process.** Only when 1 to 3 can't meet the
   need, and only after measuring the net saving across instances.

Anvil doesn't build a shared process now; nothing needs step 4. Applied to
today's cases:

- **muninn:** step 3, built in muninn.
- **gopls:** step 3, as today. Language servers without such a mode stay
  per instance under `lsp_idle_timeout` (step 1). Anvil won't multiplex
  language servers itself, because each client's open documents and
  versions would collide in one server.
- **Mermaid:** steps 1 and 2. A shared process would save about 30 MB per
  instance with engines loaded at the same time, not worth one shared
  failure domain for model-written input.

The first step 4 consumer gets its own ADR, which must settle at least:

- **Fallback:** which failures fall back to running per instance. Only
  ones where the call provably never ran, or that are safe to retry;
  refusals and overload are reported, and fallback is bounded so many
  instances don't fall back at once.
- **Transport:** an endpoint only the current user can reach, using each
  OS's own access control (Unix permission bits don't protect a socket on
  Windows).
- **Identity:** how a client knows it reached a compatible process (a
  version string isn't unique across dirty builds), and what happens when
  that binary has since been replaced by an upgrade.
- **Sharing contract:** per resource, what makes sharing safe: session
  isolation, cancellation, concurrency limits, mutable state and
  credentials. Matching config alone isn't enough.
- **Failure domains:** whether unrelated resources share one process.

## Alternatives

- **Build a general shared process now and move muninn and Mermaid onto
  it.** It would be designed against no real consumer, and its rules would
  harden into precedent before anything tested them. muninn is better
  served inside muninn, and Mermaid doesn't need it.
- **Decide case by case, with no rule.** What has happened so far. It works
  until two features each build a shared process with different discovery,
  identity and failure behaviour.
- **Fix the shared process's design now and defer only the code.** The
  rules above (fallback, transport, identity) can only be checked against
  a consumer, so writing them now would mean revisiting them anyway.
- **Run shared servers outside Anvil (launchd, systemd, Windows services)
  and point instances at them.** A fine personal setup for HTTP servers
  today, but as the default it pushes lifecycle onto every user and OS.

## Consequences

- Sharing work for muninn happens in the muninn repository; Anvil changes
  nothing to benefit. Its saving is projected, not measured: about 1 GB of
  footprint across 17 instances, less whatever the shared muninn process
  and any per-client forwarders cost.
- `AGENTS.md` carries the four steps as a checklist for new features that
  hold memory per instance.
- Language servers without a sharing mode keep using memory per instance
  while active; `lsp_idle_timeout` releases it after inactivity.
- Revisit this when a resource can't be served by steps 1 to 3, or when a
  step 3 mode a tool provides turns out unreliable enough that Anvil would
  rather own it.
