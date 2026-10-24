# Is the three-reviewer `/review` setup worth it?

The `/review` command runs three AI code reviewers in parallel on the same change:

- **Sonnet**: a faster, cheaper model, aimed at broad coverage
- **Opus**: a slower, more expensive model, aimed at deep analysis
- **Convention**: a reviewer focused purely on whether the code follows our conventions (originally on Opus)

Each finding in the merged review is tagged with which reviewer(s) caught it (e.g. `[Sonnet + Opus]`). I dug through the Anvil database of past sessions to see whether all three reviewers actually earn their keep.

The first part of this doc covers the original setup (May to August 2026). The [follow-up section](#follow-up-did-moving-convention-to-sonnet-hurt-october-2026) covers what happened after Convention was moved to Sonnet.

> **Note on rigour:** this analysis has been corrected twice.
>
> 1. The first pass accidentally mixed in other uses of the reviewer agents (ad-hoc reviews, spec reviews, plan reviews). An adversarial review caught it.
> 2. The October re-analysis found that cost and duration had been measured on a bigger set of runs than the findings were. It also found a bug in how audit results were classified.
>
> All numbers below are corrected. The main casualty: the claim that Convention was the "most accurate" reviewer doesn't hold up.

## What was analysed

- **98 clean three-reviewer runs** between May and August 2026, across 81 sessions
- **~1,000 individual findings** from the merged review outputs
- **88 runs** where all three reviewers gave a clear approve / request-changes verdict
- **25 sessions** where the findings were later audited ("go verify whether each finding is actually real"), which gives a rough measure of how often each reviewer is wrong

## Do the reviewers just find the same things?

No. Overlap is lower than you might expect:

| Reviewer | Findings | Found by them alone | % unique |
|---|---|---|---|
| Sonnet | 534 | 232 | 43% |
| Opus | 580 | 287 | 49% |
| Convention | 286 | 164 | **57%** |

Only 73 findings were caught by all three reviewers. The unique findings aren't just low-priority noise either. There were 75 sessions with Critical or Important findings, and in about half of them **each reviewer found at least one that nobody else did**: Opus in 45 (60%), Sonnet in 39 (52%), Convention in 35 (47%).

## Who finds the serious bugs?

For Critical issues: Sonnet + Opus together caught 24, all three caught 14, Opus alone 14, Sonnet alone 12, Convention alone just 4.

So Opus has a slight edge on serious bugs, but the cheap model is nearly level. Convention rarely finds critical bugs, which is fine because that's not its job. Its findings are mostly Important issues and Suggestions (style, naming, project conventions).

## How often are the findings wrong?

The most reliable measure is how many of each reviewer's findings the audit **explicitly rejected**, counting only sessions that were audited:

- **Sonnet: 16% rejected** (29 of 186)
- **Opus: 18% rejected** (38 of 206)
- **Convention: 18% rejected** (18 of 99)

These are basically the same. No reviewer is clearly more accurate than the others.

Why not just report "% confirmed"? Many audits only list the findings they reject and never mention the ones that held up, so a confirm rate would be badly skewed. (Among findings the audit *did* explicitly rule on, roughly half were confirmed for every reviewer. That's the figure an earlier version of this doc reported.) Audits also tend to happen when a review looks suspicious, so all these numbers lean pessimistic.

## Verdicts and cost

- Reviewers disagreed on approve-vs-request-changes in **41 of 88 runs (47%)**.
- Sole blocker (the only reviewer demanding changes): Sonnet 6 times, Opus 5, Convention 3. Nobody is notably trigger-happy.
- Average cost per review: **Sonnet $0.48, Convention $0.95, Opus $1.01.**
- That's about **$2.44 per `/review`** in total.

## How long do the reviewers take?

Duration of each reviewer's run, from first message to last (seconds):

| Reviewer | p50 | p90 | p99 | avg |
|---|---|---|---|---|
| Sonnet | 221s | 424s | 1040s | 248s |
| Opus | 226s | 474s | 792s | 256s |
| Convention | 155s | 266s | 491s | 168s |

Two surprises:

- **Sonnet is not faster than Opus in practice.** Their median times are almost identical. "Sonnet for speed" doesn't show up in the wall-clock data. Most of the time goes into reading code and running tools, not waiting on the model.
- **Convention is the fastest reviewer** (~30% quicker at the median), probably because its job is narrower: it checks the change against known conventions rather than reasoning about everything.

Since the three run in parallel, the `/review` wall-clock time is set by the slowest reviewer: about **4.3 minutes at the median and 8 minutes at p90**.

## Does taking longer produce better findings?

No. If anything it's the opposite. I matched each reviewer's run time against how its audited findings held up (60 pairings of a reviewer with a session):

| Duration | Valid findings | Invalid findings | Precision |
|---|---|---|---|
| Fast third (90–194s) | 19 | 18 | 51% |
| Middle third (195–282s) | 31 | 30 | 51% |
| Slow third (284–990s) | 27 | 37 | 42% |

Longer runs don't produce more valid findings (correlation r = 0.01), but they do produce more invalid ones (r = 0.33). The likely explanation: reviewers run long when the change is large or murky, and murky changes invite speculative findings. A long-running review isn't a sign of thoroughness. It's a signal to audit the output more sceptically.

## Takeaways (original setup)

1. **Keep all three.** Each reviewer regularly finds serious issues the others miss, and "two reviewers agree" is a genuinely useful confidence signal.
2. **Opus earns its price.** It has the highest rate of unique Critical/Important findings per session (60%). It costs 2x as much as Sonnet, but it's not dead weight.
3. **Convention covers a different angle.** It has the highest share of unique findings (57%) and is about as accurate as the others. Its job is grounded in written convention docs rather than deep reasoning, which made it the obvious candidate to try on a cheaper model.
4. **The audit step matters.** Around 1 in 6 findings gets explicitly thrown out on inspection, and that's a floor, not a ceiling.
5. **Long reviews deserve extra scepticism.** Taking longer doesn't buy accuracy.

## Follow-up: did moving Convention to Sonnet hurt? (October 2026)

Off the back of the analysis above, Convention was switched from Opus to Sonnet on Aug 31. Since then, the `/review` setup has changed several more times:

| Period | Dates | Setup | Clean runs |
|---|---|---|---|
| Baseline | to Aug 30 | Sonnet 4.6, Opus 4.6, Convention on **Opus 4.6** | 98 |
| A | Aug 31 to Sep 5 | Sonnet 4.6, Opus 4.6, Convention on **Sonnet 4.6** | 18 |
| B | Sep 5 to 18 | Sonnet 5, Opus 5, Convention on Sonnet 5 | 11 |
| C | Sep 18 to 30 | Four reviewers: Sonnet 5, Astra, Convention on Sonnet 5, Convention on Astra | 10 |
| D | Sep 30 to Oct 1 | Sonnet 5, Opus 5.5, Convention on Sonnet 5, Convention on Sol | 0 |
| E (current) | Oct 1 on | Sonnet 5, Opus 5.5, Convention on Sonnet 5, **Convention on Opus 5.5** | 0 |

**Period A is the only fair test of the switch**, because Convention's model was the only thing that changed. Periods B and C changed the other reviewers too, so they can't isolate it.

### Period A results

There's a catch: period A happened to review much bigger changes. The reviewers' input roughly doubled in size, and 14 of its 18 runs were on a single project. So everyone's raw numbers went up, including the reviewers that didn't change. The fair comparison is how Convention did *relative to the other reviewers in the same runs*:

| Convention compared to the Opus reviewer in the same run | Baseline (Convention on Opus) | Period A (Convention on Sonnet) |
|---|---|---|
| Cost | 94% of Opus's cost | **35%** of Opus's cost |
| Number of findings | 0.5x as many | 0.7x as many |
| Unique Critical/Important findings | 0.65x as many | **1.9x** as many |
| Findings explicitly rejected in audits | 18% (Opus: 18%) | 24% (Opus: 12%) |

In plain terms, Convention on Sonnet:

- **Got much cheaper for the work it does.** It went from costing about the same as the Opus reviewer to about a third of it, and from twice Sonnet's cost to slightly less. Its absolute cost per run didn't drop ($0.95 → $0.96) only because the changes being reviewed were twice as big.
- **Got louder.** It found more, and it flagged nearly twice as many unique Important-or-worse issues as Opus. In 13 of 14 sessions it found something nobody else did, up from about half in the baseline. It was also the sole blocker in 3 of the 7 runs where reviewers disagreed.
- **May be slightly less accurate.** 24% of its audited findings were rejected, compared to 18% before. But that's 12 rejections out of 50 findings, and the difference isn't statistically meaningful.
- **Still isn't the bottleneck.** Its median time was 380s, against 442s for Opus and 542s for Sonnet in the same period.

### Periods B and C

Too few runs to say much:

- **B (all 5-series models):** Opus 5 dominated, with 17 findings per run, two-thirds of them unique. Only one session was audited.
- **C (two Convention reviewers side by side):** the two barely overlapped. They agreed on 5 findings, while Convention on Sonnet 5 alone found 38 and Convention on Astra alone found 14. Astra was about 6x faster (77s vs 458s at the median). None of these runs were audited, so there's no way to tell which one was right more often.

Cost per `/review` climbed from $2.44 (baseline) to $4.80 (A), $7.21 (B) and $6.13 (C). That's a mix of bigger changes being reviewed and 5-series pricing, and the data can't separate the two.

### Verdict on the switch

**There's no evidence that moving Convention to Sonnet meaningfully hurt it, and it's clearly cheaper for the work done.** It is noisier: more findings, more blocking, and possibly a slightly higher reject rate. But the evidence is thin: 18 runs, mostly on one project.

The current setup (period E) runs Convention on Sonnet 5 and on Opus 5.5 side by side on the same change. That's exactly the head-to-head test this question needs, but it has no runs yet. To get a clear answer:

1. **Audit every `/review` for the next ~15 runs.** At the moment only about a quarter of sessions get audited.
2. **Have the audit list confirmed findings as well as rejected ones**, each tagged with the reviewer who found it. That fixes the reporting gap described in "How often are the findings wrong?" and makes a proper confirm rate possible.
3. Then re-run this analysis on period E, comparing `[Convention (default)]` against `[Convention (Opus)]`.

## Caveats

- Who-found-what and severity both come from the orchestrator's merged summary. It decides what counts as "the same finding" and can mislabel attribution.
- Many sessions ran `/review` more than once (review → fix → re-review). A finding that survives a fix cycle can be counted twice.
- Audits are a small, non-random subset and inconsistently formatted, so accuracy numbers are rough.
- About 13% of baseline runs used older model generations, blended into the same buckets.
- The follow-up periods are small (10 to 18 clean runs each). Treat their numbers as directional at best.

## Appendix: how this was measured

Everything comes from Anvil's local SQLite database (`~/.local/share/anvil/anvil.db`), which stores every session and message. Each reviewer runs in its own session, linked to the session that launched it, so a `/review` run and its reviewer sessions can all be connected.

- **Identifying runs**: I scanned all assistant messages for `task` tool calls with `subagent_type` of `reviewer` or `convention-reviewer`. A single message that launches the full set of `/review` reviewers counts as one run. Each reviewer is identified by the model it **actually ran on** (recorded in its session), not the model the command asked for. Whole sessions were excluded if they also contained ad-hoc reviewer usage, partial runs, or reviewers launched one at a time. This is the "clean" filter, and every metric uses the same set of runs.
- **Findings and attribution**: the orchestrator's merged review tags each finding like `[Sonnet + Opus]` or `[Convention (default)]`. I pulled out every line with a tag, along with the markdown heading above it (Critical Issues, Suggestions, etc.). "Blocking Summary" lines were skipped because they repeat findings already listed, and exact duplicate lines were dropped.
- **Uniqueness**: a finding is "unique" to a reviewer if its tag names only that reviewer.
- **Audit outcomes**: audit write-ups were classified from their headings, e.g. "Confirmed valid" vs "Rejected" or "Findings I verified as incorrect". Ambiguous "Audit Results" sections were classified line by line, using markers like `**Valid**` or "misread", and struck-through findings counted as rejected. The headline metric is *explicitly rejected ÷ that reviewer's findings, within audited sessions*. That works even when an audit only lists rejections. This is a keyword heuristic, not a manual re-check.
- **Verdicts**: I scanned each reviewer's output for its final "APPROVE" or "REQUEST CHANGES". Runs without a clean verdict from every reviewer were left out of the agreement stats.
- **Cost and tokens**: read directly from the per-session `cost` and token columns Anvil records. The input token count (`prompt_tokens`) was used as a rough measure of how big each review job was.
- **Duration**: last message timestamp minus first, within each reviewer's session. That's wall-clock time, including tool use.
- **Duration vs quality**: each reviewer's average run time in a session, matched against its audited valid/invalid counts in that session. Correlation is plain Pearson r.
- **Same-run comparison (follow-up)**: to cancel out differences in how big the reviewed changes were, Convention's cost, finding counts and reject rate are compared with the general reviewers in the same runs, rather than across periods.

To reproduce it, run `scripts/review-analysis.py` (Python 3, standard library only). It opens the database read-only and prints every number in this doc, split by period. `--dump-audit <period>` prints each audit line and how it was classified, which is the quickest way to sanity-check the audit numbers. If `/review` changes setup again, add a new entry to the `PERIODS` table at the top of the script, mapping each reviewer's actual model to its tag.
