# Devil's Advocate Review — Round 2 Findings

Second review after reconciliation pass. Original 7 critical issues verified as fixed (5 clean, 2 introduced new issues).

## New Critical Issues

### 1. Fragment step-name scoping undefined

Fragment steps reference each other by local name (`{{ preflight.status }}`), but the schema says names are prefixed externally (`preflight-and-commit.preflight`). These are incompatible.

**Resolution**: Define explicit scoping rule — fragment-internal references use local names; prefixed names are only used when referencing fragment steps from outside the fragment.

### 2. Lockstep loop semantics undefined

The schema says consecutive steps with the same `loop.over` "repeat in lockstep" but doesn't define what that means. Two valid readings: interleaved (per-item, all steps before next item) vs. sequential (all iterations of step 1, then all iterations of step 2).

**Resolution**: Define explicitly — consecutive steps sharing the same `loop.over` form a **loop group**. For each item, all steps in the group execute in order before advancing to the next item.

### 3. Structured property access on agentic output

Templates access `.verified_count`, `.title`, `.body` on agentic outputs, but the schema defines agentic output as "the agent's final response" (free-form text). No mechanism for structured extraction exists.

**Resolution**: Define a structured output mechanism — either require the agent to output JSON when structured access is needed, or add an `output.schema` property that the engine uses for extraction.

## Concerns

- **Schema spec examples contradict its own rules**: Fragment example in spec shows `retry` on an agentic step. Actual fragment file correctly omits it. Fix the spec example.
- **03-design.md significantly outdated**: Still uses `phases:`, `includes:`, `assert:`, `mcp:` on deterministic steps, natural-language conditions. Needs a pass or a "superseded by schema spec" notice.
- **`ci-fix` skill undefined**: Referenced in `watch-ci` retry but no definition exists. Needs at least a placeholder description.
- **Interactive output semantics**: `grill` outputs `spec_path` (a file path from the skill), but schema says interactive output is "the user's response." Schema should acknowledge skill-driven output on interactive steps.

## Status of Original 7 Issues

| # | Issue | Status |
|---|-------|--------|
| 1 | `repeat:` → `loop:` | ✅ Fixed |
| 2 | Natural language conditions | ✅ Fixed |
| 3 | Duplicate step name | ✅ Fixed |
| 4 | Dual `agent` + `reference` | ✅ Fixed (schema amended) |
| 5 | Missing instruction sources | ✅ Fixed |
| 6 | Phantom template variables | ⚠️ Mostly fixed; structured access gap remains |
| 7 | `gate:` on agentic step | ✅ Fixed |
