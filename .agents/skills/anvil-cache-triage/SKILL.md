---
name: anvil-cache-triage
description: Use when triaging Anvil's prompt-cache usage - cache hit rate, cache misses, cache writes, or unexpected token spend across sessions, subagents, providers or repos. Queries the step_usage table read-only with sqlite3.
---

# Anvil Cache Triage

Anvil records one `step_usage` row per completed LLM response: normalised
token counts, raw provider usage, timings, prices and request fingerprints
(hashes only). Classification of misses lives in SQL in this skill, not in
Go. Answer from the data; do not read Go code unless a finding needs a fix.

References (next to this file):

- `references/schema.md`: every column, units, normalisation, NULL rules,
  recording gaps. Read before interpreting numbers.
- `references/classify.sql`: the `classified` view (miss judgement and
  suspected cause per row).
- `references/queries.md`: copy-paste queries for each procedure step.
- `references/interpretation.md`: what each cause means, file references
  and candidate fixes.

## Finding the DB

- Default: `~/.local/share/anvil/anvil.db`. `ANVIL_GLOBAL_DATA=<dir>`
  overrides it to `<dir>/anvil.db`; otherwise `XDG_DATA_HOME` moves it to
  `$XDG_DATA_HOME/anvil/anvil.db`. `anvil dirs` prints the data directory.
  If the user names a directory, use `<dir>/anvil.db`.
- Open with `sqlite3 -cmd "PRAGMA query_only=ON"`, not `-readonly`. The
  DB is in WAL mode, and when no Anvil process has it open there is no
  `-shm` file, so `-readonly` fails with "unable to open database file
  (14)". `query_only` refuses every write while still letting SQLite
  create its WAL index. Reads are safe while Anvil runs.
- Check data exists first:
  `sqlite3 -cmd "PRAGMA query_only=ON" "$DB" "SELECT COUNT(*), datetime(MIN(request_started_at)/1000,'unixepoch'), datetime(MAX(request_started_at)/1000,'unixepoch') FROM step_usage"`

## Running queries

`classified` is a CTE wrapped around `classify.sql` (a temp view would be
a write, which `query_only` blocks). Each bash call is a fresh shell, so
set the variables and `cd` to the repo root in the same call, or give
`SQL` as an absolute path to this skill's `references/classify.sql`:

```bash
cd <anvil repo root> && DB=~/.local/share/anvil/anvil.db && SQL=.agents/skills/anvil-cache-triage/references/classify.sql && sqlite3 -cmd "PRAGMA query_only=ON" -header -column "$DB" "WITH classified AS ($(cat "$SQL")) SELECT suspected_cause, COUNT(*) FROM classified WHERE suspected_miss = 1 GROUP BY 1"
```

Replace the trailing `SELECT` with any query from `queries.md` (they all
start with `SELECT`). Keep the query free of double quotes, `$` and
backticks. Use `-line` instead of `-column` for wide `raw_usage` values.
An empty result means the pattern is absent (for example, no subagents),
not that the query failed.

## Coverage limits

- Data starts when recording shipped; rows are kept for 90 days.
- Responses that never reported usage (cancelled or failed before finish)
  are absent.
- Rows are written asynchronously; in-flight rows (often title calls) may
  be lost at shutdown.
- The System One bouncer is not recorded. The bouncer's small-model
  reviewer is (`kind = 'small'`).
- `classified` covers only `turn` and `summary` rows with a session.

## Procedure

1. **Window.** Default: all rows (since the first row). Queries filter to 7
   days; adjust or drop the filter. State the window and row count.
2. **Hit rate**, overall and by provider/model/kind/agent (queries 1).
3. **Spend**: total `list_cost`, cost on suspected misses and
   `excess_cost`, cache-write spend (queries 2).
4. **Suspected causes** ranked by `tokens_not_reused`, then by
   `excess_cost` (queries 3). Check how many turn rows were judged at all.
5. **Per repo**, split by `working_dir` (queries 8).
6. **Timelines** of the 3 worst sessions (queries 4, then 5). Corroborate
   each dominant cause there.
7. **Side checks**: subagent fan-out (6), cold starts (7), summaries (9),
   providers with no reads (10), reviewer and title volume (11), retries
   (12).
8. **Report.** Each finding with the query used and the numbers, its
   verdict from `interpretation.md` (expected or suspicious), and one
   recommended fix per dominant suspicious cause.

## Stopping rule

Judge the `turn` hit rate twice: with all rows, and excluding
`first_call` rows. Short sessions are dominated by their cold start, so
the second number is the one to compare with the bar.

If that hit rate is about 85% or more and the remaining misses are
explained by expected causes (`first_call`, `model_changed`,
`after_summary`, a branch switch, or an occasional `tools_changed` that
lines up with one `enable_mcp`), the cache is healthy. Say so and
recommend nothing. Escalate `tools_changed` only when it recurs within
sessions or dominates `tokens_not_reused`.

## Ground rules

- Classification is heuristic. Before claiming a cause, corroborate it
  with `changes`, `reuse_gap_ms`, `history_prefix_match`, neighbouring
  rows and `raw_usage`.
- Exclude `estimated = 1` rows from hit-rate judgements.
- Treat `unknown_after_restart` as inconclusive, never as a finding.
  `history_prefix_match` is NULL on the first step after any process
  restart, including `/reload-instance`.
- `changes` containing `history` on a row that was still a hit means the
  fingerprint disagreed with the provider. Report it as a possible
  fingerprint false positive, not as a cache problem.
- Compute hit rates as `SUM(cache_read_tokens) / SUM(prompt_tokens)`, not
  the average of per-row `hit_rate`.
- `list_cost` uses catwalk list prices stored per row; it is notional for
  `flat_rate = 1` rows and 0 when prices are unknown.
- Google cache reads may be over-counted (see `schema.md`).
- `agent = 'reviewer'` is both the small-call reviewer and a specialist;
  filter by `kind` too.
