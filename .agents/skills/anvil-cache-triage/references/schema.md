# step_usage Schema

One row per completed LLM response (one fantasy stream that reported a
finish). Source: `internal/db/migrations/20261009000000_add_step_usage.sql`,
written by `internal/agent/step_usage.go` through
`internal/agent/cacheusage`. Rows older than 90 days (by
`response_finished_at`) are pruned at startup. No foreign keys: rows
outlive deleted sessions.

Units: timestamps are Unix **milliseconds**; prices are USD **per 1M
tokens**; token counts are per call.

## Table `step_usage`

### Attribution

| Column | Meaning |
|---|---|
| `id` | Row ID. |
| `session_id` | Anvil session. Empty for `small` rows. |
| `parent_session_id` | Parent session for specialist subagents and `agentic_fetch`. Empty for top-level sessions and for `title` rows (even in child sessions). |
| `working_dir` | Working directory of the Anvil process. Use it to split repos. |
| `message_id` | Assistant message (turn) or compaction message (summary). Empty for title and small. |
| `agent` | `orchestrator`, a specialist name (`fixer`, `explorer`, `reviewer`, ...), `agentic_fetch`, or `reviewer` for `small` rows. A specialist called `reviewer` also exists, so always pair `agent` with `kind`. Title rows carry the session's agent. |
| `kind` | `turn` (agent loop step), `summary` (compaction), `title` (session title), `small` (`CompleteSmall`, the bouncer's small-model reviewer). |
| `depth` | Remaining delegation depth of the recording agent, not nesting level: orchestrator 3, each delegation level one less (specialist called by the orchestrator 2). `agentic_fetch` is 0. Use `parent_session_id`, not `depth`, to find subagents. |
| `run_id` | Groups the steps of one agent Run, one summary call or one title call. |
| `step_index` | fantasy `StepNumber` within `run_id`, 0-based. |
| `attempt` | Title only: 0 = small model, 1 = large-model fallback. 0 elsewhere. |

### Model

| Column | Meaning |
|---|---|
| `provider` | Config provider ID (for example `anthropic`, `openrouter`, a custom ID). |
| `provider_type` | fantasy provider name: `anthropic`, `bedrock`, `vercel`, `openai`, `azure`, `openrouter`, `google` (also Vertex), `openai-compat`, ... Drives normalisation. |
| `model` | Config model ID. |

### Timing

| Column | Meaning |
|---|---|
| `request_started_at` | End of `PrepareStep` for this request, just before fantasy sends it: after the previous step's tools ran and, for turns, after the assistant message was created and the request fingerprinted. |
| `response_finished_at` | When the provider reported usage (`OnStreamFinish`), before this step's tools run. |
| `retry_count` | `OnRetry` calls during this step (failed attempts before the one that finished). |
| `finish_reason` | fantasy finish reason (`stop`, `tool-calls`, `length`, ...). |

### Tokens (normalised)

| Column | Meaning |
|---|---|
| `input_tokens` | Uncached input. **Never includes** cache reads or writes. |
| `cache_read_tokens` | Tokens served from cache. |
| `cache_write_tokens` | Tokens written to cache (Anthropic-style only; 0 for automatic caching). |
| `output_tokens`, `reasoning_tokens` | As reported. From fantasy v0.45.2, `output_tokens` includes reasoning tokens for Google and OpenAI-family providers even when the provider reports them separately (fantasy folds them in), so never add `reasoning_tokens` to `output_tokens`. |
| `estimated` | 1 when the provider reported all-zero usage. `input_tokens` is then a rough estimate from message text and all cache columns are 0. Exclude from hit rates. |
| `raw_usage` | JSON `{"usage": {...}, "extra": {...} or null}` as reported, before normalisation. `usage` keys: `input_tokens`, `output_tokens`, `total_tokens`, `reasoning_tokens`, `cache_creation_tokens`, `cache_read_tokens`. `extra` holds OpenAI-style usage fields fantasy did not map (`openai.ProviderMetadata.ExtraFields`). |

Normalisation per `provider_type` (`cacheusage.Normalise`):

| provider_type | Rule |
|---|---|
| `anthropic`, `bedrock` | Copied through; Anthropic already excludes cache from input. |
| `openai`, `azure`, `openrouter`, `vercel` | Copied through; fantasy already subtracts cached tokens from input. |
| `google` | `cache_read = min(cache_read, input)`, then `input = input - cache_read` (Gemini's prompt count includes cached content). |
| `openai-compat` | If `cache_read` is 0 and `extra.prompt_cache_hit_tokens` (DeepSeek) is set, it becomes `cache_read` and is subtracted from input. Other vendor fields are not mapped. |
| other | Assumed OpenAI semantics, copied through. |

Caveats:

- **Google streaming may over-count cache reads.** fantasy sums
  `CacheReadTokens` across usage chunks but keeps the first chunk's input
  (suspected bug, `fantasy@v0.45.2 providers/google/google.go:857,1134`).
  `cache_read_tokens` is now capped at the reported prompt size, so a
  Google row never reads more than its prompt, but a capped row shows a
  100% hit rate with `input_tokens` 0. Compare `raw_usage` (which keeps
  the uncapped count) before trusting a Google hit rate.
- An `openai-compat` provider whose cache field has another name reports 0
  reads. Look in `raw_usage.extra`.
- **Vercel never reports cache writes.** fantasy's Vercel usage mapping
  fills only `CacheReadTokens` from `prompt_tokens_details.cached_tokens`
  (`languageModelUsage` and `languageModelStreamUsage` in
  `fantasy providers/vercel/language_model_hooks.go`), so
  `cache_write_tokens` is always 0 and tokens written to cache are counted
  as input. Vercel rows use the `anthropic_ephemeral` policy, so
  `classify.sql` judges them against `prev_prompt_tokens` instead of
  `prev_cached_prefix`, and their write cost is not visible.

### Prices (catwalk, at call time)

| Column | Meaning |
|---|---|
| `price_input`, `price_output` | Per 1M tokens. |
| `price_cache_read` | catwalk `CostPer1MOutCached` (read price, despite the name). |
| `price_cache_write` | catwalk `CostPer1MInCached` (write price). |
| `flat_rate` | 1 for subscription/flat-rate models: list cost is notional. |

All prices are 0 when catwalk has none. The `avian` provider's catwalk
read/write prices may be swapped; sanity-check them (write should exceed
read).

### Fingerprint (hashes only, never content)

| Column | Meaning |
|---|---|
| `cache_policy` | `anthropic_ephemeral` (explicit breakpoints: anthropic, bedrock, vercel), `disabled` (those providers with `ANVIL_DISABLE_ANTHROPIC_CACHE` set), `automatic` (openai, azure, openai-compat, openrouter, google cache prefixes themselves), `none` (other). Recorded per row, but summary, title and small requests send no cache markers whatever the policy. |
| `message_count`, `system_count`, `tool_count` | Non-system messages, system messages and tools sent. |
| `tools_hash` | Hash of tools in the order Anvil passed them (name, description, parameters, required). Empty-list hash for summary, title and small. |
| `system_hash` | Hash of all system messages, block boundaries included. |
| `history_hash` | Rolling hash over the semantic content of all non-system messages, flattened into entries: per message, all reasoning text as one entry, all text (trimmed) as one entry, then each tool call (id, name, input), tool result (id, output text, error or media) and file (media type, data) in order, each tagged with its role. Ignored: provider options and metadata at message and part level (cache markers, reasoning signatures, OpenAI Responses reasoning metadata), message grouping (one combined tool message hashes the same as one per result), empty reasoning and text, and file names. Large media is sampled (length plus first and last 4 KB). |
| `history_prefix_match` | Turn rows only. 1 if this request's first N history entries (see `history_hash`) hash the same as the previous recorded turn step of this session in **this process**, where N is the number of entries that step sent (not its `message_count`); 0 if they differ. It compares semantic content only, so it matches across runs even though the previous run's last request came from fantasy's in-memory messages and this run's from the database. A provider-specific encoding change that busts the cache, such as a changed reasoning signature, therefore leaves it at 1 and the miss shows as `unexplained`, not `history_rewritten`. **NULL** when: first turn of the session seen by this agent instance (new session, Anvil restart, or a reload that rebuilt the agent); the history got shorter than the previous step's (summarisation, a rewind or switching to a shorter branch; such misses show as `after_summary` or `history_shortened`); a fingerprint error; and always for summary, title and small rows. |
| `fingerprint_error` | Non-empty when hashing failed; hashes may then be empty. |

All hashes are 16 hex characters (truncated SHA-256).

## CTE `step_usage_report` (from `report.sql`)

`step_usage.*` plus the columns below. It is not stored in the database;
wrap it as a CTE as shown in `SKILL.md`.

| Column | Definition |
|---|---|
| `started_utc` | `request_started_at` as UTC datetime text. |
| `model_ms` | `response_finished_at - request_started_at`. Includes retry backoff. |
| `prompt_tokens` | `input + cache_read + cache_write`. |
| `hit_rate` | `cache_read / prompt_tokens`, NULL when 0. |
| `list_cost` | USD from the stored per-row prices. |

## CTE `classified` (from `classify.sql`)

CTE over `step_usage_report`, only `turn` and `summary` rows with a
`session_id`. Rows are compared with the previous row of the same
`(session_id, agent, kind)` ordered by `request_started_at`. Adds:

| Column | Meaning |
|---|---|
| `prev_*` | Previous row's id, provider, model, hashes, finish time, estimated flag, `message_count`. |
| `prev_cached_prefix` | Previous row's `cache_read + cache_write`. |
| `prev_prompt_tokens` | Previous row's `prompt_tokens`. |
| `model_max_read` | Max `cache_read_tokens` over all classified rows of this provider and model. |
| `reuse_gap_ms` | `request_started_at - prev_finished_at`: how long the cached prefix sat idle. Negative for a duplicate row of the same step (unexpected; see Known recording gaps). |
| `after_summary` | 1 if a summary of this session finished in that gap. |
| `baseline` | Tokens this call should have read. `anthropic_ephemeral`: `prev_cached_prefix`, except `prev_prompt_tokens` for `provider_type = 'vercel'` (see the Vercel caveat). `automatic` and `disabled`: `prev_prompt_tokens`. NULL for summary rows, `none` policy and first rows. |
| `suspected_miss` | NULL (not judged: no baseline, baseline under 1024, or this or the previous row estimated), 0 (read at least half the baseline), 1 (miss). |
| `changes` | `first_call`, or space-separated differences from the previous row: `model`, `tools`, `system`, `summary`, `history` (prefix match 0), `shortened` (`message_count` lower than the previous row's). |
| `suspected_cause` | `first_call`; empty if not a miss; else the first match of `cache_disabled`, `no_reads_reported`, `model_changed`, `tools_changed`, `system_changed`, `after_summary`, `history_rewritten`, `history_shortened` (`message_count` dropped with no summary between; `history_prefix_match` is NULL then), `likely_ttl_expired` (gap over 5 min), `unknown_after_restart` (prefix match NULL), `unexplained`. |
| `tokens_not_reused` | Misses only: `baseline - cache_read`, floored at 0. |
| `excess_cost` | Misses only: `tokens_not_reused` times (write price for `anthropic_ephemeral`, else input price, minus read price), in USD. |

## Known recording gaps

- Responses that never reported usage (cancelled or failed before finish)
  have no row.
- With fantasy v0.45.2 a step should never be recorded twice: tool
  errors are no longer retried, so a step that finished is not re-run.
  A duplicate (same `run_id`, `step_index` and `request_started_at`) is
  unexpected; if query 12 finds any, treat it as a recording bug worth
  reporting.
- Rows are written asynchronously. At shutdown Anvil waits up to 2
  seconds for detached title calls, then closes the recorder; a title call
  that outlives that wait loses its row (logged at debug level). Queued
  rows that do not flush within the shutdown deadline are also lost.
- Rows are dropped if the 512-row buffer is full. The first drop and then
  every 100th are logged as a warning with the running `dropped` count.
- The System One bouncer (`internal/systemone`) is not recorded.
