# Spike: what MCP tool routing really costs

**Question:** What does the catalogue design (`plans/design-2026-10-23-mcp-tool-catalogue.md`) cost in practice, compared with listing tools directly and with today's lazy `enable_mcp`? And should every MCP tool go through `call_mcp_tool`, or does it hurt when an agent calls some servers' tools directly and others through the catalogue?

**Answer:**
- **Mixing does no measurable harm.** In 96 catalogue and mixed runs across two models, no agent called a hidden tool by its direct name, sent a direct server's tool through `call_mcp_tool`, or answered wrongly. Agents switched between the two routes within a single task without trouble.
- **The choice is latency versus money, not correctness.** Routing everything through the catalogue costs about the same or less in tokens at real session lengths, but adds a search and a describe step the first time a session uses each small server. Leaving a small, heavily used server listed directly saves those steps (about 2 steps and 5 seconds) at a per-step token cost.
- **Against today's lazy loading,** the catalogue cut cold-cache cost per task by 41% on Opus and 45% on Sonnet, in sessions of 2 to 14 steps. The saving grows with session length, because an enabled lazy server's schemas are re-read on every later step.

## Setup

- **Fixtures.** Real `tools/list` output (schemas and server instructions) was captured from the configured servers with a spike-only `anvil mcp dump-tools`. Datadog is on the narrowed toolsets.

  | Server | Tools | Schema tokens (measured from lazy-enable jumps, or estimated from fixture bytes) |
  |---|---|---|
  | datadog | 85 | ~103k |
  | launchdarkly | 111 | ~62k |
  | linear | 76 | ~39k |
  | figma | 42 | ~38k (est.) |
  | slack | 27 | ~30k |
  | muninn | 10 | ~4k (est.) |
  | terminal | 8 | ~2k (est.) |

- **Fake servers.** `spike/fakemcp` replays a fixture over stdio, logs each call, and answers from `plans/spike-data/responses.json`. Nothing reaches a real service.
- **Prototype.** `internal/agent/tools/spike_catalogue.go` builds `search_mcp_tools`, `describe_mcp_tool` and `call_mcp_tool` for the servers named in `ANVIL_SPIKE_CATALOGUE`. It validates arguments with `jsonschema-go`, and the outer call ID and the inner name flow through to the wrapped MCP tool.
- **Runs.**
  - **Harness:** `plans/spike-data/run.py` runs `anvil run` headless, each run in an isolated config, data directory and empty working directory.
  - **Scale:** 8 tasks (`tasks.json`, one of them a 3-turn session) across 4 arms, 3 reps each, on `claude-opus-5-5` and `claude-sonnet-5-5`, for 192 runs.
  - **Prompts:** every prompt ends with "do not delegate" and "read-only".
  - **Metrics:** `summarise.py` and `costs.py` read each run's `step_usage`, `messages` and call log.
- **Arms.**

  | Arm | Tool list carries | Reaches the rest by |
  |---|---|---|
  | `direct` | every server's tools (387 tools, ~311k-token first prompt) | n/a |
  | `lazy` (today) | muninn, plus `enable_mcp` | enabling a whole server |
  | `catalogue` (uniform) | the 3 catalogue tools | catalogue for every server, muninn included |
  | `mixed` | muninn and terminal tools, plus the 3 catalogue tools | catalogue for datadog, launchdarkly, linear, figma, slack |

- **Cost.** Runs share Anthropic's prompt cache with each other (identical prefixes), so the recorded first-step cost depends on run order. `costs.py` charges each session's first step as a full cache write, called "cold cost" below. Prices are catwalk list prices; the account is on a subscription, so these are notional.

## Results

All 192 runs answered correctly. Means per run:

| Model | Arm | Steps | Wall (mean / median) | Prompt tokens | Cache writes | Cold cost |
|---|---|---|---|---|---|---|
| Opus | direct | 3.4 | 25.5 s / 21.6 s | 1,057k | 30k | $1.73 |
| Opus | lazy | 3.9 | 19.3 s / 18.1 s | 390k | 69k | $0.51 |
| Opus | catalogue | 6.8 | 26.2 s / 21.1 s | 304k | 31k | $0.30 |
| Opus | mixed | 5.8 | 22.0 s / 23.1 s | 287k | 30k | $0.32 |
| Sonnet | direct | 3.2 | 20.2 s / 15.0 s | 990k | 41k | $0.86 |
| Sonnet | lazy | 3.8 | 15.8 s / 13.0 s | 392k | 74k | $0.26 |
| Sonnet | catalogue | 5.8 | 18.1 s / 16.5 s | 254k | 31k | $0.14 |
| Sonnet | mixed | 5.3 | 17.7 s / 16.9 s | 267k | 31k | $0.16 |

Opus, by the routes a task crosses:

| Task (servers) | lazy | catalogue | mixed | Takeaway |
|---|---|---|---|---|
| t6 muninn only | 2 steps, 7.6 s, $0.224 | 4 steps, 12.8 s, $0.228 | 2 steps, 7.7 s, $0.236 | Uniform adds a search and a describe for a small server, at the same cost |
| t4 muninn + datadog | 4.0, 18.6 s, $0.461 | 6.7, 28.5 s, $0.331 | 6.7, 29.3 s, $0.352 | Mixing routes in one task costs no extra steps |
| t7 launchdarkly + datadog + muninn | 4.0, 27.3 s, $0.793 | 7.3, 34.5 s, $0.370 | 5.0, 24.6 s, $0.362 | Mixed is the fastest arm here |
| t8 three turns, muninn + datadog | 7.0, 39.8 s, $0.666 | 14.0, 55.0 s, $0.424 | 10.0, 39.4 s, $0.401 | Uniform re-describes on later turns, and mixed avoids that for muninn |
| t1, t2, t3 (one big server) | 3.0, ~14 s, ~$0.46 | 4 to 6, ~19 s, ~$0.27 | 4 to 6, ~17 s, ~$0.30 | Mixed pays about $0.03 more for muninn and terminal schemas it never uses |

## Findings

### 1. Mixing routes is safe
Neither model made a single routing mistake:
- none called `mcp_Datadog_*` when Datadog was in the catalogue,
- none sent muninn through `call_mcp_tool` when muninn was listed,
- none hit "unknown catalogue server".

The tool description says catalogue servers are "never callable directly by name", which appears to be enough. In `mixed`, agents used direct muninn calls and catalogue calls in the same task and the same turn (`mcp_Muninn_grep`, then `search_mcp_tools`, then `call_mcp_tool`).

### 2. Uniform versus mixed comes down to a per-server break-even
Listing a server directly costs its schema tokens `T` on every step. Routing it through the catalogue costs about `k ≈ 2` extra steps (search and describe), and only in sessions that use it, with probability `p`. At real session sizes, each extra step re-reads the whole prompt. Listing directly is cheaper when:

```
T × (S × read + write) < p × k × P_avg × read
≈  T < p × k × P_avg / S        (when S × read ≫ write)
```

From `step_usage`, top-level sessions average `S` = 99 steps and `P_avg` = 206k prompt tokens. That gives `T < p × ~4.2k` tokens. Share of top-level sessions that call each server, from `messages`:

| Server | p | Break-even T | Actual T | Cheaper by tokens |
|---|---|---|---|---|
| muninn | 0.53 | ~2.2k | ~4k | catalogue, narrowly |
| slack | 0.22 | ~0.9k | ~30k | catalogue |
| linear | 0.18 | ~0.8k | ~39k | catalogue |
| datadog | 0.11 | ~0.5k | ~103k | catalogue |
| terminal | 0.08 | ~0.3k | ~2k | catalogue |

So by money, uniform routing is at least as good for every server you have. What mixing buys is latency: for a small, often-used server like muninn, direct listing avoids about 2 steps and 5 seconds on its first use in each session. On the 3-turn task, it also avoided a second describe on a later turn.

### 3. Against today's lazy loading, the catalogue wins on cost and loses on step count
- **Cost:** cold cost fell by 41% (Opus) and 45% (Sonnet), and cache writes fell by more than half (69k to 31k), because there's no mid-session tool-list change.
- **Steps:** each task takes about 3 more steps.
- **Wall time:** mean wall time rises about 7 s on Opus (19.3 s to 26.2 s) and about 2 s on Sonnet.
- **Longer sessions:** these runs last 2 to 14 steps. In a typical 99-step session, an enabled lazy server is re-read on every later step. Enabling Datadog halfway through costs about 103k × (50 × $0.5 + $6.25) / 1M ≈ $3.20 on Opus. The catalogue's 2 to 3 extra steps cost about $0.25 to $0.35 at that prompt size.

### 4. Most invalid-argument errors come from server instructions
- **Rates:** 44 of 269 `call_mcp_tool` calls (16%) failed validation (8 of 74 and 10 of 53 on Opus, 13 of 83 and 13 of 59 on Sonnet), so the catalogue hit the design's "1 in 10" alarm.
- **Cause:** 42 of the 44 were `load_datadog_skill` or `list_datadog_skills` called without describing them first, missing their required `telemetry` field. Datadog's instructions, which search returns, tell the agent to call those two tools "before invoking other tools", so it calls them blind.
- **The rest:** 2 of 269 (0.7%, both on Sonnet) were schema mistakes: an extra `case` on muninn `grep`, and `query`/`sql` on `analyze_datadog_logs`.
- **Cost:** each costs one extra step. The error returns the schema, and every run still finished correctly.

I suggest search include the input schemas of any tools that the server instructions name (`describe` folded in for those tools), and that the alarm exclude errors caused by instructions.

### 5. Side finding: `tools_hash` changes after the first step
In every `direct` and `mixed` run, the first turn's `tools_hash` differs from every later step's, with the same `tool_count`, yet the next step reads the full prefix from cache. It never happens in `catalogue`, whose tool list has no MCP tools.

That matches the 448 "same count, `tools_changed`" rows (402 of them cache hits) in the earlier `step_usage` analysis. My guess is that an MCP tool's `Info().Parameters`, which aliases the shared `InputSchema` map, is modified in place after the first request is fingerprinted. If so, it's both a fingerprint false positive and a possible data race on a map shared across sessions. Worth its own look; not investigated here.

## Caveats

- **Easy tasks:** every arm answered every task correctly, so this measures cost and step count, not accuracy. A task with many plausible Datadog tools could separate the arms on discovery.
- **Small samples:** 3 reps per cell. Step counts are stable across reps; cost varies by about 10%.
- **Short canned results,** which shrink every arm's prompt growth the same way.
- **No subagents:** prompts forbade delegation, so catalogue use from subagents is untested. The prototype has no permission prompts, hooks or UI.
- **Extrapolation:** the long-session numbers come from the break-even model and the real session averages, not from runs.

## What this changes in the design doc

1. **Make the catalogue the default route for MCP tools,** with a per-server `direct` opt-out, rather than an opt-in `lazy_mode`. Mixing is safe, so the opt-out is only a latency choice. The break-even rule (`T < p × k × P_avg / S`) can be the documented guidance.
2. **Have search also return schemas for the tools a server's instructions name.**
3. **Count invalid-argument errors from blind calls to instruction-named tools separately** from genuine schema mistakes.
4. Expect about 2 to 3 extra steps (Opus: about 7 s mean, 3 s median) per task that touches catalogue servers. The step cost is the main thing to watch after release, and the design's Risks section should say so.

## Reproducing

The prototype, fake server and harness are on branch `spike/mcp-catalogue-cost` and aren't meant to merge. From that branch:

```bash
go build -o /tmp/anvil-spike . && go build -o /tmp/fakemcp ./spike/fakemcp
/tmp/anvil-spike mcp dump-tools plans/spike-data/fixtures datadog launchdarkly linear slack terminal figma muninn
python3 plans/spike-data/run.py --reps 3 --parallel 4 --model claude-opus-5-5
python3 plans/spike-data/summarise.py claude-opus-5-5 && python3 plans/spike-data/costs.py claude-opus-5-5
```

Fixtures and run directories are gitignored, since they hold the servers' tool descriptions and instructions. The per-run results are in `plans/spike-data/results/`.
