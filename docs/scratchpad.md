# Some ideas I'v not properly fleshed out / explored yet.

- It would be good if the sidebar was properly scrollable.
  - Perhaps it doesnt need the ANVIL branding so large either.
- Each list section of the sidebar (eg. skills) should be collapsable similar to how OpenCode allows.
- It would be cool if you were able to send messages to subagents to steer them back on course.
- `buildSlashACItems` has O(n²) complexity: it calls `ActiveSkillByName` (linear scan) per skill state. Fix by adding a `Description` field to `SkillState` at discovery time, or exposing a map-based lookup from the coordinator. Low priority — only runs on AC open/reload with typical counts under 100.
- The "home screen" (ie. where you end up when starting a new session) is not to my taste. I'd like to explore what it could look like instead.
- it seems that the timer for subagents isnt working properly / stops ticking at some point.
- the running indicator (animated text shimmer) for subagents is visually buggy. perhaps we use a different loading spinner. the main issue is just that it appears like its staying the same character, but i think its switching between two characters at a rate that makes it seem like only one character for 90% of the time.
- it would be good to include the model on the stats line for subagent summaries
- look at what posthog is being used for and whether it can be ripped out. or perhaps rewired to be useful for myself?
- perhaps skill loading shouldnt require explicit read perms?
- better perm system altogether? what could it look like to have something innovative for perms?
- `anvil_info` is missing `[plugins]`, `[agents]`, and `[commands]` sections. Plugins and agents data lives on the coordinator already; commands only exist in the UI layer and would need threading through. Low priority — completeness, no known pain point.
- I would like some feature akin to Stripe blueprints or Claude workflows built in. Perhaps just the support built in and the workflows themselves can exist in plugins, projects, etc like commands/skills/agents do.
- After migrating to a global DB, consider adding a retention policy for the `files` table (stores full file content, main driver of DB size). Options: `anvil sessions prune` command, auto-purge snapshots older than N days, or size-based limits. Not urgent — growth rate is the same regardless of DB location.
