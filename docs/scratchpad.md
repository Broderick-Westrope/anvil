# Some ideas I'v not properly fleshed out / explored yet.

- It would be good if the sidebar was properly scrollable.
  - Perhaps it doesnt need the ANVIL branding so large either.
- Each list section of the sidebar (eg. skills) should be collapsable similar to how OpenCode allows.
- It would be cool if you were able to send messages to subagents to steer them back on course.
- Instead of showing the ouput of tool calls in the thread, we should only be showing a one-line summary of the tool call. You should be able to dive deeper into the tool, for example opening a modal which contains the full output of a bash, or the diff for an edit. This modal could also allow copying the diff/output to clipboard in case it's too annoying to explore in that modal.
- There's a styling bug with attached files. The file name doesn't have enough contrast against it's background.
- `buildSlashACItems` has O(n²) complexity: it calls `ActiveSkillByName` (linear scan) per skill state. Fix by adding a `Description` field to `SkillState` at discovery time, or exposing a map-based lookup from the coordinator. Low priority — only runs on AC open/reload with typical counts under 100.
- Currently when anvil is run for the first time in a folder it offers to initialise the project. I don't want it to do this offer. I want initialisation to still be available via the command palette, but I normally decline this proactive offer so we might as well remove it, simplifying the UX and code.
- The "home screen" (ie. where you end up when starting a new session) is not to my taste. I'd like to explore what it could look like instead.
