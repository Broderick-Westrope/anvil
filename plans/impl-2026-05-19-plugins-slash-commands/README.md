# Plugins, Slash Commands + Skill Invocation — Implementation Plan

> **Status:** DRAFT

**Spec:**
[`plans/design-2026-05-19-plugins-slash-commands.md`](../design-2026-05-19-plugins-slash-commands.md)

## Phases

| # | Name | Depends On | Effort | PR |
|---|------|-----------|--------|-----|
| 1 | [Agent `.md` migration](phase-1-agent-md-migration.md) | — | 1–2 days | ✅ |
| 1b | [Externalize agent definitions](phase-1b-externalize-agents.md) | Phase 1 | 0.5–1 day | |
| 2 | [Plugin discovery + config](phase-2-plugin-discovery.md) | Phase 1b | 1–2 days | |
| 3 | [Command format + execution](phase-3-command-format.md) | Phase 2 | 1 day | |
| 4 | [`/` autocomplete](phase-4-slash-autocomplete.md) | Phase 3 | 2–3 days | |
| 5 | [Skill attachment + picker](phase-5-skill-attachment.md) | Phase 4 | 1–2 days | |
| 6 | [Plugin reload](phase-6-plugin-reload.md) | Phase 2 | 1 day | |

```
Phase 1 ──► Phase 1b ──► Phase 2 ──► Phase 3 ──► Phase 4 ──► Phase 5
                              │
                              └──► Phase 6 (can start after Phase 2)
```

Phases 5 and 6 are independent of each other and can be parallelized.

## Success Criteria (from spec)

- [ ] Point `plugins` at a directory, get all its skills, commands, and
      agents discovered and available.
- [ ] `/` on empty input opens inline autocomplete showing commands and
      skills with visual distinction.
- [ ] Selecting a command from autocomplete sends its prompt (with argument
      substitution and skill pre-loading).
- [ ] Selecting a skill from autocomplete attaches it as a chip above the
      textarea; it's sent as context with the next message.
- [ ] "Browse Skills" palette command opens a skill picker modal; selected
      skills appear as attachment chips.
- [ ] Plugin agents appear in the orchestrator's routing and are delegatable
      via the task tool.
- [ ] "Reload Plugins" palette command re-discovers all plugin content.
- [ ] Changing the `plugins` key in `anvil.json` triggers plugin reload
      automatically.
- [ ] Name collisions between sources show a namespace prefix in the UI.

## Review Notes

Devil's advocate review caught and addressed:

1. **Priority order contradiction** — Spec said "project > user > plugin
   > builtin" but discovery pipeline description implied the opposite.
   Fixed: discovery order is now explicitly builtins → plugins (reverse)
   → skills_paths, so "last wins" gives the correct priority.
2. **`SetupAgents` refactoring strategy** — Plan was ambiguous about
   whether to bypass or refactor the existing merge. Fixed: refactor
   `SetupAgents` to accept `.md`-derived defaults; keep existing overlay
   logic.
3. **Nil vs empty YAML semantics** — Subtle and easy to get wrong. Added
   explicit tests to Phase 1 Task 1 covering all nil/empty/list cases.
4. **Missing orchestrator `.md` file** — The orchestrator is explicitly
   exempted (uses `orchestrator.md.tpl`, not an agent `.md`).
5. **Collision handling ordering** — Moved earlier conceptually; `Source`
   field added as a prerequisite task in Phase 2.
6. **`$ARGUMENTS` substitution missing** — Added to Phase 3 Task 1.
7. **Autocomplete Item coupling** — Changed from raw domain type pointers
   to opaque string IDs.
8. **Tests co-located with tasks** — Moved from a standalone "Task 4" to
   inline steps within their respective tasks.
