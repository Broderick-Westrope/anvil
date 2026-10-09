-- Classifies turn and summary rows of step_usage_report. Each row is
-- compared with the previous row of the same (session_id, agent, kind)
-- sequence. Suspected causes are heuristics, not ground truth. No trailing
-- semicolon, so it can be embedded as WITH classified AS (...) SELECT ...
WITH seq AS (
  SELECT r.*,
    LAG(r.id)                    OVER w AS prev_id,
    LAG(r.provider)              OVER w AS prev_provider,
    LAG(r.model)                 OVER w AS prev_model,
    LAG(r.tools_hash)            OVER w AS prev_tools_hash,
    LAG(r.system_hash)           OVER w AS prev_system_hash,
    LAG(r.cache_read_tokens + r.cache_write_tokens) OVER w AS prev_cached_prefix,
    LAG(r.prompt_tokens)         OVER w AS prev_prompt_tokens,
    LAG(r.response_finished_at)  OVER w AS prev_finished_at,
    LAG(r.estimated)             OVER w AS prev_estimated,
    MAX(r.cache_read_tokens) OVER (PARTITION BY r.provider, r.model) AS model_max_read
  FROM step_usage_report r
  WHERE r.session_id != '' AND r.kind IN ('turn', 'summary')
  WINDOW w AS (
    PARTITION BY r.session_id, r.agent, r.kind
    ORDER BY r.request_started_at, r.response_finished_at, r.id
  )
),
based AS (
  SELECT seq.*,
    seq.request_started_at - seq.prev_finished_at AS reuse_gap_ms,
    CASE WHEN seq.prev_finished_at IS NOT NULL AND EXISTS (
      SELECT 1 FROM step_usage s
      WHERE s.session_id = seq.session_id AND s.kind = 'summary'
        AND s.response_finished_at > seq.prev_finished_at
        AND s.response_finished_at <= seq.request_started_at
    ) THEN 1 ELSE 0 END AS after_summary,
    -- Tokens this call could have read from cache. Summary calls carry no
    -- cache markers, tools or prefix check, so they get no baseline.
    CASE WHEN seq.kind = 'turn' THEN
      CASE seq.cache_policy
        -- fantasy's Vercel usage mapping fills cache reads but never cache
        -- writes, so prev_cached_prefix undercounts; use the whole prompt.
        WHEN 'anthropic_ephemeral' THEN
          CASE WHEN seq.provider_type = 'vercel' THEN seq.prev_prompt_tokens
               ELSE seq.prev_cached_prefix END
        WHEN 'automatic' THEN seq.prev_prompt_tokens
        WHEN 'disabled' THEN seq.prev_prompt_tokens
      END
    END AS baseline
  FROM seq
),
judged AS (
  SELECT based.*,
    CASE
      WHEN baseline IS NULL OR baseline < 1024 OR estimated = 1 OR prev_estimated = 1 THEN NULL
      WHEN cache_read_tokens >= baseline / 2 THEN 0
      ELSE 1
    END AS suspected_miss
  FROM based
)
SELECT judged.*,
  CASE WHEN prev_finished_at IS NULL THEN 'first_call' ELSE trim(
       CASE WHEN prev_provider != provider OR prev_model != model THEN 'model ' ELSE '' END
    || CASE WHEN prev_tools_hash != tools_hash THEN 'tools ' ELSE '' END
    || CASE WHEN prev_system_hash != system_hash THEN 'system ' ELSE '' END
    || CASE WHEN after_summary = 1 THEN 'summary ' ELSE '' END
    || CASE WHEN history_prefix_match = 0 THEN 'history ' ELSE '' END)
  END AS changes,
  CASE
    WHEN prev_finished_at IS NULL THEN 'first_call'
    WHEN suspected_miss IS NOT 1 THEN ''
    WHEN cache_policy = 'disabled' THEN 'cache_disabled'
    WHEN model_max_read = 0 THEN 'no_reads_reported'
    WHEN prev_provider != provider OR prev_model != model THEN 'model_changed'
    WHEN prev_tools_hash != tools_hash THEN 'tools_changed'
    WHEN prev_system_hash != system_hash THEN 'system_changed'
    WHEN after_summary = 1 THEN 'after_summary'
    WHEN history_prefix_match = 0 THEN 'history_rewritten'
    WHEN reuse_gap_ms > 300000 THEN 'likely_ttl_expired'
    WHEN history_prefix_match IS NULL THEN 'unknown_after_restart'
    ELSE 'unexplained'
  END AS suspected_cause,
  CASE WHEN suspected_miss = 1 THEN MAX(baseline - cache_read_tokens, 0) ELSE 0 END AS tokens_not_reused,
  -- Extra list cost of re-sending the prefix: billed as a cache write (or
  -- input) instead of a cache read.
  CASE WHEN suspected_miss = 1 THEN
    MAX(baseline - cache_read_tokens, 0)
    * (CASE WHEN cache_policy = 'anthropic_ephemeral' AND price_cache_write > 0
            THEN price_cache_write ELSE price_input END
       - price_cache_read) / 1e6
  ELSE 0 END AS excess_cost
FROM judged