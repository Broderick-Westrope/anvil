# Triage Queries

Run every query through the CTE pattern from `SKILL.md`, in one bash call
from the repo root:

```bash
cd <anvil repo root> && DB=~/.local/share/anvil/anvil.db && R=.agents/skills/anvil-cache-triage/references/report.sql && C=.agents/skills/anvil-cache-triage/references/classify.sql && sqlite3 -cmd "PRAGMA query_only=ON" -header -column "$DB" "WITH step_usage_report AS ($(cat "$R")), classified AS ($(cat "$C")) <QUERY>"
```

Paste a query below in place of `<QUERY>`. The queries contain no double
quotes, `$` or backticks, so they are safe inside the double-quoted
argument. Queries that only read `step_usage_report` work without the
`classified` CTE too.

Every query filters to the last 7 days with
`request_started_at >= (strftime('%s','now') - 7*86400) * 1000`. Change the
`7`, or delete the condition to use all rows. The classification itself
always runs over all rows, so the first row in the window still has a
predecessor.

Hit rate is `SUM(cache_read_tokens) / SUM(prompt_tokens)`, never an average
of per-row `hit_rate`. Exclude `estimated = 1` rows from hit rates.

## 1. Hit rate

Overall:

```sql
SELECT COUNT(*) AS calls,
  SUM(prompt_tokens) AS prompt, SUM(cache_read_tokens) AS cache_read,
  SUM(cache_write_tokens) AS cache_write,
  ROUND(1.0 * SUM(cache_read_tokens) / NULLIF(SUM(prompt_tokens), 0), 3) AS hit_rate,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM step_usage_report
WHERE estimated = 0 AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
```

By provider, model, kind and agent:

```sql
SELECT provider, model, kind, agent, COUNT(*) AS calls,
  SUM(input_tokens) AS input, SUM(cache_read_tokens) AS cache_read,
  SUM(cache_write_tokens) AS cache_write,
  ROUND(1.0 * SUM(cache_read_tokens) / NULLIF(SUM(prompt_tokens), 0), 3) AS hit_rate,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM step_usage_report
WHERE estimated = 0 AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY provider, model, kind, agent
ORDER BY list_cost DESC
```

## 2. Spend breakdown

By token type. `flat_rate_calls` are subscription calls whose `list_cost`
is notional:

```sql
SELECT COUNT(*) AS calls, ROUND(SUM(list_cost), 4) AS total,
  ROUND(SUM(input_tokens * price_input) / 1e6, 4) AS input_cost,
  ROUND(SUM(cache_read_tokens * price_cache_read) / 1e6, 4) AS read_cost,
  ROUND(SUM(cache_write_tokens * price_cache_write) / 1e6, 4) AS write_cost,
  ROUND(SUM(output_tokens * price_output) / 1e6, 4) AS output_cost,
  SUM(flat_rate) AS flat_rate_calls,
  SUM(price_input = 0 AND price_output = 0) AS unpriced_calls
FROM step_usage_report
WHERE request_started_at >= (strftime('%s','now') - 7*86400) * 1000
```

On suspected misses. `excess_cost` is what re-sending the prefix cost over
reading it from cache:

```sql
SELECT COUNT(*) AS misses, ROUND(SUM(list_cost), 4) AS miss_list_cost,
  ROUND(SUM(excess_cost), 4) AS excess_cost,
  SUM(tokens_not_reused) AS tokens_not_reused
FROM classified
WHERE suspected_miss = 1
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
```

## 3. Suspected causes

Ranked by tokens not reused. Change the `ORDER BY` to `excess_cost DESC`
to rank by cost:

```sql
SELECT suspected_cause, COUNT(*) AS misses,
  SUM(tokens_not_reused) AS tokens_not_reused,
  ROUND(SUM(excess_cost), 4) AS excess_cost,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM classified
WHERE suspected_miss = 1
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY suspected_cause
ORDER BY tokens_not_reused DESC
```

How much of the data was judged at all. `unjudged` rows are first calls,
estimated rows, small prefixes (under 1024 tokens) or `cache_policy`
`none`:

```sql
SELECT cache_policy, COUNT(*) AS turn_rows,
  SUM(suspected_miss IS NULL) AS unjudged,
  SUM(suspected_miss = 0) AS hits, SUM(suspected_miss = 1) AS misses
FROM classified
WHERE kind = 'turn'
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY cache_policy
```

## 4. Worst sessions

```sql
SELECT session_id, agent, MAX(working_dir) AS working_dir,
  MIN(started_utc) AS first_utc, COUNT(*) AS steps,
  SUM(suspected_miss = 1) AS misses,
  SUM(tokens_not_reused) AS tokens_not_reused,
  ROUND(SUM(excess_cost), 4) AS excess_cost,
  ROUND(1.0 * SUM(cache_read_tokens) / NULLIF(SUM(prompt_tokens), 0), 3) AS hit_rate
FROM classified
WHERE kind = 'turn' AND estimated = 0
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY session_id, agent
ORDER BY tokens_not_reused DESC
LIMIT 10
```

## 5. One session's timeline

Replace `SESSION_ID`. Rows with the same `run_id` and `step_index` would
be one logical step recorded twice, which should not happen with fantasy
v0.45.2; report any as a recording bug (see `schema.md`):

```sql
SELECT started_utc, agent, kind, run_id, step_index, model,
  input_tokens AS input, cache_read_tokens AS read, cache_write_tokens AS write,
  output_tokens AS output, estimated AS est, history_prefix_match AS prefix,
  reuse_gap_ms, retry_count, changes, suspected_cause, tokens_not_reused
FROM classified
WHERE session_id = 'SESSION_ID'
ORDER BY request_started_at, response_finished_at
```

Title calls are not in `classified`. List them separately:

```sql
SELECT started_utc, attempt, model, input_tokens, output_tokens, list_cost
FROM step_usage_report
WHERE session_id = 'SESSION_ID' AND kind = 'title'
ORDER BY request_started_at
```

## 6. Subagent fan-out

Per child agent, under each parent session:

```sql
SELECT parent_session_id, agent, depth,
  COUNT(DISTINCT session_id) AS sessions, COUNT(*) AS calls,
  SUM(cache_write_tokens) AS cache_write, SUM(cache_read_tokens) AS cache_read,
  ROUND(SUM(cache_write_tokens * price_cache_write) / 1e6, 4) AS write_cost,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM step_usage_report
WHERE parent_session_id != '' AND kind = 'turn'
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY parent_session_id, agent, depth
ORDER BY write_cost DESC
```

Parent's own cache writes against its direct children's (grandchildren roll
up to their own parent):

```sql
SELECT COALESCE(NULLIF(parent_session_id, ''), session_id) AS parent,
  COUNT(DISTINCT CASE WHEN parent_session_id != '' THEN session_id END) AS children,
  SUM(CASE WHEN parent_session_id = '' THEN cache_write_tokens ELSE 0 END) AS own_write,
  SUM(CASE WHEN parent_session_id != '' THEN cache_write_tokens ELSE 0 END) AS child_write
FROM step_usage_report
WHERE session_id != '' AND kind = 'turn'
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY parent
HAVING children > 0
ORDER BY child_write DESC
```

## 7. Cold-start cost

The first turn step of each session and agent:

```sql
SELECT agent, model, COUNT(*) AS first_calls,
  ROUND(AVG(cache_write_tokens)) AS avg_write, ROUND(AVG(input_tokens)) AS avg_input,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM classified
WHERE changes = 'first_call' AND kind = 'turn'
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY agent, model
ORDER BY list_cost DESC
```

## 8. Per-repo summary

```sql
SELECT r.working_dir, COUNT(DISTINCT NULLIF(r.session_id, '')) AS sessions,
  COUNT(*) AS calls,
  ROUND(1.0 * SUM(CASE WHEN r.estimated = 0 THEN r.cache_read_tokens END)
        / NULLIF(SUM(CASE WHEN r.estimated = 0 THEN r.prompt_tokens END), 0), 3) AS hit_rate,
  SUM(c.suspected_miss = 1) AS misses,
  ROUND(SUM(c.excess_cost), 4) AS excess_cost,
  ROUND(SUM(r.list_cost), 4) AS list_cost
FROM step_usage_report r LEFT JOIN classified c ON c.id = r.id
WHERE r.request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY r.working_dir
ORDER BY list_cost DESC
```

## 9. Summary calls

```sql
SELECT provider, model, COUNT(*) AS summaries,
  SUM(cache_read_tokens = 0) AS zero_read,
  ROUND(AVG(input_tokens)) AS avg_input,
  ROUND(SUM(list_cost), 4) AS list_cost
FROM step_usage_report
WHERE kind = 'summary' AND estimated = 0
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY provider, model
```

## 10. Providers that never report reads

A real cache rarely reads zero tokens over many calls. Check `sample_raw`
for a cache field that `cacheusage.Normalise` does not map:

```sql
SELECT provider, provider_type, model, cache_policy, COUNT(*) AS calls,
  SUM(prompt_tokens) AS prompt, SUM(cache_write_tokens) AS cache_write,
  (SELECT x.raw_usage FROM step_usage x
   WHERE x.provider = r.provider AND x.model = r.model AND x.estimated = 0
   ORDER BY x.request_started_at DESC LIMIT 1) AS sample_raw
FROM step_usage_report r
WHERE estimated = 0
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY provider, provider_type, model, cache_policy
HAVING SUM(cache_read_tokens) = 0
ORDER BY calls DESC
```

## 11. Reviewer and title calls

`small` rows are the bouncer's small-model reviewer, with an empty
`session_id`:

```sql
SELECT kind, model, date(request_started_at / 1000, 'unixepoch') AS day,
  COUNT(*) AS calls, SUM(prompt_tokens) AS prompt,
  SUM(cache_read_tokens) AS cache_read, ROUND(SUM(list_cost), 4) AS list_cost
FROM step_usage_report
WHERE kind IN ('small', 'title')
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY kind, model, day
ORDER BY day, kind
```

## 12. Retries and duplicate steps

Retry clusters by provider and hour:

```sql
SELECT provider, model, strftime('%Y-%m-%d %H:00', request_started_at / 1000, 'unixepoch') AS hour,
  COUNT(*) AS rows_with_retries, SUM(retry_count) AS retries
FROM step_usage_report
WHERE retry_count > 0
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY provider, model, hour
ORDER BY retries DESC
```

Logical steps recorded twice. With fantasy v0.45.2 tool errors are no
longer retried, so this should return nothing; any row is a recording bug
worth reporting:

```sql
SELECT session_id, run_id, step_index, COUNT(*) AS rows,
  GROUP_CONCAT(retry_count) AS retry_counts, SUM(prompt_tokens) AS prompt
FROM step_usage_report
WHERE kind = 'turn' AND run_id != ''
  AND request_started_at >= (strftime('%s','now') - 7*86400) * 1000
GROUP BY run_id, step_index
HAVING COUNT(*) > 1
```
