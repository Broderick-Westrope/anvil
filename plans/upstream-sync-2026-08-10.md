# Upstream Sync — v0.77.0 → v0.88.1

**Range**: `afea814e4b13359745b71c81aab2498c39a247f5..upstream/main`
**Total**: 445 commits, 352 after filtering (CLA / dependabot / auto-update / version bumps)
**Started**: 2026-08-10
**Branch**: `upstream-cherry-picking`

## Review checkpoints

Reviewed-and-merged work is tagged so the next review only diffs new
material: `git diff <latest tag>...HEAD`. Tag the merge commit on `main`
after each reviewed batch group lands, using
`review/<date>-sync-<scope>`, and add it here.

| Tag | Commit | Covers |
|---|---|---|
| `review/2026-08-10-sync-batches-1-3` | `b1b2f638` | Batches 1–3, dep bumps, granular-permissions merge, review-round fixes |

## Rules

- Cherry-picks are **sequential**, one batch at a time. Never parallel.
- Rewrite `github.com/charmbracelet/crush` → `github.com/Broderick-Westrope/anvil`.
- `styles.CharmtonePantera()` / `styles.HypercrushObsidiana()` → `styles.TokyoNight()`.
- `attachments.NewRenderer` takes 5 args locally (extra `sty.Attachments.Skill`).
- Commit format: `<original message> (cherry-pick <short-hash>)`.
- Verify `go build ./...` + `go test` on affected packages after each batch.
- Update `upstream-tracking.json` only when the sync is finished.

## Local divergences to watch

| Area | Note |
|---|---|
| MCP OAuth | Already implemented locally (`internal/agent/tools/mcp/oauth.go`, design doc in `plans/completed/design-2026-05-29-mcp-oauth.md`). Upstream's version will conflict — cherry-pick only targeted fixes, not the feature. |
| Lazy MCP | Local-only feature (`internal/agent/lazy_mcp.go`, `enable_mcp` tool). Any upstream MCP commit touching tool-list assembly needs care. |
| LSP tools | Local `lsp_*` tools already exist. Upstream "LSP superpowers" commits are a competing implementation — skip. |
| Config | Local `config.Service`. Upstream is migrating to a Bash `crushrc` DSL — skip entirely. |
| Telemetry | Local telemetry was removed. Skip herdr and PostHog-adjacent commits. |
| Elapsed timer | Local `internal/ui/chat/agent.go` already has elapsed-time handling; verify before picking `dc160997`. |
| Session trees | `sessions.CreateTaskSession` validates that the parent session exists, and `sessions.Rename` takes a 4th `titleIsCustom` arg. Upstream tests that pass a missing parent, or `Rename` calls with 3 args, need adapting. |
| Parallel tool execution | `OnToolResult` runs on parallel goroutines under `sessionLock`. Any upstream state shared between `OnToolCall` and `OnToolResult` needs its own mutex; upstream is single-threaded here. |
| Provider switch shape | `getProviderOptions` in `coordinator.go` lacks upstream's Alibaba-Singapore and Fireworks branches until batches 3 and 7 land. Drop those hunks when they show up in unrelated commits. |
| Anthropic OAuth | Fork-only provider. Upstream's `applyToken` / `exchange` only handle Copilot and Hyper; every touch of those needs the `catwalk.InferenceProviderAnthropic` case re-added, and `anthropicoauth.RefreshToken` takes the whole `*oauth.Token`, not just the refresh string. |
| Config prerequisites | Upstream's config work assumes `internal/lock` (flock), `atomicWriteFile`, `cloneForWrite`, and `setConfig`, none of which we had. `internal/lock` and `atomicwrite.go` are now vendored; see batch 2 notes. |
| Test fixture names | Upstream tests write `crush.json` fixtures. Our loader only discovers `anvil.json`, so a fixture keeping the upstream name silently loads an empty config instead of failing loudly. |

## Batch progress

Status: `pending` / `in progress` / `done` / `skipped`

| # | Batch | Commits | Status | Notes |
|---|---|---|---|---|
| 1 | Agent & tool correctness | 15 | **done** | 12 picked, 3 deferred/skipped |
| 2 | Config, auth & race fixes | 9 | **done** | 8 picked, 1 deferred to batch 3 |
| 3 | Provider fixes | 11 | **done** | 9 picked, 1 N/A, 1 folded into dep bump |
| 4 | Performance | 12 | **done** | 9 picked, 2 deferred to batch 8, 2 skipped |
| 5 | UI fixes | 15 | pending | |
| 6 | MCP fixes | 5 | pending | |
| 7 | Model auto-discovery + enrichers | 10 | pending | optional |
| 8 | Bang mode | 20 | pending | optional |
| 9 | Question tool | 18 | pending | optional |
| 10 | Dialog responsive sizing | 19 | pending | optional |
| 11 | Scrollable sidebar | 12 | pending | optional |
| 12 | Chat scrollbar | 2 | pending | optional |
| 13 | Tool call expansion UI | 2 | pending | optional |
| 14 | Attachment chip remove button | 4 | pending | optional |
| 15 | Misc features | 5 | pending | optional |
| 16 | Dep bump (fantasy/catwalk) | 1 | **done** | ran after batch 3's first pass; unblocked 5 commits |

---

## Batch 1 — Agent & tool correctness

**Status: done.** 12 cherry-picked, 3 not applied.

```
1cfa9a15 fix(subagents): fixed subagents returning empty responses (#3125)
d3af321b fix(shell): isolate child processes from Crush's session (#3097)
a9e3a57f fix(shell): skip persistence when session no longer exists
46c1799a fix(title): fallback title generation
bb44fb1f fix(hooks): bridge Claude Code additionalContext onto HookResult.Context
21a457d5 fix: validate tool call JSON before storing to prevent stuck conversations
f75435a2 fix(agent): fall back to first reasoning level when default is unset (#3218)
a08e3329 fix(bash): keep TruncateOutput from splitting UTF-8 characters
d3d68045 fix(agent): prevent session bricking when non-vision models receive tool result media
188dea64 fix(fsext): DirTrim renders wrong character for non-ASCII directory names (#3214)
67f50014 fix(glob): keep file search fast and bounded on large directories
18c27999 fix(tools): report every matching line per file in internal grep (#2994)
ae9257b9 fix(agent): serialize in-process run dispatch to prevent concurrent turns
fbf59341 fix(agent): close dispatch completion-boundary cancel race
b5cd46c2 fix(errors): properly supress context cancelation error
```

### Not applied

| Commit | Disposition | Reason |
|---|---|---|
| `a9e3a57f` | deferred to batch 8 | Depends on bang mode (`shell.PersistFunc`, `message.ShellCommand`), which doesn't exist locally yet. |
| `ae9257b9` | skipped | Rewrites `Run` dispatch around `call.Accepted`, `canceledBySeq`, `sessionMu`, `notify.RunComplete`, `persistCanceledTurn` — all client/server infrastructure we don't have. |
| `b5cd46c2` | already fixed | Local `sendMessage` already returns early on `errors.Is(err, context.Canceled)`. |

### Adaptations worth remembering

- **`46c1799a`** — the deferred fallback rename needed the local 4-arg `sessions.Rename(..., false)`.
- **`21a457d5`** — added a dedicated `sanitizedMu`, because `sanitizedToolCalls` is written in
  `OnToolCall` and read in `OnToolResult`, which run on parallel goroutines in this fork.
- **`f75435a2`** — kept `effectiveReasoningEffort` but dropped the Alibaba-Singapore and Fireworks
  provider branches; those belong to batches 3 and 7.
- **`fbf59341`** — took `csync.Map.CompareAndDelete` and the `activeCancel` pointer-identity
  wrapper, dropped the accept/dispatch bookkeeping. Also converted the three explicit mid-run
  `activeRequests.Del` calls to `CompareAndDelete` so they only release our own registration.
- **`d3af321b` fallout** — the pre-existing `exec_unix_test.go` (from cherry-pick `f32dcffc`)
  referenced a package-level `killTimeout` that this commit turned into a parameter. Renamed the
  references to `defaultKillTimeout`.
- **`d3af321b` fallout, behavioural** — `Setsid` detaches the child from the controlling terminal,
  so `sh` translates our process-group signal into a normal exit (128+SIGHUP) instead of dying
  signalled. `exitStatusFromError` only consulted `ctx.Err()` on the signalled path, so
  cancellation stopped being visible to `IsInterrupt`, which the bash tool and agent loop depend
  on. Fixed by checking `ctx.Err()` first, in follow-up commit `a54395f8`.

### Known pre-existing failure

`go test -race ./internal/agent/` fails at the sync base commit (`929ddad7`) and still does:
`generateTitle` is started with `go`, outlives the test, then logs a VCR cassette mismatch after
completion. Not introduced by this sync, so left alone. Plain `go test ./...` is green.

## Batch 2 — Config, auth & race fixes

**Status: done.** 8 cherry-picked, 1 deferred.

```
b10f890f fix(config): make concurrent config access race-free via copy-on-write
55b2f0d1 fix(config): prevent data race when reading config during reload (#3362)
de679203 fix(auth): stop two Crush instances from invalidating each other's login
63dc1f01 fix(auth): stop parallel sessions from invalidating each other's login
1535ebb7 fix(config): prevent stale ReasoningEffort from leaking across providers (#3209)
d4dc84e9 fix(config): prevent startup deadlock when configured model ID is invalid
4dd4442a fix(config): retry transient Windows rename failures in atomicWriteFile (#3469)
213ad794 fix(config): load system-wide config from /etc/crush/crush.json (#2984)
461976d0 fix(commands): scope logout command to oauth providers
```

### Not applied

| Commit | Disposition | Reason |
|---|---|---|
| `63dc1f01` | deferred to batch 3 | Bundles three things: model pinning across reloads, borrowing a peer's rotated refresh token, and `WaitForTokenChange`/`SignalAuthComplete`. The auth-signal half is the counterpart to `64bbbebc` (OnAuthRefresh, batch 3), and the conflict also drags in `EnabledChannels` from the skipped channels feature plus a `login.go` shape we don't share. Adapt it together with `64bbbebc`. |

### Prerequisites vendored

Upstream's config work sits on commits we never picked, so two pieces were brought in by hand:

- **`internal/lock`** — copied from `upstream/main` (4 files, no external deps). Cross-process
  advisory flock; `de679203` needs it for the per-provider refresh lock.
- **`internal/config/atomicwrite.go`** — taken at `4dd4442a^`, so the later cherry-pick of
  `4dd4442a` applied cleanly on top and added the Windows rename retry.

`ConfigStore.atomicWrite` is a local reimplementation: upstream's takes a cross-process
`lockConfig` flock, ours serialises on the existing in-process `mu`. Atomic rename means a reader
never sees a torn file, but two Anvil *processes* writing the same config file can still lose an
update. Worth revisiting if we adopt the rest of upstream's config-locking chain.

### Adaptations worth remembering

- **`b10f890f`** — kept our `pluginsChangedHook` and `Options.ProjectDirectory` (upstream renamed
  it `DataDirectory`); dropped `internal/agent/agenttest/coordinator.go`, an upstream-only test
  helper we don't have.
- **`55b2f0d1` — caused a deadlock, fixed.** Upstream turns `writeMu` into an `RWMutex` and has
  the metadata readers (`Resolver`, `KnownProviders`, `Overrides`, `LoadedPaths`) take `RLock`.
  `writeMu` is held for the whole reload, and our fork-only `pluginsChangedHook` runs *inside*
  that window and calls back into those readers — `RLock` while the same goroutine holds `Lock`
  deadlocks. Introduced a separate `metaMu` for that metadata plus a `setMeta` helper, and pinned
  the behaviour with `internal/config/reload_hook_deadlock_test.go`. Upstream has no such hook, so
  nothing upstream would ever catch this.
- **`de679203`** — restored the Anthropic cases in `applyToken` and the refresh switch, and
  changed `exchange` to take the whole `*oauth.Token` rather than just the refresh string, because
  `anthropicoauth.RefreshToken` compares the current access token against the credentials file to
  detect a peer refresh. Dropped the unused `configLockDeadline` const (no `lockConfig` here).
- **`de679203` test fixture** — `refresh_singleflight_test.go` wrote its fixture as `crush.json`.
  Our loader only discovers `anvil.json`, so the post-refresh reload loaded an empty config and
  dropped the provider, failing the assertion for a reason unrelated to the fix. Renamed.
- **`213ad794`** — system config path rebranded to `/etc/anvil/anvil.json` (`f80fcc35`), and the
  test's `configureProviders` call had an extra upstream `ctx` argument our signature doesn't take.

## Batch 3 — Provider fixes

**Status: done.** 9 cherry-picked, 1 not applicable, 1 absorbed by the dep bump.

The dependency bump (batch 16) was promoted and run mid-batch; see below.

Also reconsider `63dc1f01` (deferred from batch 2) alongside `64bbbebc`; both concern OAuth
refresh and the auth-complete signal.

```
604a7e30 fix(providers): swap the provider cache instead of rewriting it
78a205cd fix(copilot): guard nil request body in initiator transport (#3246)
8ccf6945 fix(copilot): add additional responses models (#3416)
3ac7f1c4 fix(baseten): make the thinking on/off toggle work for baseten
aca40878 fix(baseten): fix "none" reasoning level (#3386)
d341d84b fix(alibaba): fix missing thinking traces on deepseek via alibaba (#3259)
a882695e fix: enable thinking blocks for minimax m3 on opencode providers (#3335)
d0dc9fc9 feat: prepare for gpt-5.6 (#3270)
64bbbebc feat: integrate fantasy OnAuthRefresh for transparent auth retry
8db57337 feat: recover cleanly from mid-stream provider connection resets
4be77c56 feat: log provider warnings from fantasy step results
```

### Blocked on the dep bump

We are on `catwalk v0.44.28` / `fantasy v0.31.1`; upstream is on `catwalk v0.48.4` /
`fantasy v0.39.0`. These need symbols that do not exist in our pinned versions:

| Commit | Missing symbol |
|---|---|
| `3ac7f1c4` | `catwalk.InferenceProviderBaseten` |
| `aca40878` | `catwalk.InferenceProviderBaseten` |
| `d341d84b` | `catwalk.InferenceProviderAlibabaUS` (also needs `ebf6e826`, batch 7) |
| `d0dc9fc9` | go.mod/go.sum only — it *is* a dep bump |
| `64bbbebc` | `fantasy.OnAuthRefresh` |
| `8db57337` | `fantasy.IsTransportError`, `fantasy.NewTransportError` |

Batch 16 is therefore promoted: run the bump, then replay these six, then `63dc1f01`.

### Adaptations worth remembering

- **`8ccf6945`** — upstream keeps `copilotResponsesModels` in `coordinator.go`; ours lives in
  `coordinator_providers.go`. Git offered to insert a whole duplicate map into `coordinator.go`;
  the correct resolution was to drop the block and add only the three new model IDs to the
  existing map. Watch for this whenever upstream touches those two maps.
- **`a882695e`** — took only the OpenCodeGo/Zen case. The same conflict hunk also carried Baseten
  and Alibaba-US branches from commits we cannot apply yet.
- **`8db57337`** — the code change is version-independent, but `go.mod`/`go.sum` conflicted; we
  keep our pins and let batch 16 move versions. Blocked regardless (see table).

### The dependency bump

Six commits needed symbols absent from our pins, so batch 16 was promoted and run here:

- `catwalk v0.44.28` -> `v0.51.6` (adds `InferenceProviderBaseten`, `InferenceProviderAlibabaUS`,
  `InferenceProviderFireworks`)
- `fantasy v0.31.1` -> `v0.40.0` (adds `OnAuthRefresh`, `ModelProvider`, `IsTransportError`,
  `NewTransportError`)

The fantasy bump forced a second change: fantasy now uses `github.com/openai/openai-go/v3`, while
we still imported the `github.com/charmbracelet/openai-go` fork, and the two `option.RequestOption`
types are not interchangeable. Swapping the import in `coordinator_providers.go` folds in the
intent of upstream `14483cac` ("switch back to upstream openai sdk"), so that commit needs no
separate pick. `d0dc9fc9` ("prepare for gpt-5.6") is likewise pure go.mod/go.sum and is subsumed.

Both bumps were committed separately (`fc87244f`, `565e439a`) with a full `go test ./...` between
them, so a regression can be bisected to one dependency.

### Not applied

| Commit | Disposition | Reason |
|---|---|---|
| `d341d84b` | not applicable | Edits Alibaba branches in `getProviderOptions`. Our fork has no Alibaba handling at all (`grep -r Alibaba internal/` is empty); it arrives with `ebf6e826`, batch 7. Re-evaluate there. |
| `d0dc9fc9` | absorbed | go.mod/go.sum only; covered by the bump above. |

### Adaptations worth remembering

- **`8ccf6945`** — upstream keeps `copilotResponsesModels` in `coordinator.go`; ours lives in
  `coordinator_providers.go`. Git offered to insert a whole duplicate map into `coordinator.go`;
  the correct resolution was to drop the block and add only the three new model IDs to the
  existing map. Watch for this whenever upstream touches those two maps.
- **`a882695e`** — took only the OpenCodeGo/Zen case; the same hunk carried Baseten and
  Alibaba-US branches that did not apply at the time.
- **`3ac7f1c4` + `aca40878`** — applied by hand as a single squashed change after the bump. Both
  touch the same three lines and their conflict hunks were tangled with batch-7 content
  (Fireworks, Alibaba, the auto-discovery `default:` branch), so replaying them as cherry-picks
  produced more conflict than signal.
- **`64bbbebc`** — only the auth-refresh mechanism was taken. `internal/config/store.go`
  (`WaitForTokenChange` / `SignalAuthComplete`), `internal/oauth/token.go` (`TokenExchangeError`),
  `hyper/device.go`, and `app_workspace.go` applied cleanly. `agent.go` and `coordinator.go` were
  reverted and hand-edited, because upstream's version is interleaved with client/server plumbing
  (`RunID`, `OnComplete`, `Accepted`, `acceptSeq`, `notify.RunComplete`) and a different
  skill-tracking shape. Added: the `OnAuthRefresh` field on `SessionAgentCall`, `OnAuthRefresh` +
  `ModelProvider` on the stream call, `makeAuthRefreshCallback`, and wiring at both `Run` sites.
  Upstream's two debug `slog.Info` calls ("ModelProvider called", "Agent stream returned error")
  were left out as clear debugging leftovers.
- **`8db57337`** — code applied cleanly once the dep bump landed; only go.mod/go.sum conflicted
  and we keep our own pins.

## Batch 4 — Performance

**Status: done.** 9 cherry-picked, 2 deferred, 2 skipped.

```
3295a085 fix: cache streaming thinking renders to avoid CPU burn during long reasoning traces
884391f9 fix: stop long thinking blocks from re-rendering the entire document every frame (#3454)
1bfc53f6 perf(ui): memoize the chroma syntax-highlight style
d29fc2ca perf(ui): memoize chroma lexer lookups by filename
4d901d1b perf(ui): keep chat resize smooth on large conversations
e4175c52 perf(ui): avoid quadratic shell output rendering (#3381)
1b4ef73f fix(ui): keep shell progress output from corrupting the TUI
bd232eab perf(tui): keep synchronous workspace probes off the per-message Update path
d1626158 fix(tui): prevent stale background refreshes from overwriting UI state
173b2be6 perf(ui): skip theme rebuild when provider keeps the same theme
f5b996bf perf(config): make model selection and config reload fast
81cb9d99 feat(ui): optimize model ui rendering
cc971bd6 perf(lsp): filter servers before searching $PATH (#3370)
```

### Not applied

| Commit | Disposition | Reason |
|---|---|---|
| `e4175c52` | deferred to batch 8 | Rewrites `internal/ui/chat/shell.go`, which is bang-mode UI and does not exist locally. |
| `1b4ef73f` | deferred to batch 8 | Depends on bang mode's `RemapANSI16`; the `StripCursorControl` half only has meaning alongside it. |
| `bd232eab` | skipped | Doubly entangled: bang-mode state everywhere, and upstream's boolean yolo (`PermissionSkipRequests`/`toggleYoloMode`) vs our granular `YoloLevel`/`cycleYoloLevel` from main's permission system. The motivation — workspace probes being expensive — is a client/server (TCP) concern; our `AppWorkspace` probes are in-process method calls. |
| `d1626158` | skipped | Follow-up to `bd232eab` (fixes its `workspace_cache.go`); moot without it. |

### Adaptations worth remembering

Local commit mapping for the adapted picks (the review round noted that
`202b3b66`/`3ea4c5c0` lack adaptation notes in their commit bodies; the
mapping lives here instead of rewriting history):

| Upstream | Local commit | Adapted? |
|---|---|---|
| `3295a085` | `202b3b66` | yes — innerWidth + label/italics rewoven |
| `884391f9` | `3ea4c5c0` | yes — same renderThinking reweave |
| `4d901d1b` | `73f3f0bc` | yes — noted in commit body |
| `173b2be6` | `9f090fb0` | yes — noted in commit body |
| `f5b996bf` | `85dcb4dd` | yes — noted in commit body |

- **`3295a085`/`884391f9`** — local `renderThinking` computes `innerWidth` (ThinkingBox frame) and
  prefixes a styled "Thinking:" label with per-line italics; upstream has neither. Merged by
  passing `innerWidth` to the prefix cache and re-adding the label/italic pass around upstream's
  `tailLines` bounded-scan slicing. Upstream's `CHARM-1785` ticket references were replaced with
  the upstream commit SHA in a follow-up.
- **`1bfc53f6`** — the memoized style is registered as `chroma.MustNewStyle("anvil", ...)`
  (upstream says "crush").
- **`4d901d1b`** — took the incremental cache warming, dropped every scrollbar hunk (chat
  scrollbar is batch 12; `Chat.Draw`/`SetSize` keep our drawCache and follow-anchor logic). The
  review round then deleted the `resizing` flag (its only consumer was the dropped scrollbar
  gate) and made `chatWarmMsg` carry its owning `*Chat` so drill-in/out cannot strand a warm
  chain. If batch 12 is ever picked it must re-add the `resizing` flag and the `!m.resizing`
  Draw-side suppression, and `list.Overflows` (kept, tested, currently caller-less) is ready for
  it.
- **`173b2be6`** — fork is single-theme, so instead of porting the theme-key machinery the
  redundant `applyTheme(styles.TokyoNight())` on model selection was deleted outright — same win
  (no transcript re-render on model switch), no new state.
- **`f5b996bf`** — store.go changes were superseded by `b10f890f` (its upstream successor, picked
  in batch 2); resolved every store conflict to HEAD. Took the genuinely new pieces: memoized
  powernap LSP defaults, per-directory worktree-root cache, shell expansion fast path, and the
  model-selection benchmark (whose fixture needed the usual `anvil.json`/`ANVIL_GLOBAL_*` rename
  — third instance of that trap).

## Batch 5 — UI fixes

```
ac4bd9c1 feat(scrolling): move to delta coalescing filter (#3135)
72069811 fix(scrolling): stale scrolling acceleration (#3197)
3a99992e fix(ui): ansi.truncate instead of byte slicing in todos
9c61c7d0 fix(ui): use ansi.truncate everywhere
13501672 fix: render pills box reliably when todos or queue appear mid-session
6437f0d7 fix(ui): make chat follow-scroll reliable when content grows (#3240)
6d272018 fix: keep chat pinned to bottom after a resize (#3336)
0d4c2bbc fix: keep the spinner animating when a response restarts
80ce5834 fix(ui): prevent double spinner on session reload after kill (#3457)
74e725b3 fix(ui): resolve timer display conflicts in thinking animation and tool spinners (#3353)
c880f929 fix(ui): use fgmostsubtle for canceled text (#3360)
1ef42ef8 fix(queue): fix pill border on queued messages (#3333)
cfad8143 fix(dialog): add clear alias to summarize (#3464)
1134a5dd fix(dialog): raise permission-guard quiet period to survive natural typing pauses (#3393)
b109e35f refactor(ui): route all agent-model rebuilds through one helper
```

## Batch 6 — MCP fixes

```
132a8c89 fix(mcp): clear tools and close the session on MCP error; reap stdio process groups
8246dff5 fix(mcp): wait for MCP init before building the tool list
009ce621 fix(mcp): serialize renewals, restore all registries, arm init gate
bb3d4495 fix: reduce default and suggested mcp timeout (#3509)
bdad0f11 feat(mcp): don't hold callback port open permanently (#3481)
```

## Batch 7 — Model auto-discovery + enrichers (optional)

```
73031584 feat: auto-discover models from openai-compat providers
0f279057 feat(enricher): add litellm enricher
9299ad47 feat(enricher): add ollama enricher
89b680d2 feat(enricher): add omlx enricher
3c4e6547 feat(enricher): add lmstudio enricher
c1a48226 feat(providers): add llamacpp enricher
cfdca358 fix: correct model discovery enrichment for local providers
d1489a60 feat: wire LM Studio vision capabilities to SupportsImages (#3280)
9886d223 feat: add support for fireworks provider
ebf6e826 chore: add alibaba us (#3249)
```

## Batch 8 — Bang mode (optional)

Also pick `a9e3a57f` (deferred from batch 1) after `99a5fad5`, since it depends on
`shell.PersistFunc` and `message.ShellCommand`.

```
99a5fad5 feat: add bang mode for direct shell command execution (#3013)
03bfdc66 fix(bang): set title in bang mode
6db54b3a feat(bang): show pending spinner
6814ccfc feat(bang): stream results in
1c2da893 feat(bang): remap ansi 16 colors
47ed0f3a feat(bangmode): cancel command execution
f46387bb feat(bangmode): copy message result
175ce34c feat(bangmode): properly strip ansi in copy and context
ce2dc1ee chore(bangmode): adjust ANSI16 colors
2f89862d chore(bangmode): adjust working indicator text color
5f07034e feat(bangmode): adjust command output color
48e9cca2 fix(bangmode): interleave stderr and stdout
8305eb03 fix(bangmode): fix duplicate command message race condition
cf0d7c2f fix(bangmode): move ansi remaping into common and cleanup bangmode intalization
ee47eb80 feat(bangmode): allow prefixing a string with bangmode
11662010 fix(bangmode): activate bang mode when ! is preceded by whitespace
7e4bd6a0 fix(bangmode): engage bang mode when pasting text starting with !
f342edf0 fix(bangmode): include bang commands in history
d20e29ae fix(bangmode): sync bang mode with external editor
cb129202 fix(bangmode): don't add extra ! when browsing history
```

## Batch 9 — Question tool (optional)

Skip `75e7195f` (client/server integration). `492460a8` (coordinator struct refactor) is a
prerequisite and touches local coordinator code — expect conflicts.

```
492460a8 refactor: make the coordinator use a struct
c2a6f765 feat(question): add question tool with structured UI
321c661e feat(question): add mouse support
1b5994c0 feat(question): add paste support in text areas
cbb9d4f2 fix(question): fix scrollbar disappearing in single-select
81a8ee4b feat(question): redo tab resizing and mouse -> keyboard transition
9f4f145a feat(question): adjust question prompts and error messages
6f33b666 feat(question): add mouse scrolling
70c64b57 fix(question): route scrolling properly depending on which area is hovered
3bf40358 feat(question): make escape cancel the question instead of submitting empty answers
f69a91e5 feat(question): extend length limits on question tool
82e88985 fix(question): address PR review feedback
9e6175a2 fix(question): make scroll containers consistent
ca5a19d7 fix(question): fix a bug with the hover state coming deselected
5e611a70 feat(question): allow newlines and make free text like pop
a5a5c6c5 feat(question): tweak the confirmation tab
29f5691d feat(question): tweak yes/no to add shortcuts
1937ac54 fix: match question tool cursor color to main editor
```

## Batch 10 — Dialog responsive sizing (optional)

```
3e2bd1cc fix(dialog): make OAuth dialog width responsive to terminal size
151b38d4 fix(dialog): make APIKeyInput width responsive to terminal size
f6528f72 fix(dialog): constrain Quit dialog padding on narrow screens
6407b852 fix(dialog): scale FilePicker down on small screens
31550cb4 fix(dialog): fix OAuth content wrapping and full-width layout
2b4a01ae fix(dialog): truncate dialog titles instead of wrapping on small screens
e271cecb fix(dialog): subtract border size in Reasoning and Notifications width
e8c2772d fix(dialog): truncate TitleInfo when it exceeds dialog width
d6c63d31 fix(dialog): clamp DrawCenter and DrawOnboarding content to screen bounds
bbf654fb fix(dialog): centralize keybind hint rendering and stop overflow
aaf92f86 fix(dialog): mute command list shortcut hints
a69ace5e fix(dialog): keep dialogs stable on small screens
0c61aeee fix(dialog): stop list item names from wrapping past the list width
686a3f46 feat(dialog): hide list info column when it would crowd item names
605dfc9e fix(dialog): remove dead space beside the list scrollbar
f3592bd8 fix(dialog): account for the input prompt width so long values don't wrap
352e2651 fix(dialog): fix model provider label truncation at low widths
e456a902 refactor(dialog): dedupe scrollbar joins and tame the permission Draw
2c6424c1 fix(dialog): center permission buttons in fullscreen
```

## Batch 11 — Scrollable sidebar (optional)

```
f8d855d5 feat(ui): add scrollable sidebar with focus-based navigation
d5007646 feat(ui): widen sidebar to 32 cells and reserve scrollbar column
5d9d37bc fix(ui): prevent sidebar text clipping and restore sidebar focus
c5bf189c fix(ui): leave an empty column between sidebar content and scrollbar
4b729a93 fix(ui): only allow sidebar focus when content overflows
4244f520 feat(ui): scroll sidebar with mouse wheel when focused
04799876 feat(ui): scroll sidebar with mouse wheel when focused
79eedf79 feat(ui): show g/G shortcuts in sidebar full help
885fe4d3 fix(ui): keep sidebar layout while scrolling full content
c3b32034 fix(ui): scroll sidebar details below the logo
5b7d7f71 fix(ui): align the compact sidebar logo
efbe3083 refactor(ui): move sidebar scroll state mutation out of draw function
```

## Batch 12 — Chat scrollbar (optional)

```
b72f9aab feat(ui): add scrollbar to chat view (#3018)
ff12fa01 fix(scrollbar): only show on human scroll
```

## Batch 13 — Tool call expansion UI (optional)

```
db8add71 feat(ui): allow expanding toolcall names
cbb6daaa feat(ui): preserve newlines in expanded tool content (#3239)
```

## Batch 14 — Attachment chip remove button (optional)

Touches `attachments.NewRenderer` — remember the 5-arg local signature.

```
f604d989 feat(ui): clickable ✕ remove button on attachment chips
4413344c fix(ui): keep chips in place when toggling attachment delete-mode
15e73964 fix(ui): restore right padding on the attachment remove button
1be84086 Review fixes: attachment remove button (hidden clicks, mouse button, chip spacing)
```

## Batch 15 — Misc features (optional)

```
dc160997 feat: elapsed seconds timer (#3223)          # verify against local impl first
f413c9a9 feat(ui): auto expand reasoning dialog based on count (#3332)
3446255d feat: add --all and --crawl-dir modes to stats subcommand (#2811)
a02a9809 feat: on quit dialog, add hint on how to skip confirmation (#3356)
ebd845c0 feat(agent): send session hash in header for cache affinity
6b6fab5e refactor: simplify edit tools and enforce Sourcegraph result limits
4f4b8469 fix(refactor): refactor the edit tool to not duplicate find and replace
b679ba1e fix(edit): auto-correct whitespace mismatches in edit tool
de671233 fix(tools): surface DuckDuckGo rate limiting instead of empty results
3c61d9a3 test(tools): cover DuckDuckGo rate-limit detection
ad709357 fix: resolve short session IDs in local run --session (#3460)
3a71bdf3 fix(noninteractive): don't block non interactive mode on session title gen
155072b3 feat(noninteractive): make the spinner text use terminal default rendering
5e1cd7ef fix(fable): surface model refusals in the TUI instead of silently stopping work
e34e707c fix(events): restore file picker event (#3314)
677046d2 fix(illumos): support building and running (#3422)
42f3ce0c fix(lsp): handle window/workDoneProgress/create to prevent server crash (#3445)
5e6890de fix(tests): bail on precancelled ctx in handleJQ (#3058)
2a230d01 fix(commandPalette): merge PR #3397
12e96776 feat: add keybinding to copy verification URL in OAuth dialog (#3324)
b75d6bc2 fix(oauth): fix the ui freeze on loading model endpoints
1c074540 feat(oauth): give the browser redirect a real landing page
7810d038 feat(config): apply top-level env vars on startup
9e3ed61c feat(bedrock): retry the turn automatically after AWS SSO re-auth
810168b0 docs: document aws_auth_refresh and top-level env config
f6a841ec feat(dialog): track last closed dialog in open with grace
```

---

## Review round (after batches 1–3 + main merge)

A three-reviewer pass (2026-08-10) found and fixed:

- **Auth-signal double-close panic** — taking `64bbbebc` while deferring `63dc1f01` shipped a bug
  whose fix lived in the deferred commit: `SignalAuthComplete` panicked on a second signal with no
  intervening waiter, reachable via `SetProviderAPIKey`. Fixed with `63dc1f01`'s exact shape and
  pinned by `internal/config/auth_signal_test.go`. **Lesson: when deferring a commit, check whether
  it repairs one already taken.**
- **Publish-then-mutate race** — `reloadFromDiskLocked` wrote `cfg.Models` (a plain map) after
  `setConfig` published the config. Model resolution now happens before publish, which also
  removed the setup-failure rollback.
- **`dispatchShebang`** — the second call site of the batch-1 cancellation bug: it SIGKILLed only
  the direct child (orphaning grandchildren under Setsid) and never surfaced `ctx.Err()`. Now
  routes through `processGroupExecHandler`; regression tests in
  `internal/shell/dispatch_shebang_cancel_test.go`.
- **Non-atomic / unserialised config writes** — `RemoveConfigField` (now via `atomicWrite`),
  `SetPermissionRule` (now via `atomicWriteFile`), and a bare `s.knownProviders` read in
  `SetProviderAPIKey` (now via the guarded accessor).
- **`scopeb_race_test.go` was silently weakened** — `crush.json` fixture + `CRUSH_GLOBAL_*` env
  vars meant it loaded an empty config; exactly the fixture-name trap this doc warns about, in a
  test that shipped with batch 2.
- Branding sweep (11 comments across 8 files), stale `mu`/`Overrides` docs, dead
  `sessionID+"-summarize"` lookup in `Cancel`.

Still open (accepted): `applyToken`/`refreshOAuthTokenLocked` mutate the live config's
`Providers` csync.Map rather than cloning — thread-safe but bends the copy-on-write invariant;
revisit if provider state ever moves out of `csync.Map`. Commit message of `9042e486` retains the
upstream `/etc/crush/crush.json` title; code was fixed in `f80fcc35`.

---

## Explicitly skipped

| Group | ~Count | Reason |
|---|---|---|
| crushrc / Bash shell config | 55 | Replaces `anvil.json` with a Bash DSL. Invasive; conflicts with local config service. Revisit as a standalone project if wanted. |
| Client/server/workspace/api | 35 | Feature we don't use (per sync policy). |
| MCP channels (`--channels`) | 11 | Coupled to client/server; hidden experimental flag. |
| LSP superpower tools | 8 | `17ad5c7f cd8c06ce 8ad59d22 b9a1182b 3b9f0193 7eca7a04 07044a9b 70cd684a` — competing implementation, local `lsp_*` tools already exist. |
| Upstream MCP OAuth feature | 14 | `67e748e1 4fb12526 37fc995d 1588ae86 7946b21e 26399fc2 205b1b35 1ba4dc4b d338af88 2ca5a8c3 c3fd60ea e68041a6 533fcf1e fed86b00` — local implementation already exists. Cherry-pick individual bug fixes only if they map cleanly. |
| herdr socket | 1 | `b39a85ec` — Charm-internal telemetry. |
| Clipboard migration | 2 | `98d79e95 4bd01166` — new cgo-ish dep (`golang.design/x/clipboard`); we build `CGO_ENABLED=0`. Revisit only if clipboard is broken. |
| fantasy/catwalk dep bumps | ~8 | Do one deliberate bump at the end (batch 16) instead of picking each. |
| Charm docs/readme/golden regen | ~15 | Crush-branded or noise. |
