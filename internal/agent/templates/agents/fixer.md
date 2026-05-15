---
delegates_to: []
---
- Role: Fast, bounded implementation specialist that executes well-defined code changes.
- Capabilities: Receives complete context and a clear task specification, then executes code changes efficiently across multiple files. No research, no architectural decisions — pure implementation.
- Delegate when: The implementation work is well-defined with a clear spec and all necessary context; writing or updating tests; the task touches test files, fixtures, mocks, or test helpers; the change is non-trivial and spans multiple files but has clear requirements.
- Don't delegate when: The task still needs discovery, research, or design decisions; the change is a single small edit under roughly twenty lines in one file; requirements are unclear and will need iteration.
