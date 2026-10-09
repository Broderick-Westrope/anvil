# Interpreting Suspected Causes

Each `suspected_cause` is a guess from fingerprints and timing. Confirm it
from the row's `changes`, `reuse_gap_ms`, `history_prefix_match` and
`raw_usage` (and the neighbouring rows in the session timeline) before
reporting it. "Expected" patterns are a normal cost of how caching works;
"suspicious" ones are worth a fix.

Anthropic-style caching (`anthropic_ephemeral`) is a prefix cache in the
order tools, system, messages. A change in one part invalidates it and
everything after it, so a tools change loses the whole prefix and a system
change keeps only the tools part.

## Pattern table

| Cause / pattern | Evidence to check | Likely Anvil cause | Candidate fix | Verdict |
|---|---|---|---|---|
| `first_call` | First row of a session/agent sequence. | Cold start: the whole prefix is written. | None per call. Large totals across many short subagent sessions point to the subagent pattern below. | Expected |
| `tools_changed` | `changes` has `tools`; `tool_count` differs from the previous row. Usually the first step after an `enable_mcp` call, a Reload Config, or an MCP server reconnect. | The tool list is rebuilt each step from `a.tools` filtered by lazy MCP state (the `PrepareStep` callback in `runOwned`, `internal/agent/agent.go`; `filterLazyMCPTools` in `internal/agent/lazy_mcp.go`; `sessionAgent.SetTools`; `internal/agent/tools/enable_mcp.go`). Tools come first in the prompt, so any change misses everything. | Announce newly enabled tools through messages instead of the tool list; keep tool order stable; pre-declare lazy MCP tools so enabling them does not change the list. | Suspicious if frequent; one per `enable_mcp` is the known cost. |
| `system_changed` | `changes` has `system`, often with `history_prefix_match` NULL (agent rebuilt). | The system prompt was rebuilt (reload, instance reload, skill or memory file change). The `environment` block of `internal/agent/templates/base.md.tpl` embeds today's date and a git status snapshot (`Prompt.promptData` in `internal/agent/prompt/prompt.go`), so a rebuild after a commit or across midnight changes it. | Move volatile data (date, git status) out of the system prompt, for example into the first user message. | Suspicious when it follows a reload with no config change. |
| `after_summary` | `changes` has `summary`; a summary row sits between this and the previous turn; `history_prefix_match` usually NULL. | Summarisation replaces history, so only tools and system can be reused. | None needed; check the tools+system part was still read (`cache_read_tokens` about the size of tools+system). | Expected |
| `history_rewritten` | `history_prefix_match = 0` with no summary between. | Expected after a branch switch or message edit. Otherwise an earlier message changed between steps: `workaroundProviderMediaLimitations` (`internal/agent/agent.go`) rewriting media; the Anthropic OAuth transform (`transformForAnthropicOAuth`, `internal/agent/anthropic_oauth.go`); or job notices injected into history (`injected.apply` in the `PrepareStep` callback of `runOwned`, `deliverJobEvents` in `internal/agent/job_events.go`). Message grouping and provider metadata are not hashed, so fantasy's in-run messages and Anvil's database rebuild on the next run match unless their content differs. | Make the rewrite deterministic and stable across steps; persist exactly what was sent. | Suspicious unless a branch switch or edit explains it. |
| `likely_ttl_expired` | `reuse_gap_ms > 300000` with no fingerprint change. | Anthropic's default cache TTL is 5 minutes. The gap counts from the previous response to this request, so long tool runs and user think time both count. | Anthropic's 1-hour TTL on the tools and system breakpoint (`getCacheControlOptions` in `internal/agent/agent.go`). Weigh against the higher 1-hour write price: worth it only if gaps of 5-60 min are common and the tools+system prefix is large. | Expected for idle sessions; suspicious if the gaps are tool runtime. |
| `model_changed` | `changes` has `model`. | Mid-session model switch. Caches are per model. | None. | Expected |
| `unknown_after_restart` | `history_prefix_match` NULL, no other change, gap under 5 min. | The agent instance had no previous step to compare against (restart, rebuilt agent) or history shrank. | Inconclusive. Look at the timeline; do not report as a cause on its own. | Inconclusive |
| `unexplained` | Miss with identical hashes, prefix match 1, short gap. | Breakpoint placement in the `PrepareStep` callback of `runOwned` (`internal/agent/agent.go`: last system message and last two messages), provider-side eviction, a provider-specific encoding change the semantic history hash ignores (reasoning signatures or metadata lost or changed when Anvil rebuilds history from the database, message grouping, whitespace), or a bug in Anvil or fantasy. | Inspect `raw_usage` for the row and its predecessor; reproduce in an isolated session. | Suspicious |
| `cache_disabled` | `cache_policy = 'disabled'`. | `ANVIL_DISABLE_ANTHROPIC_CACHE` was set. | Unset it. | Expected given the setting |
| `no_reads_reported` | This provider+model has 0 cache reads across all rows; see queries.md section 10. | The provider does not cache, or reports reads in a field `cacheusage.Normalise` does not map. | Inspect `raw_usage.extra` for a cache field (for example DeepSeek's `prompt_cache_hit_tokens` is already mapped); add the provider's field to `internal/agent/cacheusage/normalise.go`. | Suspicious until `raw_usage` is checked |
| Summary rows with 0 reads | `kind = 'summary'`, `cache_read_tokens = 0`. | The summary request sets no cache-control options (`summarizeOwned` in `internal/agent/agent.go`), and sends no tools, so even its shared prefix is not reused. | Send the summary with the same tools, system and breakpoints as turns so it reads the turn cache. | Expected today; a fix candidate if summaries are frequent and large. |
| High cache writes on subagents | queries.md section 6: `child_write` large relative to `own_write`. | Each specialist and `agentic_fetch` session has its own tools and system prompt, so it pays its own `first_call` write. | Share a common prefix (tools, system) across specialists, or delegate less for small tasks. | Expected per subagent; suspicious when many short subagents dominate writes. |
| `retry_count > 0` clusters | queries.md section 12. | Provider instability or rate limits. | Not a cache issue; report separately. | Not a cache issue |

## Judgement rules

- Rank causes by `tokens_not_reused`, then check `excess_cost`; a few
  `model_changed` misses on an expensive model can cost more than many
  cheap misses.
- `suspected_miss` is NULL for prefixes under 1024 tokens (below
  Anthropic's minimum cacheable size) and for estimated rows. Do not count
  these as hits or misses.
- `automatic` providers cache in fixed-size blocks (OpenAI: 1024 tokens
  then 128-token steps), so a hit reads slightly less than the whole
  previous prompt. A row counts as a hit when it reads at least half its
  `baseline`.
- Google hit rates may be inflated (see `schema.md`, Google caveat).
- Duplicate rows for one step (same `run_id` and `step_index`) are both
  billed; the second shows a negative `reuse_gap_ms`.
