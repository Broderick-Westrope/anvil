# Is the three-reviewer `/review` setup worth it?

The `/review` command runs three AI code reviewers in parallel on the same change:

- **Sonnet** — a faster, cheaper model, aimed at broad coverage
- **Opus** — a slower, more expensive model, aimed at deep analysis
- **Convention** — an Opus-based reviewer focused purely on whether the code follows our conventions

Each finding in the merged review is tagged with which reviewer(s) caught it (e.g. `[Sonnet + Opus]`). I dug through the Anvil database of past sessions to see whether all three reviewers actually earn their keep.

> **Note on rigour:** a first pass of this analysis accidentally mixed in other uses of the reviewer agents (ad-hoc single reviews, spec reviews, plan reviews). An adversarial review caught this, and all numbers below were re-computed on a clean dataset: only runs where exactly the three `/review` reviewers were launched together, and only sessions containing no other reviewer usage. A few first-pass claims didn't survive — noted where relevant.

## What was analysed

- **147 clean three-reviewer runs** between May and August 2026, across 81 sessions
- **~1,000 individual findings** from the merged review outputs
- **88 runs** where all three reviewers gave a clear approve / request-changes verdict
- A subset of runs where the findings were later audited ("go verify whether each finding is actually real") — a rough measure of how often each reviewer is right

## Do the reviewers just find the same things?

No. Overlap is lower than you might expect:

| Reviewer | Findings | Found by them alone | % unique |
|---|---|---|---|
| Sonnet | 534 | 233 | 44% |
| Opus | 582 | 291 | 50% |
| Convention | 293 | 169 | **58%** |

Only 74 findings were caught by all three reviewers. And it's not just noise in the low-value buckets: of the 75 sessions with Critical or Important findings, **each reviewer contributed at least one that nobody else caught** in roughly half — Opus in 45 (60%), Sonnet in 39 (52%), Convention in 35 (47%).

## Who finds the serious bugs?

For Critical issues: Sonnet + Opus together caught 24, all three caught 14, Opus alone 14, Sonnet alone 12, Convention alone just 4.

So Opus has a slight edge on serious bugs, but the cheap model is nearly level. Convention rarely finds critical bugs — but that's fine, that's not its job. Its findings live mostly in the "Important" and "Suggestions" buckets (style, naming, project conventions).

## How often are the findings actually correct?

When findings were audited afterwards:

- **Sonnet: 46% confirmed** (24 of 52)
- **Opus: 49% confirmed** (34 of 69)
- **Convention: 53% confirmed** (19 of 36) — the most accurate, though the gap is small and the sample is small

Put another way: roughly half of all audited findings turned out to be wrong or overblown when investigated. Two big grains of salt: audits were usually triggered when findings looked suspicious (so the real confirm rate is probably higher), and a finding shared by two reviewers counts toward both, which pulls everyone's rates together.

## Verdicts and cost

- Reviewers disagreed on approve-vs-request-changes in **41 of 88 runs (47%)**.
- Sole blocker (the only reviewer demanding changes): Sonnet 6 times, Opus 6, Convention 3. Nobody is notably trigger-happy. (The first pass claimed Sonnet blocked far more often — that was an artifact of the contaminated data.)
- Average cost per review: **Sonnet $0.53, Convention $1.00, Opus $1.07.**
- Total spend across the 147 runs: Opus $158, Convention $148, Sonnet $77 — about **$2.60 per `/review`**.

## How long do the reviewers take?

Duration of each reviewer's run, from first message to last (seconds):

| Reviewer | p50 | p90 | p99 | avg |
|---|---|---|---|---|
| Sonnet | 239s | 471s | 1023s | 273s |
| Opus | 238s | 478s | 1060s | 272s |
| Convention | 164s | 282s | 1011s | 190s |

Two surprises:

- **Sonnet is not faster than Opus in practice.** Their durations are almost identical at every percentile. "Sonnet for speed" doesn't show up in the wall-clock data — the time goes into reading code and running tools, not model latency.
- **Convention is the fastest reviewer** (~30% quicker at the median), likely because its scope is narrower: check the change against known conventions rather than reason about everything.

Since the three run in parallel, the `/review` wall-clock time is set by the slowest reviewer — typically Sonnet or Opus at ~4 minutes median, ~8 minutes at p90.

## Does taking longer produce better findings?

No — if anything the opposite. Joining each reviewer's duration to how its audited findings held up (58 reviewer-session pairs):

| Duration | Valid findings | Invalid findings | Precision |
|---|---|---|---|
| Fast third (90–178s) | 20 | 15 | 57% |
| Middle third (194–280s) | 28 | 28 | 50% |
| Slow third (282–990s) | 29 | 37 | 44% |

Longer runs produce **more findings, but the extras are disproportionately wrong**. Duration has essentially zero correlation with the number of valid findings (r = 0.00) but a moderate positive correlation with invalid ones (r = 0.35). The pattern holds for each reviewer individually, strongest for Sonnet (r = 0.48 with invalid findings).

The likely explanation: reviewers run long when the change is large or murky, and murky changes invite speculative findings. So a long-running review isn't a sign of thoroughness — it's a signal to audit the output more sceptically. (Same caveats as the accuracy section: small, non-random audit sample.)

## Takeaways

1. **Keep all three.** Each reviewer regularly finds serious issues the others miss, and "two reviewers agree" is a genuinely useful confidence signal.
2. **Opus earns its price better than first thought.** It has the highest rate of unique Critical/Important findings per session (60%) and slightly better audit accuracy than Sonnet. It's 2x the cost, but it's not dead weight.
3. **Convention covers a different angle.** Highest share of unique findings (58%) and the best (if narrow) accuracy. Worth trialling it on Sonnet to save money — its job is grounded in written convention docs rather than deep reasoning — but that's a hypothesis, not something this data proves. *(Update: the Convention reviewer was switched to Sonnet off the back of this analysis — all runs before the switch used Opus.)*
4. **The audit step matters.** With roughly half of audited findings not surviving verification, blindly acting on the merged review would waste time on non-issues.
5. **Long reviews deserve extra scepticism.** Review duration doesn't buy accuracy — precision drops from 57% to 44% between the fastest and slowest third of runs.

## Caveats

- Finding attribution and severity come from the orchestrator's merged summary — it decides what counts as "the same finding" and can mislabel who found what.
- 26 of the sessions ran `/review` more than once (review → fix → re-review). A finding that survives a fix cycle can be counted twice.
- Only a small, non-random subset of findings were audited, so accuracy numbers are rough and likely pessimistic.
- ~13% of runs used older model generations (mid-2026 Sonnet/Opus versions), blended into the same buckets.

## Appendix: how this was measured

Everything comes from Anvil's local SQLite database (`~/.local/share/anvil/anvil.db`), which stores every session and message. Subagent sessions are stored as children of the session that launched them, so a `/review` run and its three reviewer sessions are all linkable.

- **Identifying runs**: scanned all assistant messages for `task` tool calls with `subagent_type` of `reviewer` or `convention-reviewer`. A message launching exactly three such calls (Sonnet reviewer + Opus reviewer + Convention reviewer, judged by the `model` override on each call) counts as one `/review` run. Messages launching one, two, or odd combinations were excluded, as were whole sessions that mixed `/review` runs with ad-hoc reviewer usage — this is the "clean dataset" filter that fixed the first-pass contamination.
- **Findings and attribution**: the orchestrator's merged review tags each finding like `[Sonnet + Opus]`. Every line matching the pattern `[Sonnet|Opus|Convention (+ ...)]` was extracted, along with the markdown heading above it (Critical Issues, Suggestions, etc.). "Blocking Summary" lines were excluded to avoid counting the same finding twice within one review. Exact duplicate lines within a session were also dropped.
- **Uniqueness**: a finding is "unique" to a reviewer if its tag names only that reviewer. Counted per section, on clean sessions only.
- **Audit accuracy**: some sessions were followed up with "audit these findings", producing sections with headings like "Confirmed valid" or "Rejected (reviewer error)". Findings under those headings were bucketed by heading keywords (confirm/valid vs reject/refuted/downgraded); ambiguous "Audit Results" sections were classified line-by-line on markers like `**Valid**` or "misread". This is a keyword heuristic, not a manual re-check — treat the percentages as rough.
- **Verdicts**: each reviewer session's output was scanned for its final "APPROVE" or "REQUEST CHANGES". ~12% of sessions had no clean verdict string and were excluded from the agreement stats.
- **Cost and tokens**: read directly from the per-session `cost` and token columns Anvil records.
- **Duration**: last message timestamp minus first message timestamp within each reviewer's session — i.e. wall-clock time including tool use, not just model time.
- **Duration vs quality**: reviewer-session pairs that had both a duration and audited findings (58 pairs) were joined; correlation is plain Pearson r on (duration, valid count) and (duration, invalid count).

The analysis is reproducible with sqlite3 plus standard text tools against the same database.
