# Phase 1: Name Mode, Resolver, Hook Integrity, and Minimal Rendering

> **Status:** IN_PROGRESS (tasks 1-3 authorized for implementation and
> incremental commits; tasks 4-5 not yet authorized; no pushes, PRs, merges,
> or worktree cleanup authorized)
> **Depends on:** nothing. Base commit `f549a2ca`, branch `feat/skill-name-loading`.
> **Delivers:** `view(skill_name=...)` working end to end, including the minimal TUI/copy rendering it needs, because the selector becomes model-visible the moment this phase merges.

## Specification

**Problem.** `view` can only be addressed by path. Skill loading therefore depends on the prompt carrying an absolute `<location>` for every catalog entry, and an agent with a narrow catalog has no legal way to load a skill it was told to use by name. There is also no way to read a skill without the path-based plumbing that the model has to copy correctly.

**Goal.** `view` takes exactly one of `file_path` or `skill_name`. Name mode resolves the exact skill name against the enabled registry snapshot held by the tool instance that executes the call, rereads the selected source, and returns the whole body plus the canonical location. Nothing about path mode changes, nothing about the permission model changes, and PreToolUse hooks that gate on `file_path` see the real target before any bytes are read — including the target after a hook rewrite.

**Scope.**

In scope:

- `internal/skills/lookup.go` (new resolver) and its tests.
- `internal/agent/tools/view.go`: the `skill_name` selector, the bounded
  full-body reader, the hook-target interface implementation.
- `internal/agent/tools/view_open_unix.go` and
  `internal/agent/tools/view_open_windows.go` (new, build-tagged): the
  non-blocking open helper used by name mode.
- `internal/agent/tools/tools.go`: one context key pair for the pre-hook
  baseline.
- `internal/agent/hooked_tool.go`: target preparation, path-to-name refusal,
  and final-target authorization.
- Every `NewViewTool` call site, in the same task as the signature change.
  There are four invocations plus the declaration:
  `internal/agent/coordinator.go:1021`,
  `internal/agent/agentic_fetch_tool.go:175`,
  `internal/agent/common_test.go:161`,
  `internal/agent/tools/view_test.go:268`, declared at
  `internal/agent/tools/view.go:90`.
- Minimal presentation so the newly advertised selector never renders blank: `internal/ui/chat/file.go` (`ViewToolRenderContext.RenderTool`), one pending helper in `internal/ui/chat/tools.go`, and the `view` case of `formatParametersForCopy` in `internal/ui/chat/tools.go`.
- `docs/hooks/README.md`: document the name-mode target injection and the single re-gate, since this phase changes observable hook behavior.
- Offline VCR cassette request-body repair for the `view` schema change.

Out of scope: prompt templates, `view.md.tpl`, catalog XML, `README.md`, `.agents/skills/builtin-skills/SKILL.md`, token measurement (phase 3); rendering breadth and `anvil sessions show` assurance (phase 2); task-scoped skill APIs, a `load_skill` tool, tracker attribution fixes, reload-cache redesign.

**Success Criteria.**

- [ ] `view` schema exposes `file_path`, `skill_name`, `offset`, `limit`, with `required` empty, and the tool rejects zero selectors and (absent a hook baseline) two selectors at run time.
- [ ] `view(skill_name=X)` returns the full file for both disk-backed and builtin winners, with no line-length truncation, no line numbers, and no "File has more lines" notice.
- [ ] `offset` or `limit` non-zero with `skill_name` is a bounded error.
- [ ] Unknown or globally disabled names return "not available in this registry snapshot", with no directory listing, no suggestions, and no discovery.
- [ ] A skill enabled globally but absent from the calling agent's prompt catalog still loads by exact name.
- [ ] Source precedence winner is used as-is: user skill over builtin, builtin location returned verbatim as its `anvil://skills/<name>/SKILL.md` URI, disk location returned as an absolute path with symlinks unresolved, relative registry paths absolutized against the process working directory (which is what discovery walked), never against the tool's `workingDir`.
- [ ] `Located.BaseDir()` returns `anvil://skills/jq` for `anvil://skills/jq/SKILL.md` (the `anvil://` double slash survives) and `filepath.Dir` for disk skills.
- [ ] Non-regular sources (FIFO, device, socket, directory) are rejected
      without their content being read, and the open cannot block: name mode
      opens through the platform helper (Unix `O_NONBLOCK`) and judges the
      resulting descriptor, so there is no stat/open window to lose.
      Oversized sources fail with a bounded error after reading at most
      `MaxSkillLoadSize + 1` bytes; a file exactly at the cap succeeds.
- [ ] Deleted, unreadable, malformed-frontmatter, metadata-invalid, oversized, non-regular, and renamed sources each produce a distinct bounded error with no fallback to a losing source.
- [ ] Outside-working-directory permission requests and symlink-aware
      skills-path membership behave exactly as in path mode, and
      authorization happens before the resolved target is opened.
- [ ] Hook payloads for a `skill_name` call carry the resolved `file_path`. Deny, halt, allow, `context`, and rewrites are honored, and a rewrite that changes the final target discards the pass-1 approval and runs exactly one additional bounded hook gate on the final canonical payload.
- [ ] A second retarget (a rewrite during the second gate) is a bounded
      error; there is no third pass and no loop.
- [ ] A PreToolUse hook cannot convert a path-mode call into a name-mode
      call: a rewrite that introduces `skill_name` on a call that arrived in
      path mode (or with no selector) is a bounded error, enforced both in
      `hookedTool.Run` and in `view` via the baseline mode.
- [ ] Clearing only `skill_name` in a rewrite yields path mode at the same
      canonical target (because `updated_input` shallow-merges and the
      injected `file_path` survives), with no second gate; clearing both
      selectors yields the zero-selector error.
- [ ] Ordinary `file_path` calls, and every non-`view` tool, keep today's single-pass hook semantics byte for byte.
- [ ] Sub-agents (which are never hook-wrapped) load by name with no baseline in context and strict exactly-one-selector validation.
- [ ] Repeated loads of the same skill always return content, including after the file changes on disk, both from the same tool instance with the skill already marked loaded and from a freshly built tool instance.
- [ ] A pending name-mode call renders the requested skill name when the streamed input JSON is already parseable, falls back to today's anonymous pending row when it is not, and path-mode pending output is unchanged.
- [ ] `task test` and `task lint` pass with no live provider calls.

## Context Loading

_Run before starting:_

```bash
view internal/agent/tools/view.go
view internal/agent/tools/view_test.go
view internal/skills/skills.go
view internal/skills/embed.go
view internal/skills/tracker.go
view internal/agent/hooked_tool.go
view internal/hooks/input.go
view internal/hooks/hooks.go
view internal/hooks/runner.go
view internal/ui/chat/file.go
view internal/ui/AGENTS.md
grep -n "NewViewTool" internal/
```

Key facts established during planning, verified at `f549a2ca`, so the executing agent does not have to rediscover them:

| Fact | Evidence |
|---|---|
| `file_path` is currently schema-required because it has no `omitempty`. Fantasy marks a field optional only when the json tag contains `omitempty`. Adding `skill_name` therefore changes the advertised schema as soon as this phase merges; there is no "unadvertised" window. | `charm.land/fantasy@v0.43.2/schema/schema.go:137-170` |
| `ViewPermissionsParams(params)` is a direct struct conversion, so any field added to `ViewParams` must be mirrored in the same order. | `internal/agent/tools/view.go:145` |
| Builtin skills already carry `anvil://skills/<name>/SKILL.md` in `SkillFilePath`. Never synthesize it. `BuiltinPrefix` is `anvil://skills/`. | `internal/skills/embed.go:12` |
| `path.Dir("anvil://skills/jq/SKILL.md")` returns `anvil:/skills/jq` because `path.Clean` collapses the double slash. Directory math on builtin URIs must split the prefix off first. | verified with `path.Dir` |
| Disk skill paths are whatever discovery walked, and discovery walks the configured `skills_paths` from the process working directory, so a relative configured path yields a relative `SkillFilePath`. | `internal/skills/skills.go:260-300`, `internal/skills/skills.go:195-196` |
| Registry precedence is already resolved: `Deduplicate` keeps the last occurrence (user over builtin), then `Filter` drops disabled names. The winner is unique by `Name`. | `internal/agent/coordinator.go` `discoverSkills`, `internal/skills/skills.go:381-394,443-460` |
| `skills.ParseContent` does not validate. `Skill.Validate` is separate, and its directory-name check is skipped when `Path` is empty. | `internal/skills/skills.go:153-216` |
| `Tracker.MarkLoaded` is nil-safe and only records names in the active set. It never gates content. | `internal/skills/tracker.go:34-46` |
| Hooks run on raw tool input before the tool executes, and `BuildEnv` exports `ANVIL_TOOL_INPUT_FILE_PATH` from the `file_path` key. | `internal/agent/hooked_tool.go:54-99`, `internal/hooks/input.go:53-72` |
| All matching hooks run in parallel against **one** payload built from the input as passed to the wrapper (the prepared input, for a name-mode `view` call), and the aggregate merges a possibly-rewritten input with a decision computed from the original target. | `internal/hooks/runner.go:106-121`, `internal/hooks/hooks.go:94-157` |
| `updated_input` patches are shallow-merged top-level keys over that payload: keys the patch omits survive, so clearing one selector does not clear an injected one. | `internal/hooks/hooks.go:128-140`, `internal/hooks/hooks.go:159-188` |
| A hook `allow` becomes `permission.WithHookApproval(ctx, call.ID)`, which is keyed by tool call ID only, not by target, so it applies to whatever the tool ends up reading. | `internal/agent/hooked_tool.go:79-84`, `internal/permission/permission.go:20-40` |
| Sub-agents are intentionally unwrapped, so they never fire hooks. | `internal/agent/hooked_tool.go:27-40` |
| `PrepareStep` re-reads `a.tools.Copy()` on every step, and `ReloadPlugins` replaces tool instances via `SetTools`, so a single `Run` can execute different `view` instances. | `internal/agent/agent.go:391-399`, `internal/agent/coordinator.go:1727-1747` |
| The chat renderer returns the anonymous pending row before parsing params, so today no `view` call shows anything while pending. | `internal/ui/chat/file.go:40-42` |
| `toolParamList` treats `params[0]` as the main parameter and pairs every following element as `key`,`value`. A two-element slice `{"skill", name}` would render only the literal `skill`. | `internal/ui/chat/tools.go:656-690` |
| There are 13 VCR cassettes under `internal/agent/testdata/TestOrchestratorAgent/claude/`, holding 49 interactions between them (2 to 6 each), and the matcher compares whole request bodies. Every request body carries the full tool schema; not every interaction carries the catalog block. | `find internal/agent/testdata -name '*.yaml'`, `charm.land/x/vcr@v0.1.1/matcher.go:17-55` |

## Design Decisions

### Selector semantics, and how the public contract survives the hook payload

The public contract is: exactly one of `file_path` or `skill_name`. The
two-key payload that hooks see is an internal enrichment, legal only for a
call whose context carries a preparation baseline written by `hookedTool`.

The baseline records the **pre-hook selector mode** as well as the resolved
target. It has to, because a hook rewrite can change the mode, and the tool
must be able to distinguish an original name-mode call from a path-mode call
that a hook converted into one.

| Shape | Baseline in context | Meaning |
|---|---|---|
| `file_path` only | absent, or `Mode: "path"` | Path mode. Byte-for-byte the current behavior. |
| `skill_name` only | absent | Name mode. What the model sends, and what a sub-agent sees, since sub-agents are not hook-wrapped. |
| `skill_name` only | `Mode: "name"` | Name mode; hooks ran and did not retarget. |
| `skill_name` + `file_path` | `Mode: "name"` | The hook-prepared or hook-rewritten form. Interpreted through the canonical-target rules below. |
| `skill_name` set (with or without `file_path`) | `Mode: "path"` or `Mode: "none"` | Rejected with `ErrPathToNameRewrite`. A hook may not convert a path read into a skill load in v1; see "Path-to-name rewrites are refused" below. |
| `skill_name` + `file_path` | absent | Rejected: "pass exactly one of file_path or skill_name". |
| neither, or both empty/`null` | any | Rejected: "pass exactly one of file_path or skill_name". JSON `null` unmarshals to `""`, so `{"skill_name": null}` is the zero-selector case, not a name lookup. |

A name-mode call that fires no hooks at all — no matching PreToolUse hook,
or an unwrapped sub-agent tool — carries no baseline and is untouched by
everything below: it is validated as strict exactly-one-selector and read.
That is the common public case and this phase must not regress it.

### Why hooks need a preparation step

`hookedTool.Run` fires hooks against the raw JSON input. A `skill_name` call
has no `file_path`, so `hooks/input.go` exports no
`ANVIL_TOOL_INPUT_FILE_PATH` and a user hook that gates reads by path is
silently bypassed. Preparation resolves the name to its concrete file before
hooks run and before any content is read, then writes that path into the
hook payload.

Preparation also runs for path-mode and zero-selector `view` calls, but only
to stash the baseline mode in the context. It makes **no** change to the
payload in those cases, so path-mode hook input, env vars, and pass count
stay byte-for-byte what they are today.

| Mechanism | Pros | Cons | Chosen |
|---|---|---|---|
| Narrow optional interface on the tool, plus a baseline in context | Baseline is invisible to the model and to hooks, so rewrite attribution is exact; no extra public JSON field; nothing to validate in the schema | Adds one interface and one context key pair; the ctx value is implicit coupling between wrapper and tool | **Yes** |
| Carry the pre-hook resolution in a third JSON field | Fully self-describing payload, no context coupling | A model-visible or hook-visible field that is not part of the public API; a hook could rewrite it and create a third ambiguous state | No |

The interface stays deliberately narrow: two methods, one implementer, documented as such. It is not a general "tool preparation pipeline".

### Final-target authorization (the hook gate contract)

Preparation alone does not deliver the gating promise, because of three
verified facts: all hooks run in parallel over one payload built from the
(prepared) input; the aggregate can carry `allow` from one hook and
`updated_input` from another; and the resulting approval is keyed only by
`call.ID`. Without extra work, hook A approving skill A would silently
authorize the read of skill B that hook B retargeted to, and a
destination-deny hook would never run at all.

**Canonical target.** For a baseline-carrying call, the tool can report the
concrete thing it would read, using the same registry snapshot preparation
used:

```go
type HookTarget struct {
	Mode     string // "name" | "path" | "none"
	Name     string // requested skill name, name mode only
	Location string // absolute path, or verbatim anvil:// URI; "" when unresolved
	Resolved bool
}
```

Two targets are compared by **destination**, not by selector shape. They
have the same destination when both are resolved and their normalized
`Location` values are equal: disk locations normalized with `filepath.Abs` +
`filepath.Clean`, builtin URIs compared verbatim. Anything else (including
one resolved and one not) is a different destination.

Destination is the right comparison key because it is what the pass-1 hooks
were shown. If the bytes about to be read still come from the exact path the
hooks saw in `ANVIL_TOOL_INPUT_FILE_PATH`, the approval they granted is the
approval for those bytes, so a change of `Mode` alone earns no second gate.
It only changes how the tool reads and annotates the file.

**`updated_input` is a shallow merge, and that shapes several rows below.**
`aggregate` merges each hook patch over the *prepared* input, top-level keys
only (`internal/hooks/hooks.go:94-157`, with `shallowMerge` at
`internal/hooks/hooks.go:159-188`). Keys the patch omits are preserved.
So a patch of `{"skill_name":""}` against a prepared name-mode payload leaves
the injected `file_path` in place: the result is a path-mode call at the same
canonical target, **not** a zero-selector call. Reaching zero selectors takes
a patch that clears both keys.

Resolution rules for a call whose baseline has `Mode: "name"`, given
`baseline = {Name, Location, Resolved}`:

| `skill_name` vs baseline | `file_path` vs baseline `Location` | Resulting target |
|---|---|---|
| same | same (or absent) | Name mode on the baseline skill. Same destination. |
| same | cleared (`""`) | Name mode on the baseline skill; the tool re-resolves the name, so the destination is unchanged. |
| changed | unchanged | Name mode on the new name; the stale injected path is ignored. |
| unchanged | changed | Path mode on the hook's `file_path`. Name mode is abandoned, so no skill metadata is attached unless the rewritten path is itself inside a skills path, which path mode already handles. |
| changed | changed, and equal to the canonical location of the new name | Name mode on the new name. The hook rewrote both selectors consistently, which is not ambiguous. |
| changed | changed, and *not* equal to the canonical location of the new name | `ErrAmbiguousRewrite`. Bounded error, nothing is read. |
| cleared | unchanged (the injected location survives the merge) | Path mode at the same canonical target. Same destination, so no second gate and the pass-1 approval stands; the read is an ordinary path read with path-mode line handling and no name-mode skill metadata. |
| cleared | changed | Path mode on the new path. Different destination, so the name-mode re-gate rule below applies. |
| cleared | cleared (patch clears both keys) | `Mode: "none"`. Nothing can be read; the tool returns the zero-selector error, with no second gate and no approval. |

Resolution rules for a call whose baseline has `Mode: "path"` or
`Mode: "none"`:

| Post-hook input | Resulting target |
|---|---|
| `file_path` only, rewritten or not | Path mode. Today's semantics exactly: one hook pass, no canonical-target machinery, no second gate. |
| `skill_name` non-empty | `ErrPathToNameRewrite`. Bounded error, nothing read, no approval, no second gate. |
| neither selector | Zero-selector error, exactly as today. |

**Path-to-name rewrites are refused in v1.** Preparation resolves and
injects only for calls that arrive in name mode, so a path-mode call a hook
rewrites to `{"file_path":"","skill_name":"private"}` would enter name mode
with the gate having run against the original path and nothing having run
against the skill. Supporting it properly would mean resolving the rewritten
name and driving the name-mode re-gate from a path-mode origin, widening the
state machine for a rewrite shape nobody has asked for. The simplest bounded
choice is refusal: the response is `PreToolUse hooks may not convert a
file_path read into a skill_name load` and nothing is read. Ordinary
path-to-path rewrites keep today's semantics untouched, and public name-mode
calls with no hooks configured are unaffected because no baseline exists and
no rewrite happened.

Because a path-mode call has no resolved baseline to compare against, this
rule needs enforcement in **two** places, and both are required:

1. `hookedTool.Run` compares the pre-hook mode `M0` with the post-hook input
   and returns the bounded error before the inner tool runs. This is the
   enforcement point, and it exists precisely for the case where the initial
   call was path mode and therefore has no name baseline.
2. `view` rejects a non-empty `skill_name` whenever the context baseline has
   `Mode: "path"` or `Mode: "none"`, instead of treating it as a fresh name
   call. Without this the tool cannot distinguish a hook-introduced name from
   a model-issued one, since a legitimate sub-agent name call also carries no
   name baseline. The wrapper check is the gate; this is the check that makes
   the tool safe on its own terms.

**Authorization algorithm** in `hookedTool.Run`. The canonical-target path
runs only when the inner tool implements the interface. Every other tool
keeps today's single-pass path exactly:

1. Prepare. Record the pre-hook mode `M0`. When `M0 == "name"`, `T0` is the
   canonical target hooks will be gated on and the resolved path is injected
   into the payload; if resolution failed, the baseline is still stashed with
   `Resolved: false` and the input is left untouched, so the unresolved case
   is distinguishable from "no preparation happened". When `M0 != "name"` the
   payload is untouched and only the mode is stashed.
2. Run hook pass 1 (unchanged code path). Honor `deny`/`halt` immediately, as
   today.
3. Apply pass-1 `UpdatedInput` through the existing shallow merge.
4. If `M0 != "name"` and the merged input carries a non-empty `skill_name`:
   return the bounded `ErrPathToNameRewrite` response. No approval, no second
   gate, nothing read.
5. If `M0 != "name"` otherwise: today's behavior, unchanged. Attach the
   pass-1 approval when the decision was `allow`, run the tool once, done.
6. Ask the tool for `T1 = CanonicalTarget(input1)`.
7. If `CanonicalTarget` returned `ErrAmbiguousRewrite`: bounded error
   response, no read, no approval, no second gate.
8. If `T1.Mode == "none"`, or `T1` is a name that does not resolve: no read
   can happen. Do **not** attach an approval, do **not** run a second gate,
   and let the tool return its own bounded error.
9. If `T1` has the same destination as `T0` and `T0.Resolved`: nothing was
   retargeted, even if the selector shape changed. Attach the pass-1 approval
   when the decision was `allow`. Run the tool once. Done.
10. Otherwise the final destination differs from the gated one (a genuine
    retarget, or an initially-unresolved name that now resolves). Then:
    a. Discard the pass-1 approval. It belonged to `T0`.
    b. Run **exactly one** additional hook pass over the final canonical
       payload (`input1` with `file_path` normalized to `T1.Location`),
       behind a local re-entrancy flag so no third pass is possible.
    c. Honor `deny`/`halt` from pass 2.
    d. Compute `T2` from pass 2's `UpdatedInput`. If `T2`'s destination
       differs from `T1`'s, return the bounded error `PreToolUse hooks
       retargeted this call twice; a rewritten target may be rewritten once`
       and read nothing. A pass-2 rewrite that only changes selector shape
       without moving the destination is not a retarget.
    e. Attach an approval only if pass 2 decided `allow`, still keyed by
       `call.ID`, now earned against the final target.
    f. Merge both passes into the response: `context` strings concatenated in
       pass order, `hook` metadata reporting the summed hook count, both
       reasons joined, pass 2 as the authoritative decision, and a `retarget`
       marker so the UI and logs can tell this happened.

At most two hook passes run per call. User hooks may therefore execute twice
for a retargeted name-mode call; that is documented in `docs/hooks/README.md`
as part of this phase, with the requirement that PreToolUse hooks be
idempotent.

**Worked examples**, each of which becomes a test in task 3:

| # | Setup | Old behavior | Required behavior |
|---|---|---|---|
| A | `view(skill_name="euc-go")`. `guard.sh` allows any `ANVIL_TOOL_INPUT_FILE_PATH` under the configured skills path. `retarget.sh` patches `{"skill_name":"secret-notes"}`, a registry entry living in `/tmp/private` (outside the workdir and every skills path). `deny-private.sh` denies paths under `/tmp/private`. | Pass 1: guard allows (it sees the euc-go path), retarget rewrites, approval keyed to the call ID short-circuits the permission prompt, `/tmp/private/secret-notes/SKILL.md` is read. `deny-private.sh` never sees it. | Approval discarded; pass 2 payload carries `/tmp/private/secret-notes/SKILL.md`; `deny-private.sh` denies; response is the hook-blocked error; no content, no `filetracker` record, no permission grant. |
| B | `view(skill_name="euc-gogo")` (typo). Pass-1 hook patches `{"skill_name":"euc-go"}` and returns `allow`. | Preparation fails, no baseline, hooks see no path, the rewritten name is loaded with an approval nobody earned. | Baseline recorded as unresolved; `T1` resolves and differs; approval discarded; one pass 2 on the euc-go path; load proceeds only if pass 2 allows or stays silent (silence falls through to the normal permission flow). |
| C | Pass-1 hook patches `{"skill_name":"jq","file_path":"anvil://skills/jq/SKILL.md"}`. | Both fields changed; earlier draft errored out. | Consistent rewrite: name mode on `jq`, one pass 2, no ambiguity error. |
| D | Pass-1 hook patches `{"skill_name":"jq","file_path":"/etc/passwd"}`. | Ambiguous; earlier draft errored (correct) but without the approval rule. | `ErrAmbiguousRewrite`: bounded error, nothing read, no approval, no pass 2. |
| E | Pass-1 hook retargets to skill B; a pass-2 hook patches `file_path` again. | Not modeled. | Bounded double-retarget error; exactly two passes recorded; nothing read. |
| F | `view(file_path="README.md")` with an `allow` hook and a rewrite hook configured. | One pass, approval by call ID, rewrite honored. | Unchanged: exactly one pass, no name baseline, no canonical-target machinery, no second gate. Regression-locked. |
| G | Pass-1 hook patches `{"skill_name":""}` and nothing else, against a prepared payload that already carries the injected `file_path`. | Undefined. | The shallow merge preserves `file_path`, so this is **path mode at the same canonical target**, not a zero-selector call: same destination, so no pass 2 and the pass-1 approval stands; the file is read with path-mode semantics and without name-mode skill metadata. |
| G2 | Pass-1 hook patches `{"skill_name":"","file_path":""}` (both keys cleared). | Undefined. | `Mode: "none"`; no pass 2; no approval; tool returns the zero-selector error. |
| G3 | Pass-1 hook patches `{"skill_name":"","file_path":"/etc/passwd"}`. | Undefined. | Path mode on a changed destination, so the name-mode re-gate applies: pass-1 approval discarded, exactly one pass 2 on `/etc/passwd`, deny there blocks the read. |
| H | `view(skill_name="euc-go")` where the resolved location is inside a configured skills path; a pass-1 hook retargets to another skill also inside a skills path. | Approval carried over needlessly. | Pass 2 runs on the new path; permission is still skipped because skills-path membership exempts it; a deny hook still blocks. Exempt directories change the permission prompt, never the gate. |
| I | `view(file_path="README.md")`; a pass-1 hook patches `{"file_path":"","skill_name":"secret-notes"}`. | Name mode would be entered with no name baseline and no gate on the skill. | `ErrPathToNameRewrite`: bounded error, nothing read, no approval, no pass 2. Asserted at both enforcement points — through `hookedTool.Run`, and by calling the tool directly with a `Mode: "path"` baseline in context. |
| J | `view(file_path="README.md")`; a pass-1 hook patches `{"file_path":"docs/other.md"}`. | One pass, rewrite honored. | Unchanged path-to-path semantics: exactly one pass, no `ErrPathToNameRewrite`, no second gate. |
| K | `view(skill_name="euc-go")` with no PreToolUse hooks configured at all, and separately through an unwrapped sub-agent tool. | Works. | Unchanged: no baseline, strict exactly-one-selector validation, one read, no gate machinery. |

### Full read, bounded, no silent truncation

Name mode does not go through `readTextFile`, because that function cuts
lines at `MaxLineLength` (2000) and appends `...`, which would corrupt a
skill body. Name mode returns the whole file, validates UTF-8, and emits it
without line numbers. Pagination is refused rather than ignored, so a model
cannot believe it received a slice.

The read is bounded in this order, which matters:

1. Resolve the name against the instance snapshot.
2. Authorize: skills-path membership and, when applicable, the permission
   request — **before** the target is opened.
3. Open the location through a small platform helper that cannot block on a
   non-regular source, then validate the descriptor (details below). A
   directory, FIFO, socket, or device node is rejected with a distinct
   bounded error and the descriptor is closed without a read.
4. Read through `io.LimitReader(f, MaxSkillLoadSize+1)`. If the result
   exceeds `MaxSkillLoadSize`, return the bounded size error. A file exactly
   at the cap succeeds. Nothing is ever read beyond `cap+1` bytes, so the cap
   is a real memory bound rather than a post-hoc check on an unbounded read.
5. Validate UTF-8.
6. `skills.ParseContent`, then `Skill.Validate` on the result. Malformed
   frontmatter and structurally invalid metadata (missing name or
   description, bad name pattern, over-length description) are distinct
   bounded errors. `Validate`'s directory-name check is inert here because
   `ParseContent` leaves `Path` empty; that is deliberate, since the
   canonical location may legitimately be a symlinked directory.
7. Reject a name mismatch between the parsed frontmatter and the requested
   name instead of silently serving a renamed file.

**Opening without a stat/open race.** An earlier draft did `os.Stat`, then
`os.Open`, then re-checked the descriptor. That ordering is wrong: on Unix,
`os.Open` of a FIFO with no writer blocks in the kernel, and nothing in Go
can interrupt it — a `context` deadline does not cancel a blocking `open(2)`,
so the pre-open `Stat` was load-bearing and also racy. The fix is to remove
the window entirely by never relying on a pre-open `Stat` for safety: open
first, in a mode that cannot block, and then judge the descriptor.

Add one tiny build-tagged helper pair beside the tool, following the existing
`internal/lock/lock_unix.go` / `lock_windows.go` convention (`//go:build
!windows` and `//go:build windows`):

```go
// openRegularFile opens path for reading and returns it only when the
// opened descriptor is a regular file. It never blocks on a non-regular
// source, so a FIFO in a skills directory cannot stall the agent.
func openRegularFile(path string) (*os.File, error)
```

- Unix (`view_open_unix.go`): open with `unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)` and wrap the fd with `os.NewFile`. `O_NONBLOCK` makes opening a FIFO or a slow character device return immediately instead of waiting for a peer, and it is a no-op for regular files, so no flag has to be cleared afterwards. `golang.org/x/sys` is already a direct dependency (`go.mod:69`) and `golang.org/x/sys/unix` is already used in `internal/lock/lock_unix.go`, so this adds no dependency.
- Windows (`view_open_windows.go`): plain `os.OpenFile(path, os.O_RDONLY, 0)`. Windows has no filesystem FIFO: named pipes live in the `\\.\pipe\` namespace and are not reachable as a component of a skills directory path, so there is no blocking-open equivalent to defend against and no extra flag is needed.
- Both paths then `f.Stat()` the **descriptor** and require `Mode().IsRegular()`. Anything else (directory, FIFO, socket, device) is closed and reported as the non-regular error. A directory `open` succeeds on Unix and fails on Windows; both outcomes end in the same bounded error, which the tests assert per platform.

A plain `os.Stat` may still be used *before* the open purely to produce a
nicer "no such skill file" message, but it must not be the safety check and
its result must not be trusted after the open.

What this does and does not guarantee, stated plainly: the guarantee is that
**name mode never performs a blocking read on a non-regular source and never
reads unbounded bytes**. It is not a redesign of filesystem TOCTOU handling.
Symlink and path-swap races around the wider filesystem are pre-existing
behavior shared with path mode (the resolver deliberately does not call
`filepath.EvalSymlinks`, and permission checks operate on the pre-open path),
and this phase does not change that. Saying so here is deliberate, so a
reader does not infer a stronger property than the code provides.

Ordinary path reads keep their existing behavior exactly: 2000-character line
cut, 200-line default, the `isSkillFile` effectively-unlimited branch, and no
size cap. `openRegularFile` is used only by name mode in this phase.

### Registry snapshot: per instance, not per Run

The tool captures the same `activeSkills` slice that already feeds `NewAnvilInfoTool` and the tracker in `buildToolsWithState`, so a single tool instance is internally consistent with the tracker it shares.

It is **not** true that a snapshot is pinned for the duration of a `Run`. `PrepareStep` re-reads `a.tools.Copy()` on every step, and `ReloadPlugins` swaps in freshly built tools via `SetTools`. The contract this phase commits to is therefore narrower and accurate:

- Each invocation resolves against the snapshot held by the tool instance that executes it.
- A retained older instance keeps answering from its own snapshot; a newly built instance answers from the new one. Both are self-consistent, and preparation plus `CanonicalTarget` plus the read all use the same instance, so a hook gate can never be computed against a different snapshot than the read.
- Different steps of one `Run` may therefore see different registries. That is pre-existing behavior for `anvil_info` and the tracker, and this phase does not redesign the tool cache.

A miss is phrased "not available in this registry snapshot", never "uninstalled".

### Compaction needs reloadability, not retention

Nothing persists which skills were loaded, and nothing reactivates them after compaction. The only guarantee is reloadability: a `skill_name` call always rereads the source and always returns the full body, regardless of tracker state, instance age, or how many times the same name was loaded before. That is what makes a post-compaction reload work, and it is cheap to test directly.

### Minimal rendering belongs here

Because the schema advertises `skill_name` as soon as this phase merges, a model can call it in the same release. Leaving the renderer untouched would ship a header with an empty path and a copy payload reading `**File:** `. That is the broken half-feature the phase gate exists to prevent, so the minimal renderer and copy branch land here, with their own tests. No feature flag is introduced to buy a phase split. Phase 2 hardens the rendering matrix and the CLI contract; it adds no new rendering behavior unless a matrix case fails.

## Tasks

## Resolver Tasks

### Task 1: Add the registry name resolver in `internal/skills`

**Context:** `internal/skills/skills.go`, `internal/skills/embed.go`, `internal/skills/skills_test.go`

**Files:**
- Create: `internal/skills/lookup.go`
- Create: `internal/skills/lookup_test.go`

**Contract:**

```go
// ErrNotInRegistry is returned when no enabled skill in the snapshot has
// the requested name.
var ErrNotInRegistry = errors.New("skill not in registry snapshot")

// ErrNoLocation is returned when the winning registry entry has no source
// file path recorded, which means discovery produced an unusable entry.
var ErrNoLocation = errors.New("skill has no recorded location")

// Located is a resolved skill plus the canonical location of the source
// that won name precedence.
type Located struct {
	Skill *Skill
	// Location is an absolute filesystem path, or the verbatim
	// anvil://skills/<name>/SKILL.md URI for builtin winners. Symlinks are
	// deliberately not resolved so that relative asset references keep the
	// base directory the skill author intended.
	Location string
	Builtin  bool
}

// BaseDir returns the directory that relative references inside the skill
// resolve against. For builtin winners it splits BuiltinPrefix off first and
// applies path.Dir only to the suffix, because path.Dir on the whole URI
// collapses the "//" and yields anvil:/skills/<name>.
func (l Located) BaseDir() string

// Lookup finds the enabled skill with exactly this name (case sensitive) in
// an already deduplicated and disabled-filtered registry snapshot. It never
// touches the filesystem and never searches.
func Lookup(registry []*Skill, name string) (Located, error)
```

**Steps:**

1. [ ] Write `internal/skills/lookup_test.go` first, table driven, covering: exact match on a disk skill with an absolute `SkillFilePath`; exact match on a disk skill with a **relative** `SkillFilePath`, asserting the result is absolutized against the process working directory (run the subtest with `t.Chdir(t.TempDir())` and assert the prefix is that dir, then assert the same registry entry resolved from a different `t.Chdir` yields a different absolute path — this is the regression guard against absolutizing with a tool `workingDir`); builtin winner yields its `SkillFilePath` verbatim with `Builtin: true`; case mismatch (`Euc-Go` vs `euc-go`) returns `ErrNotInRegistry`; empty name returns `ErrNotInRegistry`; a registry where a user entry shadows a builtin (already deduplicated, so only the user entry is present) resolves to the user path; an entry with an empty `SkillFilePath` returns `ErrNoLocation`; `BaseDir` asserts exactly `anvil://skills/jq` for `anvil://skills/jq/SKILL.md` and `filepath.Dir` for disk skills; a symlinked skill directory keeps the symlink in `Location` (create a real dir plus `os.Symlink`, skip on Windows via `runtime.GOOS`).
2. [ ] Implement `internal/skills/lookup.go`. Detect builtin by `strings.HasPrefix(s.SkillFilePath, BuiltinPrefix)` rather than by `Source`, so a mis-tagged entry cannot be turned into a disk read. Use `filepath.Abs` for disk entries — which resolves against the process working directory, matching what discovery walked — and explicitly do not call `filepath.EvalSymlinks`, with a comment saying why. Never join against any tool-level `workingDir`.
3. [ ] Implement `BaseDir` with the prefix split for builtins (`BuiltinPrefix + path.Dir(strings.TrimPrefix(loc, BuiltinPrefix))`) and `filepath.Dir` for disk skills.
4. [ ] Do not add a discovery fallback, a fuzzy matcher, or an `EffectiveName` match. Exact `Name` only, since `Name` is the callable identity and `DisplayName` exists purely for collision display.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/skills/ -run 'TestLookup|TestLocatedBaseDir' -v
# Expected: all subtests pass, including the two-CWD relative-path case, the
# anvil://skills/jq BaseDir assertion, and the symlink case.
```

## View Tool Tasks

### Task 2: Add the `skill_name` selector, the bounded reader, and every constructor update

**Context:** `internal/agent/tools/view.go`, `internal/agent/tools/view_test.go`, `internal/skills/lookup.go`, `internal/skills/tracker.go`, `internal/agent/coordinator.go`, `internal/agent/agentic_fetch_tool.go`

**Files:**
- Modify: `internal/agent/tools/view.go`
- Create: `internal/agent/tools/view_open_unix.go`,
  `internal/agent/tools/view_open_windows.go`
- Modify: `internal/agent/tools/tools.go` (add the baseline context key pair)
- Modify: `internal/agent/coordinator.go:1021` (registry snapshot)
- Modify: `internal/agent/agentic_fetch_tool.go:175` (explicit empty registry)
- Modify: `internal/agent/common_test.go:161`
- Modify: `internal/agent/tools/view_test.go:268` (`newViewToolForTest`)
- Create: `internal/agent/tools/view_skill_test.go`

All four invocations move with the signature, in this one task, so the tree
compiles at every task boundary. `grep -rn "NewViewTool" --include='*.go' .`
must return exactly five `NewViewTool` lines — the four invocations above plus
the declaration at `internal/agent/tools/view.go:90` — with no un-updated
invocation, before the task is considered done. (`NewViewToolMessageItem` in
`internal/ui/chat` is a different symbol and is not affected.)

**Contract:**

```go
type ViewParams struct {
	FilePath  string `json:"file_path,omitempty" description:"The path to the file to read. Mutually exclusive with skill_name."`
	SkillName string `json:"skill_name,omitempty" description:"Exact name of an enabled skill to load in full. Mutually exclusive with file_path. offset and limit are not supported."`
	Offset    int    `json:"offset,omitempty" description:"The line number to start reading from (0-based)"`
	Limit     int    `json:"limit,omitempty" description:"The number of lines to read (defaults to 200)"`
}

// Field order must match ViewParams exactly; the conversion at the
// permission call site depends on it.
type ViewPermissionsParams struct {
	FilePath  string `json:"file_path"`
	SkillName string `json:"skill_name"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

// MaxSkillLoadSize bounds a single name-mode read. Name mode has no
// pagination escape hatch, so the cap is enforced while reading
// (LimitReader at cap+1) rather than checked afterwards.
const MaxSkillLoadSize = 1024 * 1024 // 1 MiB

// NewViewTool gains one parameter: the enabled registry snapshot used for
// skill_name resolution. It is the same slice passed to the tracker and to
// anvil_info, so one tool instance is internally consistent. The snapshot is
// per instance: a rebuilt tool gets the new registry, a retained instance
// keeps answering from its own.
func NewViewTool(
	lspManager *lsp.Manager,
	permissions permission.Service,
	filetracker filetracker.Service,
	skillTracker *skills.Tracker,
	skillRegistry []*skills.Skill,
	workingDir string,
	skillsPaths ...string,
) fantasy.AgentTool
```

Call-site decisions, chosen deliberately and narrowly:

| Call site | Registry passed | Rationale |
|---|---|---|
| `coordinator.go:1021` (`buildToolsWithState`) | `activeSkills` (the function parameter already in scope) | Same snapshot as the tracker and `anvil_info`. |
| `agentic_fetch_tool.go:175` | `nil` (empty registry), with a comment | The fetch sub-agent works inside a throwaway `tmpDir` on fetched web content, and its prompt (`agentic_fetch_prompt.md.tpl`) is standalone: it never renders the skills catalog or activation guidance, so no prompt tells it to load skills. Name mode there returns the bounded not-in-snapshot error. Do not widen this sub-agent's reach into the user's skills. |
| `common_test.go:161` | `nil` | Keeps cassette-backed tests on path mode. |
| `view_test.go:268` | `nil`, plus a second helper `newViewToolWithRegistryForTest(registry, ...)` | Existing path-mode tests stay untouched. |

Name-mode output, model visible:

```
<skill name="euc-go" location="/Users/me/.config/agents/skills/euc-go/SKILL.md">
...entire file content, frontmatter included, verbatim...
</skill>

References, scripts and assets in this skill resolve relative to /Users/me/.config/agents/skills/euc-go.
```

For a builtin winner the location is the URI, the trailer base directory comes from `Located.BaseDir()` (so `anvil://skills/jq`), and the trailer reads: `This skill is embedded in the Anvil binary. Read its assets with file_path values like anvil://skills/jq/reference.md. They are not files on disk and its scripts cannot be executed.`

Metadata stays `ViewResponseMetadata` with no new fields: `FilePath` is the resolved location, `Content` is the raw file content, `ResourceType` is `skill`, and `ResourceName`/`ResourceDescription` come from the freshly parsed frontmatter.

**Steps:**

1. [ ] Add the context baseline to `internal/agent/tools/tools.go`, following the existing key style:

```go
type skillLoadBaselineKey string

const SkillLoadBaselineContextKey skillLoadBaselineKey = "skill_load_baseline"

// SkillLoadBaseline records how a view call was addressed before PreToolUse
// hooks ran, so the view tool can tell which selector a hook rewrote.
//
// Mode is the pre-hook selector mode: "name", "path", or "none". It is
// recorded for every prepared view call, not just name-mode ones, because a
// hook that introduces skill_name on a path-mode call must be rejected and
// the tool has no other way to detect that.
//
// Resolved is false when a requested name did not resolve at preparation
// time, which must stay distinguishable from "no preparation".
type SkillLoadBaseline struct {
	Mode     string
	Name     string
	Location string
	Resolved bool
}

func WithSkillLoadBaseline(ctx context.Context, b SkillLoadBaseline) context.Context
func GetSkillLoadBaseline(ctx context.Context) (SkillLoadBaseline, bool)
```

2. [ ] Write `internal/agent/tools/view_skill_test.go` before touching `view.go`. Required cases, all with a `t.TempDir()` registry built by hand (no discovery):
   - schema assertions via `tool.Info()`: `required` is empty, `properties` contains `skill_name`, and the `file_path` description mentions mutual exclusivity (this string lands in the cassettes, so the assertion documents what task 4 must patch);
   - no selector; `{"skill_name": null}`; both selectors with no baseline in
     context; `skill_name` set with a baseline whose `Mode` is `"path"`
     (asserting `ErrPathToNameRewrite`, the tool-side half of the
     path-to-name refusal) and the same with `Mode: "none"`;
   - `skill_name` with `offset: 3`; `skill_name` with `limit: 10`;
   - happy path on a 400-line skill containing one 5000-character line, asserting the full line survives with no `...`, no line-number prefixes, and no "File has more lines";
   - builtin `skill_name: "jq"` returning the embedded body, the `anvil://` location in the visible text, and a trailer base directory of exactly `anvil://skills/jq`;
   - hidden-enabled case: a registry entry whose name appears in no prompt catalog loads fine;
   - miss case asserting the message mentions the registry snapshot and that a decoy sibling directory named `euc-go-old` is not suggested;
   - deleted source; unreadable source (`os.Chmod(0o000)`, skipped on Windows); malformed frontmatter; frontmatter that parses but fails `Validate` (empty description); renamed skill (file now declares a different `name`);
   - **size cap trio**: a file of exactly `MaxSkillLoadSize` bytes succeeds; a file of `MaxSkillLoadSize + 1` returns the bounded size error; assert the error path does not include file content;
   - **non-regular sources**: the location is a directory; the location is a
     FIFO created with `syscall.Mkfifo` (Unix only, skipped elsewhere) with
     no writer, asserting the non-regular error. Assert promptness with a
     watchdog, not a context deadline: run the call in a goroutine, `select`
     on its done channel against `time.After(5 * time.Second)`, and
     `t.Fatal` on the timeout branch. A `context` deadline cannot cancel a
     blocking `open(2)`, so the test must be written as "it returned
     quickly" rather than "the timeout rescued us"; the leaked goroutine on
     the failure path is acceptable because the test has already failed.
     Add a direct unit test of `openRegularFile` for a regular file, a
     directory, and (Unix) a FIFO, which is where the real guarantee lives
     and removes the need to race a stat/open swap;
   - **reload/repeat semantics**: two consecutive identical loads both return the full body; then rewrite the file on disk and load a third time from the *same* instance whose tracker already has the name marked, asserting the new body; then build a fresh tool instance over the same registry and assert it also returns the new body;
   - **snapshot isolation**: instance A over registry R1 and instance B over registry R2 (disjoint names), asserting each resolves only its own names and reports not-in-snapshot for the other's, with no cross-talk;
   - metadata assertions for `FilePath`, `ResourceType`, `ResourceName`, `ResourceDescription`, `Content`;
   - relative-`SkillFilePath` registry entry producing an absolute location and a correct trailer, with the tool's `workingDir` set to a *different* temp directory than the process working directory, asserting the location is not joined against `workingDir`;
   - permission behavior in three variants (inside workdir; outside workdir
     but inside a configured skills path; outside both, asserting a
     permission request is made, that it is made before the target is
     opened, and that a denial is surfaced as the permission-denied
     response).
3. [ ] Extend `ViewParams` and `ViewPermissionsParams` as above. Keep field order aligned so the existing conversion compiles unchanged.
4. [ ] Change `NewViewTool` to return a wrapper struct rather than the bare fantasy tool, so task 3 can hang the hook-target methods off it:

```go
type viewTool struct {
	fantasy.AgentTool
	registry []*skills.Skill
}
```

5. [ ] Restructure the tool body: resolve the mode first (`selectViewMode`, which consults the baseline when present), then dispatch. Path mode keeps the existing code path untouched, including `anvil://` handling, permission requests, image handling, `readTextFile`, LSP open/diagnostics, and the existing skill-file metadata branch.
6. [ ] Implement name mode in the bounded order from Design Decisions:
   - reject a non-empty `skill_name` when the context baseline has
     `Mode: "path"` or `Mode: "none"` (`ErrPathToNameRewrite`), before any
     resolution;
   - reject non-zero `Offset`/`Limit` with `offset and limit are not supported with skill_name; the full skill body is always returned`;
   - `skills.Lookup` against the captured registry, mapping `ErrNotInRegistry` to `Skill %q is not available in this registry snapshot. Check the exact name, or read the file directly with file_path.` and `ErrNoLocation` to its own message;
   - builtin: read through `skills.BuiltinFS()` with the same `builtin/` prefix rewrite `readBuiltinFile` uses, full body, no pagination, and the same `MaxSkillLoadSize` check applied to the returned bytes for uniformity;
   - disk: extract `ensureReadAllowed(ctx, call, params, absPath, workingDir, skillsPaths, permissions) (fantasy.ToolResponse, bool, error)` from the existing path-mode logic (it now has two call sites) and call it **before** the open; then `openRegularFile` (the build-tagged helper), descriptor `Stat` plus `IsRegular` check, and `io.LimitReader(f, MaxSkillLoadSize+1)`;
   - validate UTF-8, then `skills.ParseContent`, then `Skill.Validate`, then the requested-vs-declared name check, each with its own message;
   - `skillTracker.MarkLoaded(parsed.Name)` unconditionally on success, never gating content on `IsLoaded`;
   - `filetracker.RecordRead` for disk loads only, matching the existing builtin behavior which records nothing;
   - skip `openInLSPs`/`waitForLSPDiagnostics`; a `SKILL.md` has no useful diagnostics and name mode should not pay the 300ms wait.
7. [ ] Add the two build-tagged helper files. Unix: `unix.Open` with
   `O_RDONLY|O_NONBLOCK|O_CLOEXEC` wrapped by `os.NewFile`. Windows: plain
   `os.OpenFile` with `O_RDONLY`. Both then `Stat` the descriptor and return
   the non-regular error for anything that is not a regular file. Follow the
   `internal/lock/lock_unix.go` / `lock_windows.go` tag style; add no new
   module dependency (`golang.org/x/sys` is already direct at `go.mod:69`).
8. [ ] Update all four constructor invocations per the table above, including
   the `nil` plus comment at `agentic_fetch_tool.go:175`.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/tools/ -run 'TestView|TestReadTextFile|TestReadBuiltin|TestOpenRegularFile' -v
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go build ./...
grep -rn "NewViewTool(" --include='*.go' .
# Expected: every pre-existing view test still passes unchanged, plus the new
# skill-mode and openRegularFile subtests; the build is clean, proving all
# invocations moved; the grep shows exactly four invocations plus the
# declaration, all carrying the registry argument.
```

### Task 3: Hook target preparation and final-target authorization

**Context:** `internal/agent/hooked_tool.go`, `internal/hooks/input.go`, `internal/hooks/runner.go`, `internal/hooks/hooks.go`, `internal/agent/tools/view.go`, `docs/hooks/README.md`

**Files:**
- Modify: `internal/agent/hooked_tool.go`
- Modify: `internal/agent/tools/view.go` (implement the interface)
- Modify: `docs/hooks/README.md` (PreToolUse section: injected `file_path` for `skill_name` calls, the single re-gate, idempotency requirement)
- Create: `internal/agent/hooked_tool_skill_test.go`

**Contract:**

```go
// In internal/agent/tools: implemented only by the view tool.
//
// HookTargetResolver lets hookedTool gate the concrete target of a
// non-path selector. Both methods use the same registry snapshot as the
// instance that will perform the read, perform no content IO, and never
// read file content.
type HookTargetResolver interface {
	// PrepareHookInput records the pre-hook selector mode and, for a
	// skill_name-only call, resolves it to the file the tool would read and
	// writes that path into the payload hooks will see. It returns the
	// prepared input and a context carrying the baseline. For a path-mode or
	// zero-selector call it returns the input unchanged and a baseline whose
	// Mode is "path" or "none". When a name does not resolve it returns the
	// original input and a baseline with Resolved=false.
	PrepareHookInput(ctx context.Context, input string) (string, context.Context, error)

	// CanonicalTarget reports the concrete target the tool would read for
	// this input, interpreted against the baseline in ctx. It returns
	// ErrAmbiguousRewrite when both selectors were rewritten to disagreeing
	// destinations, and ErrPathToNameRewrite when a hook introduced
	// skill_name on a call that arrived in path mode.
	CanonicalTarget(ctx context.Context, input string) (HookTarget, error)
}

type HookTarget struct {
	Mode     string // "name" | "path" | "none"
	Name     string
	Location string
	Resolved bool
}

var ErrAmbiguousRewrite = errors.New(
	"both skill_name and file_path were rewritten by a hook to different targets")

var ErrPathToNameRewrite = errors.New(
	"PreToolUse hooks may not convert a file_path read into a skill_name load")
```

`hookedTool.Run` keeps its current shape for every tool that does not
implement the interface. For `view` it gains the ten-step algorithm from
Design Decisions, with the second pass issued through the same
`h.runner.Run` entry point behind a local `regated bool`. Note that a
baseline now exists for path-mode `view` calls too, carrying only the mode;
the path-mode branch is still a single pass with no canonical-target work,
and the baseline is what makes the path-to-name refusal enforceable.

**Steps:**

1. [ ] Write `internal/agent/hooked_tool_skill_test.go` first, using a real
   `hooks.Runner` built from shell scripts in `t.TempDir()` (no mocking of
   the hook engine, per the testing skill's "real dependencies" default) and
   a real view tool over a temp registry. Each hook script appends a line to
   a log file per invocation so pass counts are assertable. Cases, matching
   worked examples A through K plus the basics:
   - deny when `ANVIL_TOOL_INPUT_FILE_PATH` matches the resolved skill path blocks the load (no content, no `filetracker` record);
   - `halt` stops the turn (`resp.StopTurn`);
   - `allow` pre-approves the permission prompt for a skill outside the working directory and outside every configured skills path, when nothing retargets;
   - `context` still appends to the response, for both a single pass and a re-gated call (concatenated in pass order);
   - **example A**: combined allow plus retarget, destination deny fires, load blocked, permission service records zero grants, hook log shows exactly two passes;
   - **example A-variant**: same retarget but no destination deny; assert the read happens only after a permission request (no inherited approval) and that denying that request blocks the read;
   - **example B**: unresolved name rewritten to a valid one; baseline `Resolved:false`; second gate runs; pass-1 allow does not carry;
   - **example C**: consistent rewrite of both fields loads the new skill with no ambiguity error;
   - **example D**: inconsistent rewrite of both fields returns the ambiguous error and reads nothing;
   - **example E**: pass-2 hook retargets again, bounded double-retarget error, hook log shows exactly two passes, nothing read;
   - **example F**: a plain `file_path` call with allow plus rewrite hooks runs exactly one pass and produces byte-identical output to the same scenario against an unmodified `hookedTool` (capture the expectation as an explicit string assertion so the regression is visible);
   - **example G**: a rewrite that clears only `skill_name`; assert the merged payload still carries the injected `file_path`, that exactly one pass ran, that the pass-1 approval was not discarded, and that the response is the path-mode read of that same file rather than the zero-selector error;
   - **example G2**: a rewrite clearing both selectors; no second pass; zero-selector error;
   - **example G3**: a rewrite clearing `skill_name` and changing `file_path`; exactly two passes; a deny in pass 2 blocks the read;
   - **example H**: retarget between two skills-path-exempt skills; no permission prompt; destination deny still blocks;
   - **example I**: a path-mode call whose pass-1 hook introduces `skill_name`; assert the bounded `ErrPathToNameRewrite` response, exactly one pass, nothing read, and no permission grant. Add the tool-level half of the same assertion in `internal/agent/tools/view_skill_test.go` by invoking the tool directly with a `Mode: "path"` baseline in context;
   - **example J**: a path-mode call with a path-to-path rewrite; exactly one pass, the rewritten file is read, no path-to-name error;
   - **example K**: a `skill_name` call with no hooks configured, and separately with an unwrapped tool (the sub-agent case), works with no baseline and strict exactly-one-selector validation.
2. [ ] Add a payload-level assertion: build the prepared input through `PrepareHookInput` and assert `hooks.BuildEnv(...)` exports `ANVIL_TOOL_INPUT_FILE_PATH` equal to the resolved location, and that `hooks.BuildPayload` emits a `tool_input.file_path` equal to it, for both pass 1 and the re-gated pass-2 payload. This is the regression guard for the original bypass.
3. [ ] Implement `HookTargetResolver` in `tools`, implement both methods on
   `viewTool` (reusing `skills.Lookup` over the instance snapshot), and wire
   the algorithm into `hookedTool.Run`. Keep the failure path non-fatal: if
   `PrepareHookInput` returns an error, log at debug, leave `call.Input`
   untouched, and stash the unresolved baseline so pass-1 hooks still run and
   the tool still produces its own bounded error.
4. [ ] Implement the canonical-target tables inside `CanonicalTarget`,
   including the "both changed but consistent" case, `ErrAmbiguousRewrite`,
   the `skill_name`-cleared rows (which depend on the shallow merge
   preserving `file_path`), and `ErrPathToNameRewrite` for a
   `Mode: "path"`/`Mode: "none"` baseline. Enforce the path-to-name refusal
   in `hookedTool.Run` as well, since that is the only place that knows the
   pre-hook mode for a call the tool would otherwise read as an ordinary
   name-mode request.
5. [ ] Merge pass-2 metadata: summed hook count, joined reasons, pass-2 decision authoritative, and a `retarget` boolean so logs and the UI can see it. Any new `slog` message starts with a capital letter (`task lint:log`).
6. [ ] Do not wrap sub-agent tools with hooks. `wrapToolsWithHooks` keeps its `isSubAgent` early return untouched, and the test above locks that in.
7. [ ] Update `docs/hooks/README.md`: in the PreToolUse section, document
   that a `view` call using `skill_name` has its resolved `file_path`
   injected into the hook payload before hooks run; that a rewrite which
   changes the destination discards the earlier `allow` and triggers one
   additional PreToolUse pass on the final target; that a second retarget is
   refused; that a rewrite may not turn a `file_path` read into a
   `skill_name` load; that `updated_input` shallow-merges, so clearing
   `skill_name` alone leaves the injected `file_path` and results in an
   ordinary path read of the same file; and that PreToolUse hooks must
   therefore be idempotent. Do not restate the whole protocol.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/ -run 'TestHookedTool' -v
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/hooks/ ./internal/agent/tools/
# Expected: new hook subtests pass, including the two-pass counts and the
# single-pass path-mode regression; existing hook and tool suites unchanged.
```

## Wiring and Fixture Tasks

### Task 4: Repair the VCR cassettes offline

**Context:** `internal/agent/common_test.go`, `internal/agent/testdata/TestOrchestratorAgent/claude/`

**Files:**
- Modify: `internal/agent/testdata/TestOrchestratorAgent/claude/*.yaml` (13 files, 49 interactions; only request bodies change, and only in the interactions that carry the tool schema)

**Why this is mechanical but not trivial:** the matcher compares whole request bodies. The `view` entry changes in three ways at once — the `file_path` property description gains the mutual-exclusion sentence, a `skill_name` property is inserted (fantasy emits properties alphabetically, so it lands between `offset` and… check the actual output rather than assuming), and `"required":["file_path"]` becomes an empty/absent `required`. Bodies are YAML single-quoted scalars with `''` for apostrophes and `\u003c`/`\u003e` for angle brackets, so replacements must operate on the escaped text.

**Steps:**

1. [ ] Derive the replacement from the code, not by hand. Write a throwaway helper under `internal/agent` (a `_test.go` file or a `go run` snippet you delete afterwards) that builds the real `view` tool and prints `json.Marshal` of its `Info()` schema plus the description string, so the exact new fragment comes from the same code the runtime emits. Do not invent fragments, and do not assume a fixed number of edits per file.
2. [ ] Run the suite and read the printed diff to capture the exact old fragment:

```bash
cd internal/agent
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test . -run TestOrchestratorAgent 2>&1 | head -60
```

3. [ ] Apply the replacement with a script over every `*.yaml` under `testdata`, replacing **all** occurrences in each file (interaction counts vary from 2 to 6 and not every interaction carries the same blocks), and print the per-file replacement count so a file with zero replacements is visible rather than silently skipped:

```bash
python3 - <<'EOF'
import pathlib
old = '<paste exact old fragment captured from the diff>'
new = '<paste exact new fragment generated from the tool schema>'
for p in sorted(pathlib.Path('testdata').rglob('*.yaml')):
    s = p.read_text()
    n = s.count(old)
    if n:
        p.write_text(s.replace(old, new))
    print(p, n)
EOF
```

4. [ ] Verify the edit was surgical and replay-only:

```bash
git diff --stat internal/agent/testdata
git diff internal/agent/testdata | grep -c '^[+-].*"id": "msg_'        # expect 0
git diff internal/agent/testdata | grep -c '^[+-].*event: message_'    # expect 0
git diff internal/agent/testdata | grep -c '^[+-]  response:'          # expect 0
git diff internal/agent/testdata | grep -c 'skill_name'                # expect > 0
```

Every changed line must be inside a request `body:` scalar. No response body, header, URL, or interaction ordering may change, and `content_length` values are part of the recorded request metadata — if the matcher ignores them leave them alone, and if the suite complains, recompute them in the same script rather than re-recording.

5. [ ] Do not run `task test:record`, and do not add a permanent
   cassette-updating tool to the repo. The helper from step 1 is scaffolding:
   delete it before the change goes up for review. If a future schema change
   needs the same work, the offline recipe lives in this plan.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/agent/ -run TestOrchestratorAgent
# Expected: all 13 cassette-backed subtests pass with no
# "Request interaction not found", and no network access occurs.
```

## Presentation Tasks

### Task 5: Minimal selector-aware rendering and copy output

**Context:** `internal/ui/AGENTS.md`, `internal/ui/chat/file.go`, `internal/ui/chat/tools.go`

**Files:**
- Modify: `internal/ui/chat/file.go` (`ViewToolRenderContext.RenderTool`)
- Modify: `internal/ui/chat/tools.go` (one pending helper, and the `tools.ViewToolName` case in `formatParametersForCopy`)
- Create: `internal/ui/chat/view_skill_render_test.go`

**Contract.** Two constraints shape this: the renderer currently returns the anonymous pending row *before* parsing params (`file.go:40-42`), and `toolParamList` treats `params[0]` as the main parameter and pairs everything after it as `key`,`value`. So the skill name must be the main parameter, and the location must arrive as a key/value pair.

```go
// In tools.go, next to pendingTool:
//
// pendingToolWithParams renders an in-progress tool together with the
// parameters already known from the (possibly partial) request JSON. It is
// pendingTool plus a param list, so the shimmer is preserved.
func pendingToolWithParams(
	sty *styles.Styles,
	name string,
	anim *anim.Anim,
	width int,
	opts *ToolRenderOpts,
	params ...string,
) string

// In file.go:
//
// resolvedSkillLocation returns the location the view tool reported for a
// skill_name load, or "" when the call has not completed or carried no
// usable metadata. Builtin skills report an anvil:// URI, which must not be
// run through path prettifying.
func resolvedSkillLocation(opts *ToolRenderOpts) string
```

Renderer shape:

```go
var params tools.ViewParams
parsed := json.Unmarshal([]byte(opts.ToolCall.Input), &params) == nil

if opts.IsPending() {
	// Streaming input is often incomplete JSON, in which case there is
	// nothing to show and today's anonymous row is still correct.
	if parsed && params.SkillName != "" {
		return pendingToolWithParams(sty, "View", opts.Anim, width, opts, params.SkillName)
	}
	return pendingTool(sty, "View", opts.Anim, opts.Compact)
}
if !parsed {
	return toolErrorContent(sty, &message.ToolResult{Content: "Invalid parameters"}, width)
}

var toolParams []string
switch {
case params.SkillName != "":
	toolParams = []string{params.SkillName}
	if loc := resolvedSkillLocation(opts); loc != "" {
		toolParams = append(toolParams, "location", loc)
	}
default:
	toolParams = []string{fsext.PrettyPath(params.FilePath)}
	if params.Limit != 0 {
		toolParams = append(toolParams, "limit", fmt.Sprintf("%d", params.Limit))
	}
	if params.Offset != 0 {
		toolParams = append(toolParams, "offset", fmt.Sprintf("%d", params.Offset))
	}
}
```

Copy output:

```go
case tools.ViewToolName:
	var params tools.ViewParams
	if json.Unmarshal([]byte(t.toolCall.Input), &params) == nil {
		if params.SkillName != "" {
			return fmt.Sprintf("**Skill:** %s", params.SkillName)
		}
		// existing file/limit/offset formatting, unchanged
	}
```

**Steps:**

1. [ ] Write `internal/ui/chat/view_skill_render_test.go` first. Use `ansi.Strip` from `github.com/charmbracelet/x/ansi` before substring assertions rather than matching styled bytes. Cases: pending with complete name-mode JSON renders the name and no `limit`/`offset`; pending with *truncated* streaming JSON (for example `{"skill_na`) renders exactly today's anonymous pending row and does not panic; pending path-mode renders exactly today's anonymous pending row; a pending-to-success transition for the same call ID (render pending, then render with a result) shows name first and then name plus location; completed name-mode with disk metadata renders the shortened location; completed name-mode with builtin metadata renders `anvil://skills/jq/SKILL.md` verbatim; result metadata absent or invalid JSON renders name only with no empty separator artifacts; path-mode completed rendering is unchanged (assert the same substrings the existing view tests use); copy output for name mode is `**Skill:** euc-go` with no `**File:**`, for path mode unchanged including `limit`/`offset` lines, and for a malformed call with neither selector produces no panic and no stray labels.
2. [ ] Implement `pendingToolWithParams`, the selector-aware assembly, `resolvedSkillLocation`, and the copy branch. Keep the helpers beside their existing neighbors; do not add new files for a dozen lines, do not add styles, and do no IO in render.
3. [ ] Leave the success body path (`toolOutputSkillContent`) untouched. It already renders the loaded-skill indicator from `meta.ResourceType` for both modes, and name-mode results always carry that metadata, so the empty `params.FilePath` never reaches `toolOutputCodeContent`.

**Verify:**

```bash
CGO_ENABLED=0 GOEXPERIMENT=greenteagc go test ./internal/ui/chat/ -run 'TestViewSkill|TestView' -v
# Expected: new subtests pass; existing chat tests unchanged.
```

### Task 6: Phase close-out

**Context:** whole repo

**Steps:**

1. [ ] `gofumpt -w .` (fall back to `goimports` then `gofmt` if unavailable).
2. [ ] `task lint` (includes `lint:log`, so any new `slog` message must start with a capital letter).
3. [ ] `task test`.
4. [ ] Delete the cassette-schema scaffolding from task 4 and confirm `git status` shows no stray files.
5. [ ] Manual TUI check, using the `tui-manual-testing` skill in `.agents/skills/tui-manual-testing/`: build with `task build`, start a session, and ask the agent to call `view` with `skill_name` set to an installed disk skill and then a builtin. Confirm the body arrives, the header shows the name while streaming and the name plus location once complete, and the loaded-skill indicator appears. Rendering breadth (compact mode, narrow widths, degraded metadata) is phase 2.
6. [ ] Prepare the change for human review. Committing, pushing, and opening
   a PR all require explicit authorization at implementation time; this plan
   grants none. Do not merge, and do not start phase 2 until the human has
   merged phase 1.

**Verify:**

```bash
task lint
task test
# Expected: lint clean; full suite green (baseline for this worktree was green at f549a2ca).
```

## Dependencies and Parallelization

Linear: task 1 → task 2 → task 3 → task 4, then task 5, then task 6. Task 2 depends on the resolver contract; task 3 needs `viewTool` to exist; task 4 needs the final schema; task 5 needs `ViewParams.SkillName`. Resolver, view tool, and hook work touch different packages but the coupling between the resolver contract and the tool behavior is the whole point of this phase, so run them sequentially in one agent context. Task 5 is a different subsystem (TUI) and can run in a second agent context once task 2 has landed, but it must not be split into a separate phase: the schema change and its rendering must merge together.

## Open Decisions

Provenance matters here, so each row says where the decision came from.
"Requested behavior" means the requester asked for it. "Engineering
constraint" means the code or platform forces it and review verified that.
"Proposed default" means the planner chose it and it is cheap to reverse.

| Decision | Value | Source | Reversal cost |
|---|---|---|---|
| Exactly one public selector | enforced | requested behavior | n/a |
| Hooks must see the final target, with the pass-1 approval discarded on retarget | enforced, one extra gate | engineering constraint (parallel hooks, call-ID-keyed approval) | n/a |
| Destination, not selector shape, decides whether a second gate is owed | enforced | engineering constraint (shallow-merge semantics) | n/a |
| Path-to-name hook rewrites | refused with a bounded error in v1 | proposed default (simplest bounded choice; full support is the reversal) | one branch plus a re-gate from a path-mode origin |
| Clearing `skill_name` alone | path mode at the same canonical target, no second gate | engineering constraint (`shallowMerge` preserves `file_path`) | n/a |
| Rendering ships with the schema | in this phase | engineering constraint (schema derives from struct tags) | n/a |
| All constructor invocations move with the signature | enforced | requested behavior | n/a |
| Non-blocking open plus descriptor check, no pre-open safety `Stat` | enforced | engineering constraint (a blocking `open(2)` cannot be cancelled) | n/a |
| Platform split via build-tagged helper files | `view_open_unix.go` / `view_open_windows.go`, `golang.org/x/sys/unix` on Unix | proposed default, following `internal/lock` | one file pair |
| `agentic_fetch` view registry | `nil` (empty) | proposed default | one argument |
| Name-mode size cap | 1 MiB, enforced with `LimitReader(cap+1)` | bound-while-reading is an engineering constraint; the number is a proposed default | one constant |
| Line numbers in name mode | none | proposed default | small output change |
| Content returned | whole file including frontmatter | proposed default | small output change |
| LSP diagnostics in name mode | skipped | proposed default | three lines |
| `filetracker.RecordRead` in name mode | disk loads only | proposed default | one line |
| Consistent double rewrite (both selectors agree) | accepted as a name-mode retarget | proposed default (replaces the earlier blanket ambiguity error) | one branch |
| Disagreeing double rewrite | bounded error | proposed default | one branch |
| Second retarget | bounded error, never a third pass | engineering constraint (bounding the gate loop) | n/a |
| Pending header shows the name only when the streamed JSON already parses | yes | proposed default | one branch |

## Review Notes

Three adversarial passes have run against this phase. The items below are
the verified changes that came out of them, stated as what the design now
does and why.

- **Hook gating is destination-keyed, not call-keyed.** Hooks run in parallel
  over one payload, the aggregate can combine one hook's `allow` with
  another's `updated_input`, and the approval is keyed only by `call.ID`
  (`internal/agent/hooked_tool.go:79-84`). The tool therefore reports the
  canonical target it would read, and any change of destination discards the
  earlier approval and buys exactly one more bounded gate on the final
  payload. A second retarget is refused rather than looped.
- **`updated_input` is a shallow merge, and the plan's example G was wrong.**
  `shallowMerge` preserves keys the patch omits, so clearing `skill_name`
  alone leaves the injected `file_path`: that is path mode at the same
  canonical target, not a zero-selector call. Same destination means no
  second gate; a changed destination means one; clearing both selectors is
  the only route to the zero-selector error. Examples G, G2, and G3 cover the
  three outcomes and each is a required test.
- **Path-to-name hook rewrites are refused in v1.** Preparation only resolves
  name-mode calls, so a hook that turns a path read into a skill load would
  enter name mode ungated. Refusal is the simplest bounded choice; ordinary
  path-to-path rewrites are untouched, and public name-mode calls without
  hooks are unaffected. Enforcement is in `hookedTool.Run` (which knows the
  pre-hook mode) *and* in `view` (which rejects a `skill_name` arriving with
  a `Mode: "path"` baseline rather than treating it as a fresh name call).
- **The bounded read no longer depends on a pre-open `Stat`.** A blocking
  `open(2)` on a FIFO cannot be cancelled by a context, so the earlier
  stat-then-open ordering was both racy and load-bearing. Name mode now opens
  through a build-tagged helper (`O_NONBLOCK` on Unix, plain read-only on
  Windows, which has no filesystem FIFO) and judges the descriptor, then
  reads through `LimitReader(cap+1)`.
- **The FIFO test asserts promptness, not rescue.** It uses a watchdog
  goroutine with `t.Fatal` on timeout, plus a direct unit test of
  `openRegularFile`, because no test can cancel a blocked open.
- **Limits are stated, not implied.** Symlink and path-swap races in the
  wider filesystem are pre-existing behavior shared with path mode and are
  not redesigned here. The property this phase guarantees is narrower: name
  mode never performs a blocking read on a non-regular source and never reads
  unbounded bytes.
- **Constructor count corrected.** There are four `NewViewTool` invocations
  plus the declaration, not five call sites; all four move in the same task
  as the signature, and `agentic_fetch` gets an explicitly empty registry
  because its prompt never mentions skills.
- **Smaller verified corrections.** `path.Dir` on an `anvil://` URI collapses
  the double slash, so builtin directory math splits the prefix first;
  relative registry paths absolutize against the process working directory
  (what discovery walked), never the tool's `workingDir`; `Validate` runs
  after `ParseContent`; the snapshot contract is per tool instance, not per
  `Run`; `toolParamList` pairs everything after `params[0]`, so the skill
  name is the main parameter and the location is a key/value pair; the
  cassette inventory is 13 files and 49 interactions and the patch script
  reports per-file counts; and compaction is a reloadability guarantee, not
  retention.

This plan stays IN_PROGRESS for tasks 1-3. Tasks 4 and 5, and any push,
PR, merge, or worktree cleanup, require separate explicit authorization
that has not been given.
