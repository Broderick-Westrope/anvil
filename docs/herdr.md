# Herdr Integration

When the interactive Anvil TUI runs inside a [Herdr](https://herdr.dev)
pane, it reports its status to Herdr automatically. There is nothing to
configure. `anvil run` never reports.

## What Anvil Reports

- **Agent:** the pane shows up as agent `anvil` as soon as the TUI starts.
- **Status:**
  - `blocked` while any permission prompt is pending, including prompts
    from sub-agents.
  - `working` while any session is busy, including background sessions.
  - `idle` otherwise. Herdr shows `done` when a run finishes while you are
    not looking at the pane.
- **Session:** the ID of the session currently displayed, updated when you
  switch sessions.

On exit Anvil releases the pane, so Herdr stops listing it as an agent.

## Tab Naming

Anvil names its tab after the displayed session title, so Herdr's tab bar,
Agent panel and toasts identify the session. The label has control
characters removed and is capped at 30 characters.

Anvil only renames a tab when:

- it is the only pane in the tab, and
- the tab is unnamed (its label is just its position number) or still
  carries the name Anvil gave it.

If you rename the tab yourself, Anvil leaves it alone for the rest of the
process. A name made only of digits (e.g. `2026`) looks unnamed and will be
replaced. If Anvil's pane is moved to another tab, that tab is named too.

On a clean exit (and before `/reload-instance`) the tab reverts to its
position number. After a crash the session name stays, and the next Anvil
in that tab treats it as a name you chose.

## Terminal Title

Anvil sets the terminal title to `<session title> · anvil`, or
`anvil <directory>` when no titled session exists. This applies in any
terminal. In Herdr it is available as the `terminal_title_stripped`
sidebar token, which helps tell apart Anvil panes in tabs Anvil does not
rename.

## Activation

Reporting starts only when all of these hold at startup:

- `HERDR_ENV=1` and `HERDR_PANE_ID` are set.
- `HERDR_SOCKET_PATH` points to an existing socket.
- An absolute `herdr` binary resolves from `HERDR_BIN_PATH` or `PATH`.
- `ANVIL_HERDR_REPORTING` is not set.

These are read from the environment Anvil was launched with, so a project
`.env` cannot turn reporting on. When reporting starts, Anvil sets
`ANVIL_HERDR_REPORTING=1` for its child processes, so an Anvil started from
a tool call inside the pane does not fight its parent over the pane's
status. If reporting is inactive inside Herdr, the reason is logged once
(see `anvil logs`).

## Recommended Herdr Config

The tab row carries the session name. Herdr does not accept custom agent
IDs in `rows_by_agent`, so these rows apply to all agents.

```toml
[ui]
agent_panel_sort = "priority"   # done/blocked float to the top
status_indicators = "symbols"   # distinct shapes for done/blocked/working
prompt_new_tab_name = false     # let Anvil name tabs

[ui.sidebar.agents]
rows = [["state_icon", "state_text", "workspace"], ["tab"]]

[ui.toast]
delivery = "herdr"
```

Add `terminal_title_stripped` to a row if you want the session title shown
for panes in split or user-named tabs.
