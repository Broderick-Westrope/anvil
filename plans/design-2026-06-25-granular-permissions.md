# Granular Permissions Design Spec

**Problem:** Anvil's current permissions model is too coarse. `allowed_tools` is a flat list of tool names with no input-level granularity. Users can't express rules like "allow git commands but deny rm" or "allow edits inside src/ but ask for anything else." The runtime prompt offers no way to widen a grant to a pattern, persist it to config, or provide denial reasons to the agent.

**Goal:** A pattern-based permissions system where users configure per-tool, per-input rules with `allow`/`ask`/`deny` actions, and a runtime prompt that lets users grant or deny with full control over scope and persistence.

**Scope:**

In scope:
- Granular permission rules keyed by tool name (glob + brace expansion) with optional sub-rules matching tool input (glob)
- Three permission actions: `allow`, `ask`, `deny`
- Runtime prompt with: allow once, allow for session, allow forever (project or user scope), deny with optional reason
- Editable pattern field in the runtime prompt (pre-filled with exact input)
- Last-match-wins rule evaluation
- Default action: `ask` when no rule matches
- Project-level config (`./anvil.json`) overrides user-level (`~/.config/anvil/anvil.json`)
- Yolo mode: three levels (off, standard, full) — standard skips `ask` but respects `deny`, full overrides everything
- Yolo level selectable via `--yolo` / `--yolo=full` flag and toggleable in TUI
- "Allow forever" writes the (potentially edited) pattern to the chosen config file
- Deny reason fed back to agent as tool output
- Rule evaluation details logged (which rules matched, which were overridden)
- Replaces `allowed_tools` (deprecated)
- Invalid patterns fail at startup

Out of scope:
- Regex support (globs + brace expansion only)
- Named groups / aliases for tool sets
- Input-level granularity for MCP tools (just tool name matching)
- Changes to `disabled_tools` (stays separate, takes precedence over permissions)
- Changes to hooks (remain separate, evaluated before permissions)

**Constraints:**
- Config must be valid JSON (`anvil.json`)
- Glob + brace expansion at both layers (tool name matching and input matching)
- Must not break existing hook-based workflows
- `disabled_tools` takes precedence — disabled tools are filtered at registration, before permissions
- Per-agent `AllowedTools` takes precedence over permissions — if an agent restricts its tool set, permissions only apply to tools the agent can see
- Hooks evaluated before permissions — hook `allow` bypasses permission check, hook `deny` blocks before permission check
- Startup must fail with clear error on malformed permission patterns
- Bash sub-rules are best-effort pattern matching, not a security sandbox — commands using `&&`, pipes, subshells, or `sh -c` can bypass simple patterns. Users requiring strict command control should use hooks with proper shell parsing.
- Concurrent config writes (from parallel permission prompts) must be serialized to prevent clobbering

**Success Criteria:**
- [ ] Users can configure per-tool permission rules with glob patterns in `anvil.json`
- [ ] Users can configure per-input sub-rules (e.g. `"git *": "allow"` under `bash`)
- [ ] Tool name keys support glob and brace expansion (e.g. `"{edit,write,multiedit}"`)
- [ ] Last matching rule wins when multiple rules match
- [ ] Default is `ask` when no rule matches
- [ ] Runtime prompt shows tool name, input, and editable pattern field
- [ ] "Allow once" grants for the current request only
- [ ] "Allow for session" applies the (potentially edited) pattern as an ephemeral rule for the session
- [ ] "Allow forever" writes the (potentially edited) pattern to project or user `anvil.json`
- [ ] "Deny" accepts optional reason text, fed back to agent as tool output
- [ ] Project-level config overrides user-level config
- [ ] `--yolo` skips `ask` but respects `deny`; `--yolo=full` overrides everything
- [ ] Yolo level is toggleable in the TUI (off → standard → full → off cycle)
- [ ] Invalid glob patterns cause startup failure with descriptive error
- [ ] `allowed_tools` is deprecated: if present, translated to equivalent permission rules in memory at load time with a deprecation warning logged; error if both `allowed_tools` and `permissions` are present
- [ ] Rule evaluation details are written to logs
- [ ] Hotkeys work when pattern field is not focused (a, s, f, d)
- [ ] Permission modal buttons are on one line, wrapping naturally at max width

**Design Decisions:**

- **Tool name keys, not categories.** Categories add an indirection layer that needs maintenance. Tool names are predictable — `"bash"` in config maps directly to the `bash` tool. Decided over abstract categories (like OpenCode's 13 categories) to keep the system simple.

- **Globs + brace expansion, not regex.** Globs are familiar from shell usage and cover practical cases. Brace expansion solves the grouping problem (`"{edit,write,multiedit}"`) without introducing named groups. Regex is more powerful but overkill for matching tool names and file paths.

- **Last match wins with ordered parsing.** Simple to implement, simple to explain. Go maps are unordered, so the config is parsed with a custom `UnmarshalJSON` that reads JSON object keys into an ordered `[]PermissionRule{Pattern, Action}` slice, preserving source order. The JSON config format stays the same — the ordering guarantee is an implementation detail. This avoids needing a specificity definition and matches user expectations from tools like OpenCode.

- **Default `ask`.** Safe middle ground — `deny` would break unconfigured tools, `allow` silently permits everything. Users only need to configure tools they have strong opinions about.

- **`allowed_tools` deprecated, not coexisting.** The new system is strictly more expressive — `allowed_tools: ["bash"]` is just `"bash": "allow"`. Keeping both creates confusing interactions.

- **`disabled_tools` stays separate.** Different mechanism (tool registration filtering vs runtime permission checking) and different purpose (invisible to LLM vs denied with feedback). Adding a fourth permission level like `"hidden"` would muddy the model.

- **Hooks remain separate, evaluated first.** Hooks run arbitrary logic and serve a different purpose than static pattern matching. Hook `allow` → bypass permissions. Hook `deny` → block before permissions. Hook `none` → fall through to permissions.

- **Deny reason goes to agent.** The purpose of a reason is to steer the agent. If it only went to logs, the agent would retry the same action.

- **"Allow forever" writes specific patterns.** Approving `git status` adds `"git status": "allow"` (or whatever the user edits it to) under `bash`. This preserves granularity and makes the config file a readable audit trail. Config writes use a dedicated `SetPermissionRule` method (read → unmarshal → modify → marshal → write) rather than `sjson.Set`, because sjson interprets dots as path separators, which would corrupt glob patterns containing dots (e.g. `"*.go"`).

- **Yolo respects deny by default.** If someone explicitly denied `"rm -rf *"`, they want that enforced even in convenience mode. `--yolo=full` exists for when you truly want no guardrails.

- **Fail at startup on invalid patterns.** Permissions are a security boundary — silently ignoring a malformed deny rule could allow something the user intended to block.

- **Project overrides user config.** More specific scope wins, matching conventions of git config, VSCode settings, etc. Merge strategy: concatenate user-level rules first, then project-level rules after. Since last-match-wins, project rules naturally take precedence. This is append-based, not key-based merging — both rulesets are fully preserved, avoiding silent loss of user rules when a project defines rules for the same tool.

**Config Schema:**

```json
{
  "permissions": {
    "*": "ask",
    "bash": {
      "*": "ask",
      "git *": "allow",
      "git push *": "deny",
      "rm *": "deny"
    },
    "{edit,write,multiedit}": {
      "*": "allow",
      "vendor/*": "deny"
    },
    "{view,ls}": "allow",
    "{fetch,download,agentic_fetch}": {
      "*": "ask",
      "https://docs.*": "allow"
    },
    "mcp_Sourcebot_*": "allow",
    "task": "allow",
    "lsp_rename": "ask"
  }
}
```

String value = all inputs get that action. Object value = input-level pattern matching with last-match-wins.

**Tool Input Matching:**

| Tool | Input matched against |
|---|---|
| `bash` | Command string |
| `edit` / `multiedit` / `write` | File path |
| `view` / `ls` | File path |
| `fetch` / `download` / `agentic_fetch` | URL |
| `lsp_rename` / `lsp_replace_symbol` | File path |
| `task` | Subagent type |
| All other tools (`glob`, `grep`, `sourcegraph`, LSP tools, MCP tools, `enable_mcp`, etc.) | Tool name only (no sub-rules) |

**Evaluation Order:**

```
1. disabled_tools check → tool not registered, LLM never sees it
2. Hook PreToolUse → allow (bypass permissions) / deny (block) / none (continue)
3. Permission rules:
   a. Build ordered rule list: user-level rules, then project-level rules appended after
   b. Append session grants (ephemeral rules from "allow for session") last
   c. Walk rules in order; for each rule where tool-name glob matches:
      - If rule is string → record action, continue scanning
      - If rule is object → walk sub-rules in order, for each where input glob matches, record action
   d. Last recorded action wins
   e. If no rule matched → default action (ask)
   f. If yolo=standard → ask becomes allow, deny stays
   g. If yolo=full → everything becomes allow
4. If action is ask → show runtime prompt, block until user responds
5. If action is deny → return denial to agent (with reason if provided)
6. If action is allow → execute tool
```

Session grants can only upgrade `ask` → `allow`, never `deny` → `allow`. This is enforced at evaluation time, not creation time: after the full rule walk, if the last *config-level* action (from user + project rules, ignoring session grants) was `deny`, the final action remains `deny` regardless of any session grant. This prevents broad session patterns (e.g. `*`) from silently overriding narrower config deny rules (e.g. `rm *`).

**Runtime Prompt:**

```
┌─ Permission Required ──────────────────────────────────────┐
│                                                            │
│  bash: git push origin main                                │
│                                                            │
│  Pattern: [git push origin main________________________]   │
│                                                            │
│  [Allow (a)] [Session (s)] [Forever (f)] [Deny (d)]       │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

- Buttons on one line, wrap naturally at max modal width
- Pattern field pre-filled with exact input, editable
- Hotkeys active when pattern field is not focused
- `f` expands to `[Project (p)] [User (u)]` sub-choice inline, escape backs out
- `d` opens optional text input for denial reason
- Session grants use the (potentially edited) pattern as an ephemeral rule with same glob matching
- Forever grants write the pattern to chosen config file

**Context Files:**
- `internal/permission/permission.go` — current permission service, `Grant`, `GrantPersistent`, `Deny`, `Request` flow
- `internal/config/config.go` — `Permissions` struct, `AllowedTools`, `Options.DisabledTools`
- `internal/config/load.go` — config loading and validation
- `internal/agent/coordinator.go` — tool registration, `buildToolsWithState`, `wrapToolsWithHooks`
- `internal/agent/hooked_tool.go` — hook → permission integration
- `internal/hooks/` — hook runner, decisions, input/output protocol
- `internal/cmd/root.go` — `--yolo` flag handling
- `internal/ui/` — TUI components (permission dialog will live here)
