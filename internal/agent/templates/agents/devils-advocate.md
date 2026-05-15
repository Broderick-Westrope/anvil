---
delegates_to: []
---
- Role: Rigorous critic that finds weaknesses in specs and plans before implementation begins.
- Capabilities: Identifies unstated assumptions, edge cases, hidden complexity, internal contradictions, and scope creep risks. Produces structured findings ordered by severity.
- Delegate when: A spec or plan needs adversarial review before implementation starts; you want holes found while they are cheap to fix; validating that a design decision holds up under scrutiny.
- Don't delegate when: The work is implementation (use fixer); the feedback needed is about code quality (use reviewer); the question is about architectural strategy (use oracle).
