# Orchestrator true cost: Fable 5 vs Opus 5, time and money per turn

Follow-up to `design-2026-08-31-model-routing.md`, which measured cost per
million input tokens. That metric answers "what does a token cost", not
"what does getting something done cost". This document measures the
second one, in both dollars and wall-clock time, at p50/p95/p99.

Scope: the **orchestrator only**. Subagent spend is subtracted from the
parent session rather than rolled up, so every dollar below was spent by
the top-level agent.

## The headline

Fable 5 costs 2x Opus 5 per token. It does **not** cost 2x per turn — it
costs about the same, because Opus 5 spends 2.3x as many actions per turn.
And Fable finishes a turn in roughly half the wall time.

| Per completed turn | Fable 5 | Opus 5 | Opus 4.6 |
| --- | --- | --- | --- |
| turns measured | 1,392 | 402 | 993 |
| **wall** p50 | **86s** | 2.5m | 56s |
| wall p95 | 15.5m | 21.2m | 10.8m |
| wall p99 | 47.2m | 44.5m | 37.3m |
| **generation** p50 | **58s** | 2.0m | 30s |
| generation p95 | 10.4m | 16.0m | 6.6m |
| generation p99 | 21.8m | 33.5m | 15.7m |
| **cost** p50 | $1.76 | $1.87 | $0.42 |
| cost p95 | $11.29 | $14.49 | $3.21 |
| cost p99 | **$27.45** | **$22.52** | $6.03 |
| steps (API calls) p50 | 4 | 9 | 3 |
| tool calls p50 | 4 | 10 | 2 |

Read the p99 row carefully: it is the one place the two models cross over.
Fable's money tail is *worse* ($27.45 vs $22.52) while its time tail is
slightly better. On a hard turn, Fable's 2x token price stops being
absorbed by Opus's extra steps.

## Where the parity comes from

Per unit of work, Fable is exactly as expensive as its price sheet says:

| Per completed turn (means) | Fable 5 | Opus 5 | Opus 4.6 |
| --- | --- | --- | --- |
| tool calls | 7.0 | 16.4 | 5.5 |
| steps | 6.7 | 14.8 | 5.2 |
| bash calls | 3.2 | 10.3 | 1.7 |
| edits | 0.9 | 1.8 | 0.6 |
| seconds per API call (median) | 14.8 | 14.0 | 9.3 |
| **cost per tool call (median)** | **$0.475** | **$0.218** | $0.158 |

$0.475 / $0.218 = 2.18x, against a list-price ratio of 2.0x. There is no
hidden per-token efficiency in Fable. The per-turn parity is entirely a
composition effect: Opus 5 does more, more cheaply.

Per-call latency is the same for both (~14s), so Opus's longer turns are
step count, not slower generation.

## Same finding, measured a second way

The per-turn cost above is an allocation of each session's recorded cost
across its turns. Session totals need no allocation at all, and they say
the same thing:

| Whole session, single-model roots | Fable 5 | Opus 5 | Opus 4.6 |
| --- | --- | --- | --- |
| sessions | 191 | 95 | 136 |
| turns p50 | 3 | 3 | 4 |
| cost p50 | $4.51 | $5.33 | $1.77 |
| cost p95 | $170.93 | $115.55 | $47.91 |
| cost p99 | $407.44 | $233.09 | $100.97 |
| generation time p50 | 9.6m | 13.4m | 5.7m |

Same crossover: Fable is marginally cheaper at the median and materially
more expensive in the tail.

Blended with the subagent spend each orchestrator triggered:

| | orchestrator | subagents | total | subagent share |
| --- | --- | --- | --- | --- |
| Fable 5 | $3.30/turn | $0.42/turn | $3.72/turn | 11% |
| Opus 5 | $3.77/turn | $0.56/turn | $4.33/turn | 13% |
| Opus 4.6 | $0.94/turn | $0.18/turn | $1.12/turn | 16% |

## Controls

The obvious objection is task mix: Opus 5 is the current default and gets
the hard work, Fable was the default for everything in July and August.
Three controls, and the effect survives all of them.

**Same harness era (Jul+Aug 2026 only).** Fable 1,330 turns: wall p50 87s,
$1.80, 4 steps. Opus 5 150 turns: wall p50 2.3m, $1.85, 8 steps. Unchanged.

**Stratified by user prompt size**, which is chosen before the model runs
and so cannot be an outcome of it:

| prompt | model | n | wall p50 | $ p50 | steps p50 |
| --- | --- | --- | --- | --- | --- |
| <40 tok | Fable 5 | 1,020 | 80s | $1.71 | 4 |
| <40 tok | Opus 5 | 305 | 2.3m | $2.00 | 9 |
| 40–150 | Fable 5 | 271 | 1.6m | $2.16 | 4 |
| 40–150 | Opus 5 | 66 | 3.2m | $2.51 | 10 |

**Paired within-session switches** — same session, same repo, same task,
model changed mid-thread. 13 sessions. Fable/Opus 5 median ratios:
wall 0.84x, generation 0.66x, steps 0.64x, cost 0.86x.

Split by direction, the pairs expose the selection bias directly:

| direction | n | steps | cost |
| --- | --- | --- | --- |
| opus → fable | 6 | 0.29x | 0.72x |
| fable → opus | 7 | 0.90x | 1.41x |

I switch *down* to Fable for cheap work and *up* to Opus when a turn is
already going badly. Both halves are biased, in opposite directions, and
their midpoint agrees with the unpaired numbers.

## The quality term, which is not measured here

Cost parity assumes equal outcomes, and outcomes are not in this data.
The one available proxy is how often I immediately re-prompt after a turn
finishes cleanly (<45s gap):

| model | immediate re-prompt rate |
| --- | --- |
| Fable 5 | 23% (n=1,211) |
| Opus 5 | 16% (n=307) |
| Opus 4.6 | 39% (n=857) |

This cuts against Fable, but it is a weak signal — a fast follow-up is as
likely to be "good, now the next thing" as "no, try again". It is enough
to say the quality term is not obviously zero, and not enough to size it.

## Harness-level finding, equal across models

Only 52–56% of turns reach a clean `end_turn`. The rest:

| last finish reason | Fable 5 | Opus 5 |
| --- | --- | --- |
| `end_turn` | 56% | 52% |
| `tool_use` (turn abandoned mid-loop) | 35% | 38% |
| `canceled` | 8% | 10% |
| `error` | 0.9% | 0.8% |

Charging all of that against the turns that did complete:

| model | per completed turn | per attempted turn |
| --- | --- | --- |
| Fable 5 | $6.76 | $3.81 |
| Opus 5 | $7.31 | $3.78 |
| Opus 4.6 | $1.60 | $0.95 |

The ~44% of spend on turns that never closed cleanly is the largest single
cost lever in this document, and it is model-independent. It is a harness
and interaction problem, not a routing problem.

## Method

Source: `~/.local/share/anvil/anvil.db`, 546 root sessions, 90,393
messages, 5,236 orchestrator turns, 2026-05-17 to 2026-09-10. Scripts in
`scratch/`.

**Turn** = one user message plus every assistant and tool message until
the next user message. Assistant messages are walked by
`parent_message_id`, so branched sessions are priced against their own
ancestry, with a fallback to chronological order for pre-tree-migration
sessions (20 of 469).

**Wall time** = last assistant `finished_at` in the turn minus the user
message `created_at`. Includes tool execution, subagent runs, and any
time spent waiting on a permission prompt. **Generation time** = sum of
per-call `finished_at - created_at`, which excludes all of that. Both are
reported because the gap between them is real: 48% overhead for Fable,
25% for Opus 5.

**Cost.** `sessions.cost` accumulates from provider-reported usage and is
exact, but it is only stored per session, and
`coordinator.updateParentSessionCost` rolls child sessions into the
parent. Orchestrator cost = `session.cost - Σ child.cost`. Per-turn cost
allocates that total across the session's turns in proportion to
per-call (context x input price + output x output price). Session-level
tables above use the unallocated figure and reach the same conclusion, so
the allocation is not load-bearing.

## Caveats that matter

- **These are notional API-equivalent dollars, not a bill.** The provider
  is an Anthropic OAuth subscription; `providers.json` prices
  `claude-fable-5` at $12/$60 per 1M and `claude-opus-5` at $6/$30, both
  1.2x list. The ratio is what is being compared, and it is exact.
- **Cache tokens are priced at zero** for both models in
  `providers.json` (`cost_per_1m_in_cached` and `cost_per_1m_out_cached`
  are 0), so recorded cost omits cache read and write charges. Empirically
  recorded cost lands within ~1.0–1.7x of full-context-per-call pricing.
  The omission is identical for both models, so absolute dollars are
  understated and the ratio is unaffected.
- **Thinking text is not stored** (only the signature blob), so output and
  context token estimates undercount, by roughly 2x against
  `sessions.prompt_tokens`. This affects the intra-session allocation, not
  the session totals.
- Opus 5 ran with `reasoning_effort: high` and `max_tokens: 128000` per
  `anvil.json`. Fable's settings during its era are not recoverable from
  the DB. Some of the step-count gap may be configuration rather than
  model.
- Timestamps are second-resolution, and `created_at` is in **seconds**
  despite the schema comment saying milliseconds.

## What I would actually conclude

1. **Cost is a wash at the median; Opus 5 wins the tail.** If the fear was
   "Fable is 2x, so it doubles the bill", that is wrong — per-turn cost is
   within 6%. If the hope was "Fable's efficiency makes it cheaper", that
   is also wrong above p95.
2. **Fable buys latency, and it is the clearest effect in the data.**
   86s vs 2.5m at p50, 58s vs 2.0m of generation. For interactive work
   that is the difference that gets felt.
3. **The step-count gap is the real question, and this data cannot close
   it.** Opus 5 taking 2.3x the actions is either thoroughness worth
   paying for or waste. The re-prompt proxy leans thoroughness. Sizing it
   needs an outcome measure, not more usage telemetry.
4. **Stop optimising routing before fixing abandonment.** 44% of spend
   goes to turns that never reach `end_turn`, identically on both models.
   That dwarfs the Fable/Opus delta.
