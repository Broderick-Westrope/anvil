# Architecture Survey: Permission Decision Path

**Scope:** `internal/permission/...`, `internal/bouncer`, `internal/systemone`,
`internal/hooks`, `internal/config/{permissions,bouncer,yolo}.go`,
`internal/ui/dialog/permissions.go`, and the tool-side callers of
`permission.Service` in `internal/agent/tools`. The complexity of `Request`,
`segment.split`, `triage.Analyze` and `ComputeStats` is handled by #83 and
left out here.
**Method:** six-month churn and size, one explorer tracing a single tool
call end to end, then every candidate below re-read at the cited lines.
2026-10-10.

Churn (six months, non-test): `ui/dialog/permissions.go` 14,
`permission/permission.go` 12, `config/bouncer.go` 6, `segment.go` 5,
`hooks/runner.go` 5. Most of this area is the user's code;
`permission.go` is about a quarter the user's, the rest upstream.

## Top Recommendation

**Candidate 1, the bash safe-command bypass.** It is the only finding here
with a security cost today. Commands matching a "safe" prefix never reach
the permission service, so config deny rules, the bouncer and the prompt
are all skipped, and the prefix check is easy to slip past. Fixing it is
small and makes every later permission change apply to bash uniformly.

## Candidates

### 1. The bash safe-command list bypasses the permission service (Strong, security)

**Status (2026-10-12):** Done in #91. Safety is now decided from the parsed command.

- **Flags:** Duplicated concept, Information leakage
- **Evidence:**
  - `tools/bash.go:262-274` marks a command safe when it starts with an
    entry from `safeCommands` (`tools/safe.go:9-58`) followed by end, space
    or `-`, and contains none of `; | && $( \``
    (`safe.go:60-75`). Safe commands skip `permissions.Request` entirely
    (`bash.go:281`).
  - Wrappers on the list run anything: `env`, `nice`, `nohup`, `time`,
    `timeout` (`env sh -c '...'`, `timeout 5 rm -rf x`).
  - Destructive commands pass as well: `kill` and `killall` are on the
    list, and the `git branch`, `git tag` and `git remote` prefixes admit
    their mutating forms (`git branch -D`, `git tag -d`,
    `git remote remove`).
  - The chaining check misses a newline (`ls -la\nrm -rf x` starts with
    `ls `), a single `&` (`ls & rm -rf x`), redirects (`echo x > ~/.zshrc`)
    and process substitution `<(`.
  - `segment.IsWrapper` (`segment.go:418-433`) and triage's `neverPropose`
    (`families.go:52-107`) already treat `env`, `nohup`, `timeout` and
    friends as wrappers, so three lists disagree about the same commands.
  - Upstream Crush has the same `safe.go`.
- **Cost today:** a configured deny rule such as `bash: "rm *"` doesn't
  fire for `timeout 5 rm -rf x`, and nothing tells the user it was skipped.
- **Deletion test:** deleting the safe list sends every command through the
  permission service, where config allow rules and the session auto-approve
  already cover read-only commands. Callers lose only the fast path.
- **Direction:** decide "safe" from `segment.Split` segments (every segment
  safe, no redirects, no wrappers) instead of a string prefix, or drop the
  list and let config rules carry it. This changes behaviour, so it is a
  `fix:` with tests for each bypass above.

### 2. Mode and threshold knowledge is restated across packages (Strong)

**Status (2026-10-12):** Open.

- **Flags:** Duplicated concept, Hand-synced state
- **Evidence:**
  - Bouncer mode exists as `config.BouncerMode` (`config/bouncer.go:27`)
    and `permission.BouncerMode` (`permission/decision.go:152`), converted
    in `app/bouncer.go:79`. Review mode the same
    (`config/bouncer.go:62`, `permission/review.go:57`).
  - Mode cycling is written twice: `ui/model/ui.go` `cycleBouncerMode` and
    `ui/dialog/commands.go` `nextBouncerMode`.
  - Thresholds: ranges and defaults in `config.Bouncer.Validate`
    (`config/bouncer.go:118-181`), `bouncer.Thresholds.Validate`
    (`route.go:57-108`), `DefaultThresholds` (`route.go:38`), the jsonschema
    `default=` tags, and the overlay in `app/bouncer.go:188`.
    `maxSeverity` is defined in both `decision.go` and `route.go`.
  - Hazard axes in `bouncer.HazardQuestions` and `config.BouncerHazardAxes`,
    kept in step only by a test (`app/bouncer_test.go:337`).
- **Cost today:** changing a threshold or adding a mode means about five
  edits, and tests catch only some of the drift.
- **Deletion test:** deleting the `config` copies would leave `config`
  unable to validate, because `config` can't import `bouncer`
  (`bouncer` → `permission` → `config`). That import direction is the root
  cause.
- **Direction:** move the shared definitions (modes, axes, threshold
  ranges) into one leaf package both sides import, and derive the rest.

### 3. Permission policy lives in the UI (Worth exploring)

**Status (2026-10-12):** Open.

- **Flags:** Feature envy, Information leakage
- **Evidence:** `ui/model/ui.go` (the `PermissionGrant*` response handling)
  decides what "allow for session" and "allow forever" mean, including what
  to do when persisting fails. `splitPatterns` in `ui.go` splits on
  `" && "`, the inverse of the join in `ui/dialog/permissions.go:284`.
  `dialog/permissions.go:520` re-derives "the bouncer denied this" from
  `Outcome == "deny" && !Shadow`, which the service already computes as
  `bouncerDenied`.
- **Cost today:** the pattern format is hand-synced across two files, and
  the grant rules can't be tested without the TUI.
- **Deletion test:** a single service method that takes the user's choice
  and the patterns would remove the UI's branch and the split/join pair.
- **Direction:** give the service one "respond" entry point that owns the
  grant scope and pattern format.

### 4. Wide `permission.Service` with hand-written fakes (Worth exploring)

**Status (2026-10-12):** Open.

- **Flags:** Mocking your own code, Shotgun surgery
- **Evidence:** about 17 methods (`permission.go:119-162`), one real
  implementation, and at least seven hand-written fakes
  (`tools/bash_test.go`, `view_test.go`, `multiedit_test.go`,
  `agent/hooked_tool_skill_test.go`, `app/bouncer_test.go`,
  `workspace/app_workspace_test.go`). Tools only call `Request`.
- **Cost today:** adding a method to the service means editing every fake.
- **Deletion test:** a one-method interface type declared in
  `agent/tools` (the consumer) would let the fakes shrink to one method.
- **Direction:** narrow the interface type tools depend on to `Request`.

### 5. Legacy exact-match session grants (Strong, small)

**Status (2026-10-12):** Done in #97.

- **Flags:** Two ways to do one task
- **Evidence:** `GrantPersistent` and the `sessionPermissions` map
  (`permission.go:183, 217, 523`) duplicate `GrantSession` rules. The only
  production caller is the pass-through
  `AppWorkspace.PermissionGrantPersistent` (`app_workspace.go:246`), which
  nothing calls. Its comment cites `backend.GrantPermission`, which no
  longer exists.
- **Cost today:** every policy evaluation checks a map nothing writes, and
  every fake carries the method.
- **Deletion test:** nothing is lost.
- **Direction:** delete the method, the map, the key type and the workspace
  pass-through.

### 6. Hook allow beats a config deny rule (Speculative)

**Status (2026-10-12):** Open.

- **Flags:** Information leakage
- **Evidence:** `permission.go:288` honours `hookApproved` before
  `evaluatePolicy`, so a PreToolUse hook "allow" overrides a configured
  deny and skips the bouncer. `docs/hooks/README.md` documents that hooks
  skip the prompt, but not that they beat deny rules, and no test pins it.
- **Cost today:** the precedence is implicit. It may well be intended,
  since hooks are user-authored.
- **Direction:** decide the precedence, then document and test it.

## Rules Worth Enforcing

- A test that every command in `safeCommands` (or its replacement) yields
  only safe segments from `segment.Split`, so the two can't drift.
- A test that every `permission.BouncerMode` has a `config.BouncerMode`
  twin, until candidate 2 removes the duplication.

## Not Findings

- `permission.Bouncer`, `Reviewer` and `IntentSource` are interface types
  defined in the consumer to avoid an import cycle; `bouncer` is the
  vendor-adapter boundary.
- Hooks running before permission checks is documented in `AGENTS.md` and
  `docs/hooks/README.md`.
- Bouncer and yolo settings come only from user-level config by design, so
  project config can't loosen them.
- The allow cache is swapped rather than cleared so in-flight assessments
  can't write into the new one (`permission.go:196`).
- The lock ordering in `permission.go:172-176` is documented.
