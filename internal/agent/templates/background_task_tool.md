Launch a specialized agent in the background and return immediately. The agent runs concurrently while you continue with other work.

**Parameters:**
- `prompt`: The complete task description for the agent.
- `subagent_type`: The name of the specialized agent to use (e.g. "explorer", "fixer", "reviewer").
- `description`: A short (3–5 word) label used as the session title (e.g. "Search auth code").

**Returns:** A `task_id` string identifying the background task.

**How results are delivered:** Results are automatically injected into your next turn's context when the background task completes. You do NOT need to poll for results — there is no tool to check on background tasks. Do NOT use `job_output`, `bash`, or any other tool to try to retrieve results. Simply continue working on other things; results will appear automatically when you next respond.

**When to use:** Fire multiple independent research or implementation tasks in parallel when you don't need results before proceeding. For tasks where you need the result immediately, use the blocking `task` tool instead.

**Do NOT** spawn a blocking `task` just to "wait" for background tasks to finish. Continue doing useful work or respond to the user — results arrive on your next turn.

Up to 10 background tasks may run concurrently. If the limit is reached, the tool returns an error — wait for existing tasks to finish before launching new ones.

Background tasks are automatically cancelled if the parent session is cancelled.
