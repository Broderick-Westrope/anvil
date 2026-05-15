Launch a specialized agent in the background and return immediately. The agent runs concurrently while the orchestrator continues with other work.

**Parameters:**
- `prompt`: The complete task description for the agent.
- `subagent_type`: The name of the specialized agent to use (e.g. "explorer", "fixer", "reviewer").
- `description`: A short (3–5 word) label used as the session title (e.g. "Search auth code").

**Returns:** A `task_id` string. The task runs asynchronously; results are delivered as a notification when the task completes.

Use this tool when you want to fire multiple independent tasks in parallel and do not need the result before proceeding. For tasks where you need the result before continuing, use the `task` tool instead.

Up to 10 background tasks may run concurrently. If the limit is reached, the tool returns an error — wait for existing tasks to finish before launching new ones.

Background tasks are automatically cancelled if the parent session is cancelled.
