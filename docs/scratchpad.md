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
- File versioning is not branch-aware. The `files` table predates branching and has no `message_id` column — all branches within a session share a single linear version sequence scoped to `(path, session_id)`. When a user navigates back and starts a new branch, the agent sees the abandoned branch's file versions as "latest," falsely detects user manual edits, and stores spurious intermediate versions. Fix likely involves adding `message_id` to the `files` table and scoping queries to the current branch path. This may tie into a "branch summaries" feature — when navigating back, generating a summary of what the abandoned branch did (including its file changes) would benefit from branch-scoped file tracking.
- After migrating to a global DB, add a configurable session retention policy. Measurement showed `messages` is 77% of storage (45 MB), `files` is 22% (13 MB) — so retention should target whole sessions, not just file snapshots. Options: configurable `session_retention` in `anvil.json` (e.g. `"6m"`), `anvil sessions archive` command to export and remove old sessions, auto-archive on startup for sessions older than the configured threshold. Archived sessions could be exported to JSONL before deletion for offline reference. Sessions with `CASCADE DELETE` on messages/files means archiving a session cleans up all related data in one operation.
