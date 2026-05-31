# Pre-Implementation Thinking

You've just completed grilling and a design spec has been written.

**Before planning begins, YOU need to think through these 5 questions independently.** The agent has not done this analysis — you must do it yourself first. This is about exercising your judgment, not reviewing the agent's judgment.

## The 5 Questions

### Q1. What is actually happening? (3 min)

Restate the feature in your own words. Not the spec's language — YOUR understanding.

- One sentence: what's the user-visible change?
- One sentence: what triggers this code path?
- One sentence: what should happen when it triggers?

**Test**: could you explain this to a colleague in 30 seconds without referencing the spec?

### Q2. What exists already? (4 min)

Before any new code, think about what's already there.

- Is there existing code that does something similar? Where?
- What patterns does the codebase use for this kind of thing?
- What shared utilities, hooks, or helpers are relevant?
- What does the current implementation look like (if modifying, not greenfield)?

Write 3-5 bullets: "Use X for Y", "Follow the pattern in Z", "Don't reinvent W".

### Q3. What could go wrong? (3 min)

- What happens if this fails at runtime?
- What ordering/timing issues exist?
- What does this affect beyond the immediate change?
- What's the rollback story?

Write the 2-3 risks that feel most real.

### Q4. How will I know it's right? (2 min)

- What would I check in code review to feel confident?
- What behaviour would I manually test?
- What's the one thing the agent is most likely to get wrong?

### Q5. What am I not sure about? (3 min)

For each uncertainty, decide:
- Can the agent figure this out?
- Do I need to ask someone?
- Should I investigate more before starting?

**Rule**: if you have more than 2 items in "should investigate more", you're not ready to delegate.

---

## Format

Provide your notes in roughly this shape:

```
### What's happening
[your 3 sentences from Q1]

### Existing patterns to follow
- [bullets from Q2]

### Risks
- [2-3 from Q3]

### Review focus
- [what to check from Q4]

### Unknowns
- [categorised list from Q5]
```

---

## What Happens Next

After you provide your notes, the agent will:

1. Do its own codebase exploration focused on the same areas
2. **Compare** its findings against yours — surfacing gaps, not replacing your thinking
3. Produce a merged output that feeds into the planning phase

The comparison catches things like "You said to use `httputil.DoRequest` — that was deprecated last week, the new pattern is X" or "You identified 3 risks; here's a fourth I found in the error handling path."
