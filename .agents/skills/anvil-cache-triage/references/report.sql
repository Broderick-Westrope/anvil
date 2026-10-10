-- step_usage plus derived columns. No trailing semicolon, so it can be
-- embedded as WITH step_usage_report AS (...) SELECT ...
SELECT
  s.*,
  datetime(s.request_started_at / 1000, 'unixepoch') AS started_utc,
  s.response_finished_at - s.request_started_at AS model_ms,
  s.input_tokens + s.cache_read_tokens + s.cache_write_tokens AS prompt_tokens,
  CASE WHEN s.input_tokens + s.cache_read_tokens + s.cache_write_tokens > 0
       THEN CAST(s.cache_read_tokens AS REAL)
            / (s.input_tokens + s.cache_read_tokens + s.cache_write_tokens)
  END AS hit_rate,
  (s.input_tokens * s.price_input
   + s.cache_read_tokens * s.price_cache_read
   + s.cache_write_tokens * s.price_cache_write
   + s.output_tokens * s.price_output) / 1e6 AS list_cost
FROM step_usage s