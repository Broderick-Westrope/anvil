Launch a specialist agent to handle a task. The `subagent_type` parameter selects which agent to use — see the `<Agents>` block in your system prompt for available agents and when to use each.

The optional `model` parameter overrides the agent's model for this call (format: `provider/model`, e.g. `anthropic/claude-opus-4-6`). Omit to use the agent's configured default. This enables dual-model patterns — run the same agent with two different models in parallel for diverse perspectives.

Launch multiple agents concurrently by using multiple task tool calls in a single message. All calls execute in parallel and results are returned together.
