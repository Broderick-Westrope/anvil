# Review Merge Rules

Instructions for merging findings from parallel dual-model code review.

## Matching Findings

Two findings **match** when they reference the **same file and line** (or overlapping line range) AND describe the **same underlying issue**. Minor wording differences don't matter — match on substance.

## Merge Rules

| Scenario | Action |
|----------|--------|
| Both reviewers found the same issue | Single entry, mark `[Sonnet + Opus]` — high confidence |
| Only one reviewer found it | Single entry, mark `[Sonnet]` or `[Opus]` |
| Reviewers disagree on severity | Use the higher severity, note the disagreement |
| Reviewers contradict each other | Include both perspectives inline, let user decide |

## Verdict Logic

| Sonnet Verdict | Opus Verdict | Merged Verdict |
|---------------|-------------|----------------|
| APPROVE | APPROVE | APPROVE |
| APPROVE | REQUEST CHANGES | REQUEST CHANGES |
| REQUEST CHANGES | APPROVE | REQUEST CHANGES |
| REQUEST CHANGES | REQUEST CHANGES | REQUEST CHANGES |

## Failure Handling

If one reviewer fails, errors, or times out:
- Proceed with the surviving reviewer's output
- Note: "Single-model review — [Opus/Sonnet] reviewer failed"
- All findings attributed to the surviving reviewer only

## Output Format

```markdown
# Code Review

## Summary
- **Files changed**: X files (+Y/-Z lines)
- **Reviewers**: Sonnet + Opus (parallel)
- **Agreement**: X of Y findings confirmed by both models

## Critical Issues ⛔
- `[Sonnet + Opus]` `file.go:123` - Description
- `[Opus]` `file.go:456` - Description

## Important Issues ⚠️
- `[Sonnet + Opus]` `file.go:789` - Description

## Suggestions 💡
- `[Sonnet]` `file.go:012` - Description

## Verdict
**[APPROVE | REQUEST CHANGES]** - Explanation
```
