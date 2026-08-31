# Model routing: session-scoped defaults and evidence-based escalation

Date: 2026-08-31
Status: proposal, for discussion

## The question

Today the orchestrator runs whatever `models.large` happens to be, globally and
persistently. The stated goals were:

1. Stop defaulting to Fable 5 for everything; use Opus as the daily driver.
2. Make model switches session-scoped so sessions start from a clean default.
3. Have the system *intelligently* escalate to Fable when a task is complex,
   and de-escalate to Sonnet when it is trivial.
4. Make the defaults configurable.

Goals 1, 2 and 4 are well-supported and cheap to build. Goal 3, in the form of a
learned or self-assessed complexity router, is the one part the evidence says
not to build. There is a better version of it that already exists in this
codebase.

## Finding 0: the biggest cost driver is not the orchestrator

**None of the ten Anvil subagents declares a model.**

```
$ rg "^model:" ~/dev/helse/claude-essentials/anvil/agents/*.md
(no matches)
```

`config.ResolveAgentModel` (`internal/config/config.go:1061-1071`) falls back to
the global large model when `agent.Model == ""`. So running Fable 5 as the
orchestrator means `explorer` running eight greps runs on Fable 5, at $10/$50 per
MTok, with adaptive thinking that cannot be disabled.

The belief that "most of my subagents have a model assigned" is true of the
Claude Code plugin agents in `~/.claude/plugins/**` (which do set `model: sonnet`
/ `model: opus`). It is false of the ten agents actually wired into Anvil via
the `claude-essentials` plugin.

Illustrative cost for a session shaped like this one — one orchestrator thread of
~8 turns growing to ~120k context, plus four subagent calls — using Anthropic
list prices from `~/.local/share/anvil/providers.json`:

| Configuration | Orchestrator | Subagents | Total |
| --- | --- | --- | --- |
| All Fable 5 (today) | ~$3.20 | ~$6.40 | **~$9.60** |
| Opus 5 orchestrator, all Fable subagents | ~$1.60 | ~$6.40 | ~$8.00 |
| Opus 5 orchestrator, tiered subagents | ~$1.60 | ~$1.00 | **~$2.60** |

Assumptions: 90% cache-read hit rate on the orchestrator prefix, 50% on
short-lived subagents, ~15k orchestrator output tokens, ~8k output per subagent.
These are modelled, not measured — see Phase 3.

**Important caveat, discovered after the table was written.** This workspace
authenticates to Anthropic with subscription OAuth (`sk-ant-oat01` tokens in
`~/.local/share/anvil/anvil.json`), not a metered API key. Anvil even carries a
`FlatRate` provider flag that zeroes cost accumulation
(`internal/agent/agent.go:1448`), though it is not currently set on the
anthropic provider. So **dollar figures are not the operative cost here** and
the table should be read as a relative-intensity proxy, not a bill.

What survives without per-token billing:

- **Latency.** Thinking happens before the first token and the orchestrator
  blocks on subagent completion. Artificial Analysis puts Sonnet 5 at 182.62s
  TTFT at max effort versus Opus 5 at 67.92s and Fable 5 at 90.40s. For an
  interactive loop this is the cost that is actually felt.
- **Rate-limit budget.** Subscription plans meter usage windows. Thinking
  tokens consume that budget at $0 marginal cost, so burning weekly quota on
  grep-summarising is still a real loss.
- **Quality.** The self-review penalty (Finding 5) and the strong-orchestrator
  evidence (Finding 2) are independent of billing entirely.

Two things fall out. First, **two thirds of the token intensity is in
subagents**, and fixing that requires no Go code, only frontmatter. Second,
Fable 5 and Opus 5 pricing differ by exactly 2×, so on a metered key the
orchestrator swap is a clean 50% cut on that line with no modelling error.

## Finding 1: Opus 5 beats Fable 5 for orchestration on the public numbers

This is the part that turns "Fable feels slow and expensive" into a defensible
position rather than a preference.

| | Fable 5 | Opus 5 |
| --- | --- | --- |
| Announced | 2026-06-09 | 2026-07-24 |
| Knowledge cutoff | Jan 2026 | May 2026 |
| Input / output $/MTok | 10 / 50 | 5 / 25 |
| Cache read $/MTok | 1.00 | 0.50 |
| AA Intelligence Index | 62 | **63** |
| AA TTFT (max effort) | 90–109 s | 68 s |
| Terminal-Bench | 84.3% (v2.0) | 89.1% (v2.1) |
| Adaptive thinking | always on, cannot disable | default effort `high`, tunable |

Sources: [Anthropic pricing](https://docs.anthropic.com/en/docs/about-claude/pricing),
[model overview](https://docs.anthropic.com/en/docs/about-claude/models/overview),
[Artificial Analysis — Anthropic](https://artificialanalysis.ai/providers/anthropic).

Caveats stated plainly: the Terminal-Bench figures are different benchmark
versions and are not directly comparable; Artificial Analysis does not publish a
measurement date; Fable 5 does lead on SWE-bench Pro (80.3% vs ~69%) but that
comparison is against Opus **4.8**, not Opus 5.

Even with those caveats, Opus 5 is newer, rated marginally higher on the one
aggregate index that covers both, has a fresher cutoff, costs half as much, and
reaches first token ~25% sooner. Fable 5's documented niche is *"multi-day
autonomous agents, long-horizon work"* and it is explicitly *"not ideal for
interactive chat, tight developer loops"*. An orchestrator whose main job is
reading a prompt and firing three `task` calls is a tight developer loop.

There is also a direct argument against always-on thinking for agentic work.
["The Danger of Overthinking"](https://arxiv.org/abs/2502.08235) (Berkeley/ETH,
Feb 2025, 4,018 trajectories on SWE-bench Verified) measured overthinking scores
of 3.505 ± 1.774 for reasoning models vs 2.228 ± 0.751 for non-reasoning, with a
strong negative correlation to resolve rate (β₁ = −7.894, R² = 0.892). Their
headline: o1 at high effort scored 29.1% for $1,400; three samples at *low*
effort scored 30.3% for $1,200. The named failure modes — analysis paralysis,
rogue actions, premature disengagement — are orchestrator pathologies. Fable 5
cannot turn thinking off.

**Recommendation: `models.large` = `anthropic/claude-opus-5`.**

## Finding 2: do not downgrade the orchestrator below Opus

The temptation, having established that cheaper is often fine, is to push the
orchestrator to Sonnet. Every available source points the other way.

- Anthropic's [multi-agent research system](https://www.anthropic.com/engineering/multi-agent-research-system)
  (2025-06-13): *"a multi-agent system with Claude Opus 4 as the lead agent and
  Claude Sonnet 4 subagents outperformed single-agent Claude Opus 4 by 90.2%"*.
  Strong lead, cheap workers — and the uplift is measured against the strong
  model working **alone**. The same post lists orchestrator failure modes that a
  weaker lead makes worse: spawning 50 subagents for simple queries, subagents
  duplicating searches from vague instructions, and explicitly *"agents struggle
  to judge appropriate effort for different tasks"*.
- [MAST](https://arxiv.org/abs/2503.13657) (1,642 traces, 7 frameworks, human
  κ = 0.88): failures are dominated by coordination and verification, not raw
  capability — step repetition 15.7%, reasoning-action mismatch 13.2%,
  termination-unawareness 12.4%, disobeying task specification 11.8%, incorrect
  or absent verification 17.3% combined. All of those are orchestrator jobs.
- [Cognition, "Don't Build Multi-Agents"](https://cognition.ai/blog/dont-build-multi-agents):
  cites Edit Apply Models as a cautionary case where splitting decision-making
  (big model) from execution (small model) across models caused miscommunication.
- Anthropic's own [multi-agent orchestration guidance](https://docs.anthropic.com/en/docs/about-claude/managed-agents/multiagent-orchestration)
  documents a **Fable 5 coordinator + Sonnet 5 workers** recipe and an **Opus 5
  executor + Fable 5 advisor** recipe. Both put frontier capability at the
  decision layer.

Evidence gap, flagged: **no paper ablates orchestrator-model strength against
worker-model strength on a coding benchmark.** Nobody has published "mid-tier
planner + frontier workers vs frontier planner + mid workers". The proxies all
agree, but this is inference, not measurement.

## Finding 3: do not build a complexity router

This is the direct answer to "intelligently know when to use Fable over Opus".

**Learned routers do not demonstrate task-specific targeting on agentic
benchmarks.** [Kumar & Saminathan, arXiv 2608.14641](https://arxiv.org/abs/2608.14641)
(2026-07-28) evaluated four open-source routers over 290 frozen tasks and 2,610
locked candidate outcomes across RouterBench, BFCL v4, tau2-bench and WebArena:

> "Three routers emit constant or near-constant tier assignments... Always-Mid
> matches Aurelio exactly on three benchmarks and within 0.003 on the fourth.
> For vLLM, task-level superiority tests detect no task-specific advantage over
> a share-matched content-blind allocation."

A fixed "always mid-tier" policy matched four real routers. The savings came from
*tier mix*, not from picking correctly per task.

**Routers do not transfer.** [RouteLLM](https://arxiv.org/abs/2406.18665) achieved
3.66× cost saving at 95% GPT-4 quality on MT-Bench — but needed **70.3% of MMLU
traffic and 68.7% of GSM8K traffic** on the strong model to recover 80% of the
gap, and its BERT router scored **−21.8% vs random** before benchmark-specific
augmentation. [Hybrid LLM](https://arxiv.org/abs/2404.14618) (ICLR'24) hit "40%
fewer large-model calls, no quality drop" only in the *small*-gap model pair; in
the large-gap pair, 40% routing cost 10.3% accuracy and two of three routers were
"only marginally better than random". Opus↔Fable is a large-gap pair.

**Confidence-based deferral is provably the wrong signal here.**
[Jitkrittum et al.](https://arxiv.org/abs/2307.02764) (NeurIPS'23, Lemma 4.1):
optimal only if downstream error probability is constant across inputs. It fails
for specialist downstream models, intrinsically hard inputs, and distribution
shift. All three apply.

**Self-assessed difficulty is worse than it sounds.**
[Kadavath et al.](https://arxiv.org/abs/2207.05221) — the paper usually cited
*for* self-knowledge — found zero-shot P(True) *"is poorly calibrated and lies
near 50%"*; only few-shot P(True) with 5–20 comparison samples calibrates. A
one-shot "is this task hard?" prompt is the zero-shot regime. The same paper
found that adding a "none of the above" option *"significantly degrades both
accuracy and calibration"* — a "route to the big model" option is structurally
the same escape hatch. The [GPT-4 technical report](https://arxiv.org/abs/2303.08774)
§5 states *"post-training hurts calibration significantly"*, and every model we
would ship is post-RLHF. [Huang et al.](https://arxiv.org/abs/2310.01798)
(ICLR'24) found intrinsic self-assessment *degrades* performance: GPT-3.5 on
CommonSenseQA 75.8% → 38.1%. [AutoMix](https://arxiv.org/abs/2310.12963) calls
self-verification *"noisy and ill-calibrated"* and builds a POMDP meta-verifier
purely to denoise it.

**The one vendor who made it work needed resources we do not have.**
[Cursor Router](https://cursor.com/blog/how-cursor-router-works) trained Compass
on 600k+ live requests with online A/B across millions more. Compass reaches 96%
accuracy at the high-complexity end but only **71% at the low-complexity end** —
i.e. it is much better at spotting hard than at safely spotting easy, which is
precisely the direction that matters for de-escalating to Sonnet. They also
concede *"no model dominates every kind of work"* and that offline evals *"miss
costs that production traffic captures, including cache misses caused by
switching models."*

GitHub Copilot's Auto is the cautionary tale: shipped Sep 2025, only gained
task-based routing in [May 2026](https://github.blog/changelog/2026-05-20-auto-model-selection-now-routes-based-on-your-task-in-vs-code/),
publishes no quality data beyond "no quality regression", offers a 10% billing
discount for using it, and was found by [Visual Studio Magazine](https://visualstudiomagazine.com/articles/2026/02/06/why-copilots-auto-mode-for-ai-models-ignores-your-actual-task.aspx)
to have optimised for server capacity rather than task complexity for ~8 months.
The most consistent community complaint about both routers is not "it picks
badly" — it is *"I can't tell what ran, so I can't debug the result."*

**Conclusion: no classifier. Escalation must be explicit, visible, and rare.**

## Finding 4: mid-conversation switching has a real, quantifiable cost

If switching is going to be manual and session-scoped, it needs to be *cheap
enough to do once* and *expensive enough that nothing does it automatically*.

- **Thinking blocks are model-bound.** Anthropic's
  [extended thinking docs](https://platform.claude.com/docs/en/build-with-claude/thinking):
  *"When you switch between any two models... strip `thinking` and
  `redacted_thinking` blocks from prior assistant turns. Thinking blocks are tied
  to the model that produced them. Other models silently ignore them rather than
  rejecting the request, but ignored blocks still add input tokens."* So a switch
  requires mutating history — which itself invalidates the cache.
- **Cache invalidation cascades.** [Prompt caching docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching):
  hierarchy is `tools` → `system` → `messages`, and a change at one level
  invalidates that level and all below. *"Modifying tool definitions... invalidates
  the entire cache."* Anvil bakes the model name into the system prompt
  (`internal/agent/coordinator.go:762`), so a swap changes the system block too.
  Anvil's lazy-MCP filtering already mutates the tool list per turn.
- **The arithmetic.** Cache read is 0.1× input, 5-minute cache write is 1.25× —
  a 12.5× step change on the prefix for the switching turn. On a 150k-token Opus 5
  context: ~$0.075 as a cache read becomes ~$0.94 as a cache write. Fine once per
  session. Ruinous per-turn.
- **Both shipping vendors treat this as first-class.** Cursor: *"Cursor Router is
  cache-aware in both how it is trained and evaluated."* GitHub routes *"along
  natural cache boundaries."*

Caveat: Anthropic's invalidation table has no explicit "model change" row.
OpenAI's [caching guide](https://developers.openai.com/api/docs/guides/prompt-caching)
is explicit (*"A different model can use different weights and caching
behavior"*), and Cursor's production data treats it as measured fact. Treat
"switching loses the prefix cache" as well-supported in practice, weakly
documented by Anthropic.

Evidence gap, flagged: **no published measurement of the quality cost of
mid-conversation switching** — no A/B on style discontinuity, tool-format drift,
or plan abandonment. The mechanisms are documented; the magnitude is not.

## Finding 5: "Sonnet reviews better than Opus" is real, but not a capability inversion

There is **no published head-to-head Sonnet-vs-Opus code review evaluation** from
any lab, vendor, or academic group. Every review vendor that has disclosed its
model uses a frontier model: Greptile (*"the main review agent is usually a
frontier model"*, small models only for reply classification), CodeRabbit
(frontier for review, Nemotron for routing only), Graphite Diamond (Claude, for
*"the lowest false positive rate of any LLM we tested"*).

But two measured effects explain the observation without a capability inversion,
and they point at different fixes:

1. **Over-flagging scales with recall.** [CriticGPT](https://arxiv.org/abs/2407.00215)
   (OpenAI, Jun 2024): *"models which hallucinate bugs more often are also more
   likely to catch human inserted and previously detected bugs."* On tasks humans
   rated flawless, the critic flagged problems **24%** of the time vs **6%** for a
   second human. [Greptile](https://greptile.com/blog/model-inversion) (2026-07-21)
   measured Opus 4.7 emitting **7–8 comments per review** with hedging and praise
   versus GPT 5.5's **1–2**, spending 59.4% of trace tokens scoping rather than
   investigating. Separately, Greptile found **79% of comments were nits**, and a
   clustering filter lifted the address rate from 19% to 55%. A bigger reviewer
   feels worse because it says more, not because it knows less.
2. **Models are worse at reviewing their own output.** Same Greptile study, 500
   Claude-authored + 500 Codex-authored PRs, ~1,500 ground-truth high-severity
   comments: Opus 4.7 recall **53.7%** on Claude-authored vs **62.0%** on
   Codex-authored — an ~8pp self-review penalty. If Anvil writes the code on
   Opus, an Opus reviewer is self-reviewing.

Meanwhile the LLM-as-judge literature runs *against* smaller reviewers:
[MT-Bench](https://arxiv.org/abs/2306.05685) found verbosity-bias failure rates of
8.7% for GPT-4 vs 91.3% for Claude-v1/GPT-3.5, and self-preference bias of +10%
vs +25%. Stronger judges are consistently less biased.
[CriticBench](https://arxiv.org/abs/2402.14809) is the only genuine
smaller-beats-larger finding (Mistral-7B critique F1 55.70% vs LLaMA-2-70B
52.48%) and the effect is non-monotonic in the mid-range, with GPT-4 still on top
at 81.62%.

**So: putting `reviewer` on Sonnet 5 is a good call, but for the anti-self-review
reason and the cost reason, not because Sonnet is a better critic.** The larger
win is prompt-side nit suppression, which is worth more than the model choice
(19% → 55% address rate).

## Plan

### Phase 0 — subagent model defaults (shipped 2026-08-31)

Set global defaults in `~/.config/anvil/anvil.json` (it had no `models` key at
all, so it was silently inheriting catwalk's stale
`default_large_model_id: claude-sonnet-4-6`):

```json
{
  "models": {
    "large": { "provider": "anthropic", "model": "claude-opus-5" },
    "small": { "provider": "anthropic", "model": "claude-haiku-4-5-20251001" }
  }
}
```

Then assign per-agent models. This is role-based static routing — the Aider
architect/editor and Claude Code `opusplan` pattern, and the only design in
this space with a published positive number: Aider measured o1-preview alone at
**79.7%** versus o1-preview architect + o1-mini editor at **85.0%**
([2024-09-26](https://aider.chat/2024/09/26/architect.html)).

Frontmatter also gained `reasoning_effort` and `think` alongside `model`, so
effort is available as a knob. **No agent sets them.** Effort is left at each
model's vendor default (`high` for the Anthropic 5-series).

An earlier revision of this document assigned per-agent effort levels
(`low` for search, `medium` for execution, `high` for reasoning). That was
stripped, for three reasons:

1. **Anthropic 5-series thinking is adaptive.** Effort biases the budget, it
   does not fix it — the model self-limits on easy work. So "a high-effort
   model on a trivial grep burns thinking tokens" is largely false, and the
   expected saving was smaller and more variable than claimed.
2. **`high` is the vendor default.** Deviating from it needs positive evidence,
   and there is none specific to effort-versus-agent-role. The overthinking
   paper is the closest, and it measures effort *and* sampling strategy
   together on o1, not per-role effort on Claude.
3. **The levels were set on judgement, not measurement** — the exact
   vibes-based reasoning this document exists to avoid. `explorer` at `low` was
   the worst of them: it produces the map everything downstream depends on, so
   a cheap wrong map is expensive. That is the same argument used below to
   reject Haiku for `explorer`, and it was not applied to effort.

| Agent | Model | Rationale |
| --- | --- | --- |
| `explorer` | `claude-sonnet-5` | Mechanical search; tools already restricted to glob/grep/ls/view/lsp. |
| `librarian` | `claude-sonnet-5` | Fetch and summarise external docs. |
| `fixer` | `claude-sonnet-5` | The Aider "editor" role: bounded execution against a decided spec. |
| `tester` | `claude-sonnet-5` | |
| `designer` | `claude-sonnet-5` | Delegates hard calls to `oracle` already. |
| `convention-reviewer` | `claude-sonnet-5` | Mostly pattern matching against stated rules. |
| `reviewer` | `claude-sonnet-5` | Different model from the Opus author → avoids the ~8pp self-review penalty. |
| `planner` | `claude-opus-5` | Specification quality is where MAST says failures concentrate. |
| `devils-advocate` | `claude-opus-5` | Adversarial reasoning; judge-bias literature favours stronger. |
| **`oracle`** | **`claude-fable-5`** | **The escalation valve — see Phase 2.** |

Sonnet 5 rather than Haiku 4.5 for `explorer`: Haiku is only 2× cheaper
($1/$5 vs $2/$10) but is a 2025 model capped at 200k context, and a bad
codebase map poisons everything downstream. Sonnet 5 is the better point on
that curve. Sonnet 5 rather than Sonnet 4.6 throughout: it is both cheaper
($2/$10 vs $3/$15) and better, with a native 1M window.

Verified live — `--debug` now logs `Resolved agent model` per agent:

```
agent=orchestrator     model=anthropic/claude-opus-5   reasoning_effort=high
agent=explorer         model=anthropic/claude-sonnet-5 reasoning_effort=high
agent=librarian        model=anthropic/claude-sonnet-5 reasoning_effort=high
agent=devils-advocate  model=anthropic/claude-opus-5   reasoning_effort=high
agent=oracle           model=anthropic/claude-fable-5  reasoning_effort=high
```

#### Two bugs this surfaced

Both were silent cost escalations in the same direction — cheap specialist
quietly promoted to the most expensive model available.

1. **`ResolveAgentModel` discarded global tuning.** It rebuilt `SelectedModel`
   from catwalk defaults, so declaring a model on an agent dropped the user's
   `think`, `temperature`, `top_p`, `top_k` and penalties. Harmless while no
   agent declared a model; load-bearing the moment they all do. Now the global
   large model is the base and the agent's model is layered over it, with
   provider options dropped only on a provider change.
   (`internal/config/config.go:1072`)

2. **A hallucinated `model` override upgraded the agent instead of failing.**
   The `task` tool's `model` field had no `omitempty`, so fantasy marked it
   **required** in the JSON schema — the orchestrator was forced to invent a
   value on every delegation. On the first test run it produced
   `anthropic/claude-haiku-4-5` (the real ID carries a `-20251001` suffix),
   `ResolveAgentModel` errored, and `buildAgentModels` fell back to the
   *global large* model. Net effect: asking for Haiku silently ran Opus 5, with
   no log line. Fixed three ways — `omitempty` so the field is genuinely
   optional, validation at the tool boundary that drops an unresolvable
   override and keeps the agent's configured model, and a warning on the
   remaining fallback path.
   (`internal/agent/task_tool.go:26`, `:76`, `internal/agent/coordinator.go:1158`)

The second one is worth dwelling on: it is a concrete instance of the opacity
failure mode that dominates community complaints about Cursor Auto and Copilot
Auto. The routing was wrong *and* invisible. The `Resolved agent model` debug
line exists because of it.


### Phase 1 — session-scoped model state (the ask)

Currently `handleSelectModel` writes `config.ScopeGlobal`
(`internal/ui/model/ui.go:2522`), so a switch in one session leaks into every
other session and every future session. That is the bug.

**Do not add a `sessions` column.** The codebase already has a better,
established pattern. `deriveLazyMCPState` (`internal/agent/lazy_mcp.go:25`)
derives branch-scoped state by replaying message history, and — critically —
Anvil *already writes* a `MessageTypeModelChange` breadcrumb on every large-model
switch (`internal/ui/model/ui.go:2537-2560`). Nothing reads it. It is display-only
today.

1. Add `deriveSessionModel(messages) *config.SelectedModel` in
   `internal/agent/`, mirroring `deriveLazyMCPState`: replay
   `MessageTypeModelChange` entries chronologically, last one wins, nil if none.
2. Change `handleSelectModel` to stop writing `ScopeGlobal`. The breadcrumb
   becomes the source of truth for the session.
3. Have `coordinator.Run` (already calls `UpdateModels` on every run at
   `internal/agent/coordinator.go:353-356`) consult the derived session model,
   falling back to `cfg.Models[large]`.
4. `models.large` / `models.small` in `anvil.json` become the per-session
   *starting point*, restored on every new session. Which is exactly the
   requested behaviour: fresh default per session, configurable, switches
   isolated.

Advantages over a column: branch-scoped rather than session-scoped (survives
branching and compaction), no migration, no new writer, reuses a pattern with
existing tests.

Two things to fix while in there:

- `ConfigStore.pinPreferredModelLocked` (`internal/config/store.go:430`) pins the
  choice **process-globally**. Session-scoped resolution must not read through it.
- `ResolveAgentModel` (`internal/config/config.go:1097-1119`) **discards** the
  user's `think`, `temperature` and sampling settings when an explicit
  `provider/model` override is used — it rebuilds `SelectedModel` from catwalk
  defaults only. With Phase 0 assigning models to every agent, this silently
  becomes load-bearing. It should inherit non-model fields from `models.large`.

### Phase 2 — escalation without a router

**Option A (recommended): ship nothing. Escalation is already delegation.**

With `oracle` on Fable 5, "escalate to Fable for a hard thinking task" becomes
"delegate to `@oracle`". This is strictly better than switching the orchestrator:

- Zero cache cost. The subagent gets its own context; the orchestrator's 150k
  prefix stays warm. Compare ~$0.94 for a mid-conversation switch.
- No thinking-block stripping, no system-prompt rewrite, no `PrepareStep` changes.
- It matches Anthropic's documented **Opus 5 executor + Fable 5 advisor** recipe.
- The routing decision is already specified in
  `~/dev/helse/claude-essentials/anvil/agents/oracle.md` in behavioural terms
  ("problems persist after 2+ fix attempts", "security or data integrity
  decisions") rather than as a self-assessed difficulty score — which is the one
  form of self-assessment the literature does not condemn, because it keys off
  observable events rather than introspection.
- It is visible in the transcript, which is the single most common complaint
  about Cursor Auto and Copilot Auto.

The one change worth making: `oracle.md`'s `delegate_when` should name the
escalation explicitly, and `delegates_to: []` means it is a leaf — good, no
recursive Fable spend.

**Option B (only if A proves insufficient): an explicit, capped `switch_model`
tool.**

Gate it hard, because everything in Findings 3 and 4 applies:

- Permission-gated, like any other mutating tool.
- Hard cap of 1–2 switches per session, enforced in the tool not the prompt
  (Anthropic: *"agents struggle to judge appropriate effort"*).
- Emits a `MessageTypeModelChange` breadcrumb, so Phase 1's derivation picks it up
  for free and the user sees it.
- Implementation notes: `largeModel` is snapshotted once at
  `internal/agent/agent.go:229`, so `PrepareStep` must re-read
  `a.largeModel.Get()`; the system prompt embeds the model name
  (`coordinator.go:762`) so the swap must also re-`SetSystemPrompt`; prior
  `thinking` blocks must be stripped from history.

**Cheaper escalation to try before either: raise reasoning effort, or sample
twice.** The Overthinking paper's k=3-at-low-effort beating k=1-at-high-effort at
15% lower cost is the strongest published result on escalation-within-a-model.
Note that `output_config.effort` changes also invalidate message-block cache, so
this is not free either.

**De-escalation to Sonnet for trivial tasks: do not automate.** Compass is only
71% accurate at the low-complexity end with 600k requests of training data. Route
trivial work to a cheap *subagent* instead — which Phase 0 already does.

### Phase 3 — measure it, or none of this was evidence-based either

The whole premise of this document is not shipping on vibes, so the routing
policy needs the same standard. On subscription auth the metric of record is
**wall-clock and rate-limit budget, not dollars**.

Anvil already records `Model` and `Provider` per message
(`internal/agent/agent.go:446-447`) and cost per session. What is missing is the
rollup.

1. **Settle the Sonnet-slower-than-Opus question first** (weakness 6 above).
   Fix a task, run the same subagent on Sonnet 5 and Opus 5 at default effort,
   several trials each, and compare wall-clock and output tokens. If Sonnet 5 is
   consistently slower, the whole Sonnet tier needs rethinking — either revert
   to Opus 5 for latency-sensitive agents, or reintroduce a *measured* effort
   override rather than the guessed one that was stripped. Nothing else in this
   phase matters until this is answered.
   - A first attempt at this was abandoned mid-run for taking too long, which is
     itself weak evidence that these tasks are slow enough to matter. Two
     `explorer`-at-`low` runs completed in **37.6s** and **33.8s** wall; the
     `high` arm never finished. Not a result — a note that the experiment needs
     to be designed to terminate.
2. Extend `anvil stats` with a breakdown by model **and by agent name**: token
   counts, wall-clock, and cost where a metered key is in use. Requires
   attributing subagent messages to their agent, which the coordinator knows.
3. Capture a baseline of representative sessions, then re-run comparable work
   and compare latency, token intensity, and how often `oracle` is invoked.
4. The quality signal to watch is not a benchmark, it is **how often you manually
   escalate**. If you find yourself reaching for Fable in the orchestrator often,
   Finding 1 is wrong for your workload and the default should move. If you never
   do, consider whether `planner` and `devils-advocate` also drop to Sonnet 5.

## Explicitly not doing, with reasons

| Rejected | Why |
| --- | --- |
| Learned complexity classifier | "Always-Mid" matched four routers on agentic benchmarks (arXiv 2608.14641). Large-gap routers ≈ random (Hybrid LLM). Cursor needed 600k live requests. |
| Ask the model "is this hard?" | Zero-shot P(True) ≈ 50% and poorly calibrated (Kadavath); RLHF degrades calibration further (GPT-4 report §5); intrinsic self-assessment degrades outcomes (arXiv 2310.01798). |
| Sonnet orchestrator | Anthropic +90.2% is strong-lead/cheap-worker; MAST failures are specification and verification, i.e. orchestrator jobs; Cognition warns against split decision-making. |
| Automatic per-turn model switching | 0.1× → 1.25× cache flip ≈ 12.5× on the prefix; thinking blocks are model-bound; both shipping vendors constrain switching to cache boundaries. |
| `sessions` table column for model | `deriveLazyMCPState` pattern is branch-scoped, needs no migration, and the `MessageTypeModelChange` breadcrumbs are already being written. |
| Confidence-based deferral | Provably suboptimal unless downstream error rate is input-independent (arXiv 2307.02764, Lemma 4.1). |

## Where this argument is weakest

Stated so it can be attacked:

1. **No orchestrator-strength ablation exists.** The "strong orchestrator"
   recommendation rests on Anthropic's single internal eval plus indirect MAST and
   Aider evidence. Nobody has published mid-planner/frontier-workers vs
   frontier-planner/mid-workers on a coding benchmark.
2. **No orchestration-specific benchmark exists.** SWE-bench, Terminal-Bench and
   tau-bench all measure end-to-end task completion, not planning or delegation
   quality in isolation. The Opus-5-over-Fable-5 claim leans on an aggregate index
   with no published measurement date.
3. **The cost table is modelled, not measured**, and this workspace is on
   subscription auth so dollars are not the operative currency anyway. Cache-hit
   rates are assumed. Phase 3 exists to replace it.
4. **Cursor's numbers are self-reported against a satisfaction proxy**, not
   correctness — a router optimised on "the user moved on" can be biased toward
   plausible-looking cheap output.
5. **Sonnet-for-review rests on mechanism, not measurement.** No head-to-head
   exists. If the over-flagging explanation is right, the better fix is prompt-side
   nit suppression, and the model choice is secondary.
6. **Sonnet 5 may be *slower* than Opus 5 at default effort, which would
   invert the latency argument for cheap subagents.** Artificial Analysis
   reports Sonnet 5 at **182.62s** TTFT at max effort against Opus 5 at
   **67.92s** — the smaller model spends longer thinking. Anvil sets no effort
   overrides, so subagents inherit `high`. If that ordering holds at `high`,
   tiering down to Sonnet 5 buys token intensity and rate-limit headroom but
   costs wall-clock, which on subscription auth is the cost that actually
   matters. This is the single most likely way Phase 0 is wrong. It is also
   directly measurable and should be the first thing Phase 3 checks — before
   any of the cost accounting.
