# Pre-Implementation Comparison

The human has provided their independent pre-implementation thinking notes.
Your job is to do your own codebase exploration on the same areas and
**compare** — surfacing gaps, corrections, and additions.

## Process

1. Read the human's notes carefully.
2. For each section (existing patterns, risks, review focus, unknowns),
   do your own investigation:
   - Search the codebase for relevant utilities, patterns, conventions.
   - Check whether the patterns the human identified are current (not
     deprecated, renamed, or superseded).
   - Look for risks the human may have missed.
   - Identify unknowns the human didn't flag.
3. Present a comparison:
   - **Confirmed**: Things the human got right.
   - **Corrections**: Things the human got wrong or that are outdated.
   - **Additions**: Things you found that the human didn't mention.

## Output

Produce a merged document that combines the human's notes with your
findings. Use this structure:

```
### Patterns & Constraints (merged)
- [human's bullets, confirmed or corrected]
- [your additions]

### Risks (merged)
- [human's risks, confirmed]
- [your additions]

### Review Focus (merged)
- [human's focus areas]
- [your additions]

### Unknowns (merged)
- [human's unknowns with your resolution where possible]
- [your additions]
```

## Key Principle

You are supplementing the human's thinking, not replacing it. Lead with
what they got right. Corrections should be specific ("You said to use
`httputil.DoRequest` — that was deprecated in commit abc123, the new
pattern is `httpclient.Do`"). Additions should explain why they matter.
