# Upstream Sync Triage — v0.89.0 → v0.97.1

**Range**: `feb63006e9452be370721c22a0c2a3be008fd475..upstream/main` (`140e8cb97`, 2026-10-06)
**Total**: 272 non-merge commits, 180 after filtering (CLA / auto-update / version bumps)
**Started**: 2026-10-08
**Branch**: `docs/upstream-sync-2026-10`
**Previous sync**: `plans/completed/upstream-sync-2026-08-10.md` (rules and local divergences still apply)

None of these commits are in Anvil yet (checked with `git cherry` and our
`(cherry-pick <hash>)` tags).

## How this document works

Each item is discussed on its own, and the decision is recorded here.

Decision values: `pending` / `want` / `want (partial)` / `skip` / `defer`.

Once every item has a decision, an implementation plan (batch order, worktree,
adaptations) goes in the **Plan** section at the bottom.

---

## A. Agent and tool correctness

| ID | Commits | Summary | Recommendation | Decision |
|---|---|---|---|---|
| A1 | `63829edf` | Always emit tool results right after their calls. Without this, sessions get permanently stuck on strict-adjacency providers (Kimi, DeepSeek). | Take | **done** (`7ae9d90b`). Conflict: replaced our `filterOrphanedToolResults` / `syntheticToolResultsForOrphanedCalls` with upstream's `toolResultsForCalls`. Tests now pass the local 3-arg `sessions.Create`. |
| A2 | `80ebee41` | Fix reasoning effort being dropped. | Take | **done** (`5f9c7695`). Hand-applied, since upstream sits on top of A3. The test uses an explicit provider list because `discover.RegisteredProviderTypes` came from a skipped schema commit. |
| A3 | `8e5b708d` | Keep `top_k` for known custom providers. | Take | **done** (`42f9a4ad`). Merged on top of A2's reasoning-effort block; kept our sub-agent `Run` (upstream's has a Hyper notify branch). `callTopK` also added to the local-only `RunWake` call site. |
| A4 | `a98394ff` | Count Anthropic cache-creation tokens. `agent.go` already uses `CacheCreationTokens` in places, so check for overlap. | Take after checking | **done** (`16b42899`). Clean pick. No local overlap: the context counter left out cache writes, so Claude context usage was undercounted and auto-compaction could trigger late. |
| A5 | `b8e93d11`, `afb55f03` | Truncated bash / `job_output` output is written to a file so the agent can read it back. | Take | **done** (`10ea00da`). Hand-ported. Copied only the spill part of bang mode's `truncate.go` into `internal/shell/spill.go` (`SpillOutput`, `SpillDir`, 7-day / 256MB pruning); the bang-mode persist path stays out. Spills go to `GlobalDataDir()/shell-output`. Wired through bash (foreground, fast-fail and the auto-background preview) and `job_output` (live and archived jobs, via `JobToolOptions.SpillDir`). Added a `trustedReadDirs` param to `NewViewTool` so the agent can read spill files without a permission prompt, keeping normal read limits (unlike skill paths). Updated the bash/job_output descriptions and the 13 VCR cassettes. `afb55f03` (cassette regen) N/A. Tested end to end with `anvil run`: the agent read lines back from a 20,000-line spill. |
| A6 | `945c518f`, `059ba81f` | Security fixes to the bash safe-command matcher. | Take | **done, own fix** (`fbe733b7`). Upstream reverted both commits along with the parser rewrite they fixed (`d73a6e42`), so there was nothing to pick. Took option 3: the fast path now parses the command with `segment.SimpleCommand` (new; quotes and escapes resolved, so `"--output=x"` / `--out\put` can't hide a flag) and only accepts one simple command with static words. It refuses redirection, pipes, chaining, backgrounding, command substitution and newlines. Removed `env`, `nice`, `nohup`, `time`, `timeout`, `kill`, `killall`, `git ls-remote`. Per-command argument checks: git refuses `--output`/`--ext-diff`/`--textconv` and glob/brace chars; branch/tag only list (short flags checked per character); remote only bare, `-v`, `get-url`, `show -n`; grep refuses `-O`; date only formats and display flags; hostname and Windows `ipconfig` are read-only. Everything else goes to the permission rules and the Bouncer. Re-ran the probe: every bypass now prompts and left no side effects. **Follow-up fixed** (`93cf10cd`): the rules engine allowed `LD_PRELOAD=/tmp/x.so ls`, `GIT_EXTERNAL_DIFF=/tmp/x git diff`, `export LD_PRELOAD=...; ls` and `PATH=/x; ls`, because `segment.Split` dropped assignments and declarations. It now also emits each command with its assignments attached, emits `export`/`declare`/`local`/`readonly`/`typeset` and pure assignments as segments, and attaches the assignments a wrapper (`env`, `sudo`, `timeout`) skipped over. Evaluation is worst-outcome-first, so this is stricter only. `Generalize` suggests rules that keep the assignment verbatim (`GOOS=linux go build *`). `NAME=*` would be unsafe because `*` also matches spaces (fixed in `a3495565`). Side effect: env-prefixed commands such as `CGO_ENABLED=0 go build` now ask unless a rule names the variable. |
| A7 | `6f670995` | `~` shortening and expansion respect path boundaries. | Take | **done** (`a46d8746`). Clean pick. Tests pass for `home` and its callers (`fsext`, `plugin`, `agent/prompt`, `lsp`). `GOOS=windows go vet` is clean for the Windows test. |
| A8 | `be3d0796`, `bd867448` | Memory leak: pending state kept two snapshots of every streamed message. | Take | **done** (`b3ffc210`, `253c3b57`, `1bfcd7b8`). We already had part of this: `0de382b5` deletes the pending entry after a terminal flush. Upstream's baseline projection and dropping `latest` once clean still help mid-stream. Conflict in `message_test.go`: kept our `TestPendingState_PrunedAfterFinish`, and changed upstream's `TestPendingStateDoesNotRetainPayload` to use a non-terminal flush (a finished message gets pruned here, so there is no entry to inspect). Reworded upstream's comment to match our pruning. Checked by mutation that the test fails without the fix. **Local bug found** by `bd867448`'s negative test, fixed in `253c3b57`: `0de382b5` pruned after *any* structural flush (tool call added or finished, reasoning ended), not only when the message finished. That threw away the baseline, so every later delta in the same message flushed to SQL synchronously and went through must-deliver publish: debouncing was off for the rest of every turn after its first tool call or reasoning block. Now we prune only when `snap.IsFinished()`. Tested end to end with `anvil run` (two tool calls); every saved message has its final parts and a finish part. |

## B. MCP

| ID | Commits | Summary | Recommendation | Decision |
|---|---|---|---|---|
| B1 | `cce88718` | Fix stdio startup diagnostics. `maybeStdioErr` duplicates argv0 on the diagnostic re-run. | Take | **done, partial** (`734a0845`). Hand-applied the argv0 half (`stdioCheck` drops `Args[0]`) with upstream's `TestStdioCheck_DoesNotDuplicateArgv0`, plus a local `TestMaybeStdioErr_SurfacesChildOutput` on a bare `CommandTransport`. Left out the transport-unwrap half (`transportWrapper` / `unwrapTransport`): it exists only to see through `channelTransport` from Claude Channels (F17), and we don't wrap transports. Revisit if F17 or any other transport decorator lands. Both tests fail without the fix. Tested end to end with `anvil run` and a stdio MCP that writes to stderr and exits 3. Before: `EOF` / `exit status 126: /bin/sh: /bin/sh: cannot execute binary file`. After: `EOF` / `exit status 3: B1-REAL-STARTUP-ERROR: missing config`. Note: a server that writes non-JSON to stdout fails with a JSON parse error, not EOF, so it never reaches this path (unchanged upstream too). |
| B2 | `1ea27147` | Scope error teardown to the session that errored, and serialize refreshers, so healthy replacement sessions survive. | Take | pending |
| B3 | `8b1d542c` | Keep sessions alive when a server doesn't support ping. | Take | pending |
| B4 | `48768f8b`, `c78cc674`, `1e5be92e`, `f50509d4`, `b8453c4a` | Stop MCP init from blocking interactive prompts, put a time limit on init waits, and log connect durations. Our MCPs connect lazily, so first check whether the problem happens here at all. | Investigate | pending |
| B5 | `82557e2a`, `ad901405`, `189fa9a2` | Support sessionless MCP servers (GitHub MCP), and the related SDK bumps. Only needed if we move go-sdk from v1.6.1 to v1.7.x. | Tie to the go-sdk bump | pending |

## C. Performance

| ID | Commits | Summary | Recommendation | Decision |
|---|---|---|---|---|
| C1 | `978d49d5`, `296b5c39` | Cap prompt history at 200 messages and add an index on message role (upstream measured 335ms → 3.8ms). `ListUserMessagesByWorkingDir` here has the same unbounded pattern. | Take | pending |
| C2 | `595f625f` | Decode message parts in one pass. | Take | pending |
| C3 | `fd58b5ed` | Read the session transcript off the event loop. | Take | pending |
| C4 | `9207cb2c` | Skip the global prompt-history scan while a session is loading. | Take | pending |
| C5 | `d55e017d` | Read only the part of a compacted session after its summary. Conflicts with our branch-path queries (`GetBranchPathTail`), so it needs adapting. | Adapt | pending |
| C6 | `8ce2bdfb`, `2019da14` | Drive every spinner from one shared animation clock. | Take | pending |
| C7 | `0ffab3f7`, `35b5f877` | Cache rendered frames for scroll-only updates (+1.1K LOC), and stop coalesced wheel scrolls from rewinding. Overlaps simplification phase 5 (`View()` buffer reuse). | Evaluate | pending |
| C8 | `2586b0c1`, `a2a024e7` | Faster streaming of long reasoning traces, and a bound on the markdown fallback. | Take | pending |

## D. UI fixes

| ID | Commits | Summary | Recommendation | Decision |
|---|---|---|---|---|
| D1 | `75962c48` | Fix odd indentation in code blocks. | Take | pending |
| D2 | `460a877f` | Keep first-line indentation in tool call previews. | Take | pending |
| D3 | `7b38a40e` | Keep single line breaks in submitted user messages. | Take | pending |
| D4 | `8f63ee59`, `a0fe47b6` | Wrapped messages counted as newlines; return to the start of the line. | Take | pending |
| D5 | `46469e2c`, `3429749e` | Pin the selected list item to the top; keep `AtBottom` correct when the top item is taller than the viewport. | Take | pending |
| D6 | `ef87ba3b`, `c8a9842d` | Fix copying inline code and the screen flicker it caused. | Take | pending |
| D7 | `37eb7535` | Use the correct `Quiet` field in kitty graphics options. | Take | pending |
| D8 | `aee87604` | Don't upload images to models without vision. | Take | pending |
| D9 | `cdc72006`, `074e41e1`, `833bef63` | Always show the full bash command; keep scroll position when expanding or collapsing; show content in full when collapsing would hide only one line. | Take | pending |
| D10 | `87a5bbd8`, `242b4d3a`, `e41ae8ee`, `ca6ae26c` | Spacing above the finish banner, gap under a failed edit, syntax highlighting across wrapped parameters, multi-line output under sub-agent branches. | Take | pending |
| D11 | `76cc5c57` | Stop the attachment row from claiming files that are already visible. | Take | pending |
| D12 | `b4dd6e6d` | Fix `openEditor` swallowing an empty buffer. | Take | pending |
| D13 | `4a74b9e0` | Keep the sessions list position after a deletion. | Take | pending |
| D14 | `8661a1c4` | Windows: stop modifier keys alone from scrolling up. | Take | pending |
| D15 | `051fb37a` | Colour the to-do task name while the tool is still running. | Take | pending |

## E. Dependencies

| ID | Commits | Summary | Recommendation | Decision |
|---|---|---|---|---|
| E1 | `7944b8e5`, `5be6a338`, `066f4444`, `88298324`, `72654940`, `18601566`, `06e27eb1`, `a6fecdc7` | fantasy v0.43.2 → v0.45.1 (inference fixes, tool call truncation fix, reasoning-only turns). Our catwalk is already ahead (0.52.59 vs 0.52.49). | Take | pending |
| E2 | `7dead4d4` | Switch back to Charm's openai-go fork. | Take with E1 | pending |
| E3 | `a6fecdc7`, `b0115a1b` | Bubble Tea 2.0.6 → 2.0.9, bubbles 2.1 → 2.2.1. | Take | pending |
| E4 | `1c08c35e`, `a15d4f19` | Migrate `mvdan.cc/sh/moreinterp` to `mvdan.cc/sh/x`. | Evaluate | pending |
| E5 | — | go-sdk v1.6.1 → v1.7.x (needed for B5). | Evaluate | pending |
| E6 | `01da7bc6`, `959ebd5d`, `393e5578`, `e18e3f27`, `f0de8dcf`, `63d73f7b` | Use the upstream Anthropic SDK, pin `x` packages, and the dependabot group bumps. | Take as part of the dependency pass | pending |

## F. Features to decide on

| ID | Commits | Summary | Notes | Decision |
|---|---|---|---|---|
| F1 | `1648f10a`, `eb845a9e`, `9636af64` | Plan mode | Large. Touches the server/backend/proto packages we deleted, so it needs adapting. | skip (not needed) |
| F2 | `f38b7947`, `a88b5735`, `56abdfa7`, `5f295a6f` | Theme support | +4.4K LOC. Conflicts with our TokyoNight-only styles. | skip (TokyoNight only) |
| F3 | `a9a257c4`, `1f3827bc` | Toggle MCPs on and off | Competes with our lazy MCP palette toggle. Probably skip. | pending |
| F4 | `2da6f781`, `f2d66d28`, `08984f59`, `2fbaa90b`, `f2674806`, `65865e01` | Show the git branch in the TUI | Small. `2fbaa90b` (read the branch in the prompt without spawning a shell) depends on the new `gitutil` package. The prompt still spawns a shell for `git status` and `git log`, so the saving is small. | skip (not needed, including `2fbaa90b`) |
| F5 | `b82d7d05` | Restore the last provider/model when resuming a session | Drop the proto/server hunks. | pending |
| F6 | `90b48cda`, `36a4f79a`, `ed15da4c` | Bash syntax highlighting; drop the redundant `cd` prefix | Small UX improvement. | pending |
| F7 | `08552fb9` | Text selection in the textarea | Small. | pending |
| F8 | `62de0deb` | Ctrl+B toggles the sidebar | Small. | pending |
| F9 | `25bf6a25` | Ctrl+End jumps to the bottom and follows | Small. | pending |
| F10 | `63a73bab`, `460653aa` | Configurable request timeout (default doubled to 120s) | Drop the shellconfig hunks. | pending |
| F11 | `8db8331c` | `--reasoning-effort` flag for non-interactive runs | Small. | pending |
| F12 | `563ff0f7` | Option to disable mouse support | Small. | pending |
| F13 | `90bc466d`, `e3dc6d2a`, `5c6dd666`, `04a2cd85`, `f043451d`, `6f1fd614` | Mouse support in the session picker and quit dialog | Optional. | pending |
| F14 | `804324fb`, `773c3565`, `a83480eb` | Print the session ID on exit; compact resume banner; exit banner refactor | Partly overlaps our Herdr resume work. | pending |
| F15 | `efba40b7`, `e5687e2d`, `652f74ff`, `45a45c5c`, `04eed5d4`, `094ac5db`, `616c17ba`, `3a84d5e7`, `d25b5eee`, `33be610d`, `7a63cb15`, `e2f077cd`, `bc3931c8`, `18447d5f`, `fded9d7d` | OpenAI (ChatGPT) OAuth and the unified OAuth CLI TUIs | About 2.5K LOC. Only worth it with a ChatGPT subscription. We deleted `cmd/login.go` / `logout.go`. | pending |
| F16 | `30145cbc` | Grok OAuth | About 2.5K LOC. Depends on F15's login refactor. | pending |
| F17 | `564d14e4` | Claude Channels delivery | Built around the server/SSE path. | pending |
| F18 | `bb33cee2` | Generic fan-in helpers in the event bus (refactor) | Only if it makes later picks easier. | pending |

## G. Skip: code we removed or already declined

Listed so they aren't re-triaged next time. Override any of these if wanted.

| ID | Commits | Reason | Decision |
|---|---|---|---|
| G1 | `c21c7c45`, `3297f43b`, `263a0201`, `ee76ff2f`, `5b7377be`, `03667134` | Server / swagger stack, deleted in the simplification. | skip |
| G2 | `ec2fe437`, `35a7bcab`, `b07464e9`, `65a9b8d9`, `b4e25013`, `f9746789` | Hyper / Copilot, deleted in the simplification. | skip |
| G3 | `404a93c6`, plus shellconfig hunks elsewhere | crushrc / shellconfig, declined last sync. | skip |
| G4 | `8597c9b7`, `52cbd4b6`, `4ab302dd` | Clipboard package migration. We still use `atotto` (CGO disabled). | skip |
| G5 | `10ec02a5` | Bang mode, declined last sync. | skip |
| G6 | `5e3fe4b5` | Question tool, declined last sync. | skip |
| G7 | `00b33e4b` | `stats` command, deleted. | skip |
| G8 | `7dc88e24`/`47df446e`, `f5c0b067`/`0b4dc190`, `48f4e8a4`/`3f575432` | Reverted pairs (user@host header, MindsHub docs, image glyph). | skip |
| G9 | `73d1a50a`, `37951029`, `d8285661`, `ecd4de24`, `a2786ce1`, `c824c880`, `7d78d742`, `59116792`, `40f2da58`, `50113c26`, `0f14334f`, `6ee6e5c4` | CI, meta repo, dependabot, goreleaser, VCR cassettes, toolchain. | skip |
| G10 | `06345cc3`, `599a4abc`, `463a0eb1` | Upstream docs only. | skip |
| G11 | `6fa9e690`, `db7fac6c`, `140e8cb9`, `65865e01` | Upstream test-flake fixes. Pick only the ones that touch tests we share. | pending |

---

## Plan

To be written once every item has a decision.
