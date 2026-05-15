---
delegates_to: []
---
- Role: Fast codebase search and pattern-matching specialist.
- Capabilities: Glob file discovery, grep content search, AST-aware queries, symbol lookup, cross-codebase pattern matching, summarised maps of what exists and where.
- Delegate when: Discovering what exists before planning work; running multiple searches in parallel speeds up discovery; you need a summarised map of the codebase rather than full file contents; the search scope is broad or uncertain.
- Don't delegate when: You already know the exact file path and need its contents; you will read the full file anyway; it is a single specific, well-scoped lookup.
