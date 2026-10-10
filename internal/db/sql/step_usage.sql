-- name: InsertStepUsage :exec
INSERT INTO step_usage (
    id, session_id, parent_session_id, working_dir, message_id, agent, kind,
    depth, run_id, step_index, attempt, provider, provider_type, model,
    request_started_at, response_finished_at, retry_count, finish_reason,
    input_tokens, cache_read_tokens, cache_write_tokens, output_tokens,
    reasoning_tokens, estimated, raw_usage,
    price_input, price_output, price_cache_read, price_cache_write, flat_rate,
    cache_policy, message_count, system_count, tool_count,
    tools_hash, system_hash, history_hash, history_prefix_match,
    fingerprint_error
) VALUES (
    ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?, ?,
    ?
);

-- name: DeleteStepUsageBefore :exec
DELETE FROM step_usage WHERE response_finished_at < ?;
