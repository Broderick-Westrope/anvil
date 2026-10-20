# 0005: Share memory across Anvil instances with the cheapest mechanism that works

**Status:** Proposed
**Date:** 2026-10-20

## Context

People run many Anvil instances at once, one per pane or worktree. On
2026-10-20 one machine had 19, averaging about 97 MB RSS each. Anything an
instance loads for itself is paid once per instance, so memory grows with
the instance count rather than with the work being done. Three cases have
come up so far:

| Resource | Per instance | How it's shared today |
| --- | --- | --- |
| gopls | grows with the module; not measured | gopls's own daemon (`-remote=auto`) |
| `muninn mcp` (stdio MCP server) | about 57 MB physical footprint | not shared: 17 copies, about 1 GB |
| Mermaid engines (ADR 0004) | about 30 MB loaded, 48 MB peak | resvg's compiled code via wazero's on-disk cache (#122) |

Each was solved, or not, on its own. More will come: other stdio MCP
servers, other language servers, and engines Anvil runs in-process, such as
local models, tokenizers or code indexes. Without a rule, each feature
picks its own mechanism, and the expensive one (a shared process Anvil
manages) gets built more than once.

Sharing a process across instances is costly in ways that don't show in a
memory table: it needs discovery, start-up races, version skew between
instances, a transport on three OSes, reconnects, and a crash that now
takes a feature away from every instance at once.

## Decision

A resource that costs memory per instance is shared through the first of
these that works, and a design that skips a rung says why:

1. **Don't hold it.** Load on first use and drop when idle, as the Mermaid
   engines (20 s) and LSP clients (`lsp_idle_timeout`) already do.
2. **Let the OS share it.** Keep derived artefacts on disk where every
   process reads them (wazero's compilation cache) and memory-map
   read-only data, whose pages the OS shares between processes for free.
   Caches that hold executable code live only in the user's own cache
   directory, never a shared temp directory.
3. **Use the resource's own sharing.** A server's daemon or forwarder mode
   (gopls `-remote=auto`), or one server reached over HTTP. Where we own
   the tool, add the mode to the tool, so every client benefits, not just
   Anvil.
4. **Run it in an Anvil host.** Only when 1 to 3 can't work and the saving
   is measured.

No consumer needs rung 4 today, so the host is not built yet. When the
first one does, it follows these rules, so later consumers reuse it rather
than building their own:

- **One host per user per Anvil build.** It's the same binary run as a
  hidden `anvil host` command, and its socket path includes the build
  version. Instances of different versions never share a host, so there's
  no wire compatibility to maintain, and in-process services reuse Anvil's
  own packages.
- **Started on demand, gone when unused.** The first client takes a lock
  with `internal/lock`, starts the host and connects. The host exits when
  it has no clients and nothing loaded.
- **Shared resources are keyed.** A key is the resource's identity plus a
  hash of everything that changes its behaviour (config, env, working
  directory where it matters). Two instances share only when their keys
  match. Each resource starts on first use, is counted per client and is
  dropped when idle.
- **Sharing is an optimisation, never a requirement.** Every shared
  resource keeps its in-process path. If the host can't be reached, crashes
  or refuses, the instance logs a warning and runs it in-process.
- **Unix domain sockets on every OS**, in a per-user `0700` directory.
  Windows 10 and Go both support them.
- **Opt-in per resource.** Stdio MCP servers share only when their config
  says so. A server that calls back into the client (roots, sampling,
  elicitation) or depends on the caller's working directory can't be
  shared.

Language servers are not multiplexed by the host. Each client's open
documents and versions would collide in one server, so they share only
through a mode the server provides (rung 3).

## Alternatives

- **Build the host now and move muninn and Mermaid onto it.** About two
  weeks for the host plus one to two for stdio MCP multiplexing, to save
  memory that rung 3 (muninn) and rungs 1 and 2 (Mermaid) already save
  more cheaply. It would also make every instance depend on a process
  shared by all of them for no current gain.
- **Decide case by case, with no rule.** What happened so far. It works
  until two features each build a shared process with different discovery,
  versioning and failure behaviour.
- **One host per machine, shared across Anvil versions.** Fewer processes
  during upgrades, but it needs a versioned protocol and a policy for which
  version's code runs. Version-keyed hosts trade a second process during
  an upgrade for having none of that.
- **Run shared servers outside Anvil (launchd, systemd, Windows services)
  and point instances at them.** Works for a person tuning one machine,
  and is a fine use of rung 3 for HTTP servers today. As the default it
  pushes lifecycle onto every user and OS.

## Consequences

- muninn should gain a gopls-style mode: `muninn mcp` becomes a thin stdio
  forwarder to a muninn daemon it starts on demand. That shares one index
  across every MCP client, not only Anvil, with no Anvil change.
- Mermaid stays in-process. Moving it onto a host would save about 30 MB
  per instance rendering at the same moment, which isn't worth a shared
  failure domain for model-written input.
- Language servers without a daemon mode stay per instance. Their memory
  is bounded by `lsp_idle_timeout`, which is the lever if they grow.
- The first rung 4 consumer pays for the host (an estimated 1.5 to 2.5k
  lines with tests on three OSes) and must measure the saving first. A
  third-party stdio-only MCP server that is expensive and stateless is the
  likeliest candidate.
- Revisit this if a rung 4 consumer appears, if the fallback rule makes a
  feature impossible to build, or if per-version hosts stop being cheap.
