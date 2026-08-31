Launch a specialist agent to handle a task. The `subagent_type` parameter selects which agent to use — see the `<Agents>` block in your system prompt for available agents and when to use each.

Each agent already runs on a model chosen for its role, so omit `model` in almost every case. Only set it (as an exact `provider/model` ID) when you deliberately want a different model — for example running the same agent twice on two models for diverse perspectives. An ID that does not resolve is ignored and the agent's configured model is used instead.

Launch multiple agents concurrently by using multiple task tool calls in a single message. All calls execute in parallel and results are returned together.
