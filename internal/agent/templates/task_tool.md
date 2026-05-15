Launch a specialized sub-agent to handle a specific task. Use this tool to delegate work to agents with particular expertise.

The `subagent_type` parameter selects which specialist to use. Available agent types:

- **oracle**: Strategic advisor for deep reasoning, architecture decisions, and complex debugging.
- **explorer**: Read-only specialist for exploring and understanding codebases.
- **librarian**: Research specialist with web search, documentation, and external knowledge access.
- **designer**: UI/UX specialist with browser tools for visual and front-end work.
- **fixer**: Fast, bounded implementation specialist that executes well-defined code changes.
- **planner**: Planning specialist for designing solutions, writing plans, and drafting technical specs.
- **tester**: Testing specialist for running tests, writing test cases, and debugging flaky tests.
- **reviewer**: Code review specialist for assessing quality, correctness, and style.
- **devils-advocate**: Critical analysis specialist for identifying problems, risks, and failure modes.

Select the `subagent_type` that best matches the nature of the task you are delegating.

Launch multiple agents concurrently by using multiple task tool calls in a single message. All calls execute in parallel and results are returned together.
