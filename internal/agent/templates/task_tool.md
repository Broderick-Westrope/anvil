Launch a specialist agent to handle a task. The `subagent_type` parameter selects which agent to use — see the `<Agents>` block in your system prompt for available agents and when to use each.

Each agent already runs on a model chosen for its role, so omit `model` in almost every case. Only set it (as an exact `provider/model` ID) when you deliberately want a different model — for example running the same agent twice on two models for diverse perspectives. An ID that does not resolve is ignored and the agent's configured model is used instead.

Launch multiple agents concurrently by using multiple task tool calls in a single message. All calls execute in parallel and results are returned together.

Every result ends with a `subagent_id` line. To continue a subagent you spawned, whether it succeeded, failed or was cancelled, call `task` with `resume` set to its `subagent_id` and `prompt` set to your follow-up. It keeps its full history, so don't repeat context it already has. `subagent_type` and `model` are inherited; omit them. You can't resume a subagent that is still running.

Transient provider failures are retried automatically within the same call; the result says when that happened. A human may also message a subagent while it runs; anything they sent is listed at the end of its result, word for word.
