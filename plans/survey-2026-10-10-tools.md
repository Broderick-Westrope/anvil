# Architecture Survey: Tools, Shell, Jobs and Messages

**Scope:** `internal/agent/tools` (including `mcp/`), `internal/shell`,
`internal/jobstore`, `internal/jobevents`, `internal/message`. The
complexity of the tool constructors and message functions is handled by
#88 and #85; findings they already fixed are listed at the end.
**Method:** six-month churn and size, one explorer, then every candidate
below re-read at the cited lines. 2026-10-10.

Churn (six months, non-test): `tools/mcp/init.go` 23, `tools/view.go` 19,
`tools/tools.go` 16, `tools/bash.go` 15, `multiedit.go` 12, `edit.go` 11,
`shell/background.go` 10, `message/content.go` 10. `shell/background.go`
is 1,282 lines. Jobs (`shell/background.go`, `jobstore`, `jobevents`) are
the user's code; most file tools and `message` are upstream.

## Top Recommendation

**Candidate 1, give job status wording one owner.** The job system is the
user's own code, it is still changing, and the same `EndReason` decisions
are restated in at least four places, one of which (the TUI) recovers them
by regex from prose. Fixing it is local to code with no upstream sync
cost.

## Candidates

### 1. Job end-state knowledge is restated and re-parsed (Strong)

**Status (2026-10-12):** Open.

- **Flags:** Growing switch, Duplicated concept, Information leakage
- **Evidence:**
  - The five `End*` reasons (`shell/background.go:424-430`) are switched on
    in `shell.JobOutcome` (`jobformat.go:14`), `FormatArchivedJobStatus`,
    `killArchivedJob` and `archivedListStatus` (`tools/job_archive.go`),
    and `agent/job_handoff.go:44-46`.
  - The "completed, exit N" branch repeats at `jobevents/format.go:26`,
    `tools/job_format.go:71` and `agent/job_handoff.go:59`.
  - `jobevents.FormatNotice` writes prose (`format.go:18-52`), and
    `ui/chat/job_event.go:108-109` parses it back with two regexes. A
    wording change silently drops the TUI to its fallback branch.
  - The "heartbeat within `InstanceLiveWindow`" rule is inlined three times
    (`jobstore/store.go:172`, `jobevents/persist.go:250`,
    `app/jobs.go:140`).
  - DB row → `shell.JobInfo` is built by hand in both
    `jobstore.recordFromRow` and `jobevents/persist.go:269-283`.
  - `jobstore.Record` stores `EndReason` twice (`Record.EndReason` and
    `Record.Info.EndReason`), set together in `applyLiveness`.
- **Cost today:** adding an end reason or rewording a notice means editing
  four or more places, and the TUI breaks silently if one is missed.
- **Deletion test:** the copies vanish if one function owns
  "reason → wording" and events carry structured fields the TUI renders
  directly.
- **Direction:** one owner for end-state wording and liveness; have job
  events carry structure to the TUI instead of prose.

### 2. The tool preamble is hand-rolled with drift (Strong)

**Status (2026-10-12):** Partly done. #88 added `requestPermission` (used by bash, fetch, download and view) and #92 fixed the missing `ToolCallID`s. The session guard, tracked writes and the remaining permission call sites are still hand-rolled.

- **Flags:** Duplicated concept, Two ways to do one task, Data clumps
- **Evidence:**
  - Session guard: about 12 hand-rolled copies with different messages;
    `fetch.go`, `mcp-tools.go`, `edit.go` and `multiedit.go` all say
    "...for creating a new file".
  - Permission request and denial tail: 16 call sites. #88 adds
    `requestPermission` and uses it in bash, fetch, download and view;
    edit, multiedit, ls, read/list MCP resources and `mcp-tools.go` still
    hand-roll it. download and lsp_rename omit `ToolCallID`, so
    `hookApproved` (`permission.go:35`) never pre-approves them.
  - Write then record in the file tracker: `commitFileChange`
    (`edit.go:229`) exists, but `edit.go`, `multiedit.go` and `write.go`
    also hand-roll it, `lsp_rename.go` records without a hash and
    `download.go` records nothing.
  - Path handling: `SmartJoin` in some tools, raw paths in grep, glob and
    the LSP tools; only `ls` asks permission for paths outside the working
    directory among the read-only tools.
  - `(lspManager, permissions, filetracker, workingDir)` travels through
    five constructors.
- **Cost today:** a policy fix (for example, the `ToolCallID` bug or the
  `..foo` check) needs editing in many files, and the outliers already
  behave differently.
- **Direction:** one shared tool context carrying the clump, with the
  session guard, permission step and tracked write as its methods. Most of
  these files are upstream, so sequence it after the upstream sync.

### 3. Six copies of the HTTP client setup (Worth exploring)

**Status (2026-10-12):** Open.

- **Flags:** Duplicated concept
- **Evidence:** the same transport clone in `fetch.go`, `download.go`,
  `sourcegraph.go`, `web_search.go`, `web_fetch.go` and
  `agent/agentic_fetch_tool.go`; the `"anvil/1.0"` user agent in four
  places; the http(s) URL check twice.
- **Direction:** one constructor taking the timeout.

### 4. `skillContentRe` is defined twice and has drifted (Strong, small)

**Status (2026-10-12):** Done in #94.

- **Flags:** Information leakage
- **Evidence:** `skills/format.go:10` captures the body and is anchored by
  `ParseContentXML`; `ui/chat/user.go:165` doesn't capture and matches
  anywhere. `commands.CollapseExpansionXML` already shows the right shape:
  the UI calls the owning package.
- **Direction:** the UI calls a `skills` function; delete its regex.

### 5. The message part registry is four hand-synced lists (Worth exploring)

**Status (2026-10-12):** Partly done. #85 made `unmarshalParts` a table; `marshalParts` is still a switch.

- **Flags:** Hand-synced state, Growing switch
- **Evidence:** 13 `partType` constants (`message.go`), a 13-case
  `marshalParts` switch, the decoder table #85 adds for `unmarshalParts`,
  and 13 `isPart` methods in `content.go`. A new part type needs four
  coordinated edits.
- **Direction:** one registry entry per part type that both directions
  read. `message.go` is upstream, so weigh sync cost.

### 6. `shell/background.go` mixes six concerns (Speculative)

**Status (2026-10-12):** Open.

- **Flags:** God module, Hand-synced state
- **Evidence:** 1,282 lines covering output buffering, job state and
  watches, ID allocation, recorder admission, the manager's shell map and
  aliases, publish/finalize and shutdown. Shutdown is three flags
  (`shuttingDown`, `eventsClosed`, `recClosed`) set together. The job ID
  format is owned by `jobstore.FormatID`/`ParseID` but re-implemented as
  `"%03X"` in `counterAllocator`. The manager is a global singleton
  reached directly from eleven sites, so tool tests share process state.
- **Direction:** split by reason to change after candidate 1, and pass the
  manager in rather than reaching the global.

## Rules Worth Enforcing

- A test that every `shell.End*` reason has wording from the single owner
  (after candidate 1).
- A test that the TUI renders every `jobevents` kind without hitting its
  fallback branch.
- Lint or test: every `CreatePermissionRequest` sets `ToolCallID`.

## Not Findings

- The ripgrep plus Go-regex fallback in grep is deliberate.
- `EventSink`, `IDAllocator` and `JobRecorder` are consumer-defined
  interface types in `shell` with real second implementations; the
  `jobstore` → `shell` and `jobevents` → `jobstore` directions are right.
- `message.Service` debounce semantics and fallback job IDs are documented.
- Internal test packages throughout `tools`, `shell` and `message` are the
  repo's pattern.

### Already fixed by #88 and #85

- bash's explicit and auto-background paths were two drifting copies.
- view and write each hand-rolled the outside-working-dir check.
- job_output and job_kill each looked up archived jobs.
- `unmarshalParts` was a 13-case switch.
- Message deltas stopped debouncing after the first structural flush.
