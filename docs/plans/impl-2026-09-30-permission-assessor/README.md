# Permission Assessor Implementation Plan

> **Status:** DRAFT

## Overview

**Problem:** Every tool call that no explicit permission rule covers (or that
hits an `ask` rule) blocks on a human prompt. Over the last 30 days Anvil made
~1,640 tool calls/day (49,142 total; `bash` 50%, `view` 22%, Muninn 9%, edits
6%), so the human is the bottleneck for long agentic runs. The only escape
hatch today is yolo mode, which is all-or-nothing. There is also no record of
*who* decided each call, so there is no data-driven way to grow the explicit
rule set.

**Goal:** A layered permission pipeline:

1. Explicit `allow`/`deny` rules decide first (unchanged).
2. Calls with no rule, or an `ask` rule, go to "someone":
   - the **assessor** (a System One classifier, `von-1.0.0` on Baseten, API
     compatible with TypeSafe Jev) when enabled, which allows, escalates to
     the human, or denies;
   - otherwise the human, as today.
3. Every decision is logged. A **triage** command periodically mines the log
   for frequently repeated, unresolved calls that a simple explicit rule
   could cover, and proposes those rules for one-keystroke approval. Over
   time explicit rules absorb the common cases, reducing classifier calls,
   latency, and human prompts.

The assessor ships in `shadow` mode first (assess and log, but the human
still decides) so its verdicts can be compared against real human decisions
before it is trusted to act.

This is phased because it touches four independent review domains: the
persistence layer (schema + recorder), the permission core and an HTTP
classifier client, a CLI analytics command, and the TUI.

## Phases

| # | File | Delivers | Depends on | Review focus |
|---|------|----------|------------|--------------|
| 1 | `phase-1-decision-log.md` | `permission_decisions` table, async recorder, every `Request` outcome logged with its source | — | Schema, index choice, recorder back-pressure, no behaviour change |
| 2 | `phase-2-assessor.md` | `internal/assessor` (von client, state builder, question battery, router), global-only config, `Request` integration with `off`/`shadow`/`enforce` modes, live calibration harness | Phase 1 | Fail-closed-to-human semantics, lock restructuring, data sent off-machine, threshold defaults |
| 3 | `phase-3-triage.md` (parallel with 2) | `anvil permissions triage` and `anvil permissions stats` | Phase 1 | Candidate-pattern safety filters, never proposing over-broad rules |
| 4 | `phase-4-ui.md` | Assessor note in the permission dialog, runtime mode toggle, status indicator, triage nudge | Phases 2 and 3 | UI conventions per `internal/ui/AGENTS.md` |

> Phase 3 can be developed and merged before or after phase 2. Landing it
> first is recommended: it cuts prompts using only the human decisions
> phase 1 logs, with no classifier. It reads assessor data from the log
> when present and degrades gracefully when not.

> **Volume caveat:** ~1,640/day is *tool calls*, not permission requests.
> In-workspace `view`/`grep`/`glob` and bash `safeCommands` never reach
> `Request`. `anvil permissions stats` reports the real request volume
> once phase 1 has been collecting for a few days, and that number is
> what drives classifier cost.

## Phase Boundaries

- **1 → 2:** The log must exist before shadow mode has anywhere to write
  assessments. Phase 1 is also independently useful: it starts collecting
  human decisions for triage immediately.
- **1 → 3:** Triage only reads the log. It does not need the assessor.
- **2, 3 → 4:** The UI surfaces assessor state (phase 2) and nudges towards
  triage (phase 3).

## Cross-Cutting Design Decisions

- **Name:** "assessor" is the working name in code (`internal/assessor`,
  `permission_assessor` config key, `DecisionSourceAssessor`).
- **Classifier API:** `POST <url>` with `Authorization: Api-Key <key>` and a
  TypeSafe-compatible body (`model`, `state`, `questions` of type
  `noul`/`choice`/`score`). The auth scheme is configurable so the same
  client can target TypeSafe directly (`Bearer`).
- **No company URLs in the repo.** The Baseten deployment URL is configured
  by the user in global config; tests use env vars. The key is read from an
  env var and never stored in config or logs. The variable name is
  configurable via the optional `permission_assessor.api_key_env` and
  defaults to `BASETEN_API_KEY`.
- **Global-only config, with frozen trust.** `permission_assessor` is
  honoured only from user-level config files (`/etc/anvil/anvil.json`,
  `ANVIL_GLOBAL_CONFIG`, `ANVIL_GLOBAL_DATA`). Those paths and the API key
  are captured **before** project `env` is applied (`applyEnv` calls
  `os.Setenv`), and reused on reload. A cloned repo can't enable the
  assessor, loosen its thresholds, redirect the trusted config path, or
  swap the key.
- **Deterministic checks before any paid call.** Code sends the call
  straight to the human, with no network call, when:
  - the tool isn't on the eligible list, or its operation is opaque (MCP
    with no args, oversized args);
  - the target is a protected path (`.git/`, `anvil.json`, shell rc,
    `~/.ssh`, ...);
  - the payload is too large (heredocs, inline scripts);
  - symlink resolution fails;
  - the outage breaker is open.

  Banned bash commands are rejected before the permission request
  entirely.
- **Explicit rules win at every commit point.** Rules are re-evaluated
  after the classifier returns and again after acquiring the prompt slot.
  A deny added mid-flight beats an assessor allow.
- **Fail to the human, not open.** Timeouts, HTTP errors, parse errors, and
  a missing key all fall through to the human prompt. The assessor can only
  ever *remove* a prompt when it is confident, never add risk silently.
- **What leaves the machine.** It's sent to the configured endpoint only:
  - tool name and action;
  - bounded, redacted command segments, URL, target path, or MCP
    arguments;
  - a new-content excerpt for in-repo edits;
  - optionally, the last 3 user messages from the active branch
    (`send_user_messages`, default true).

  Tool output is never sent. Redaction is best-effort.
- **Code computes facts, the model judges meaning.** Paths inside/outside the
  working dir, extracted hosts, segment counts, redirects, etc. are computed
  in Go and passed as fields. Arithmetic and counting never go to the model.
- **Calibration is unproven.** The sample `von-1.0.0` response returned
  near-uniform choice probabilities (confidence 0.078) and a 0.44 noul on an
  obvious question. Defaults are therefore conservative, `shadow` is the
  recommended starting mode, and phase 3 ships a stats command that reports
  assessor-vs-human agreement from the log before anyone switches to
  `enforce`.
- **Triage proposes narrow rules only.** Tier A comes from a curated
  table of command families that are safe for any arguments (e.g.
  `git status *`, `go test *`, `gh pr view *`). Everything else, including
  MCP tools, is Tier B: shown with a warning and never applied by `--yes`.
  Every candidate is validated against *all* logged evidence its pattern
  would match, not just its group, and simulated before writing.
- **Timestamps in seconds.** `messages.created_at` is actually stored in Unix
  seconds despite its schema comment. The new table uses seconds explicitly
  and says so.

<!-- Review notes (devils-advocate, 2026-09-30). Findings incorporated:
- Triage generalisation was unsafe (`gh pr view` -> `gh pr *` covers merge;
  `find *`/`sed *`/`rg *` have destructive flags). Replaced the blacklist
  with a curated Tier A family table, a Tier B manual tier, and full-match
  validation plus simulation.
- The assessor couldn't see MCP args or edit contents. Added per-tool
  eligibility, `Content`/`ArgsJSON` request fields, and deterministic
  escalation for opaque operations.
- Only allow was re-checked after waits. Added two commit boundaries that
  honour deny, cancellation checks, and channel-barrier tests.
- Project `env` could redirect `ANVIL_GLOBAL_CONFIG`. Trusted paths and the
  key are now frozen before `applyEnv` and reused on reload.
- Cleanup funcs run concurrently, so the recorder flush now happens inside
  the DB-release cleanup. `Record` is safe after `Close`.
- Intent used DESC-ordered, cross-branch messages. It now uses the active
  branch tail, ordered correctly.
- Redirect detection and path facts were wrong. `segment.IsRedirect` is now
  exported, the target path is separated from the grant dir, and unknown
  symlink resolution escalates.
- Malformed or out-of-range answers could route to allow. Full response
  validation added, plus threshold consistency checks.
- Payload bounds, redaction, and opt-out for user messages added.
- Calibration could pass with all errors. There's now a 95% valid-response
  gate, stats split into shadow/enforce/error sections, and a versioned
  AssessmentRecord in phase 1.
- Mode off now still wires the assessor, so the phase 4 toggle works.
- The nudge query moved below the UI boundary, and the fake-update list
  now covers the agent tests.
- Added: breaker, bounded concurrency, per-session allow cache, bash
  banned-command pre-check, deep-copied rule snapshots, cleared
  activeRequest on cancel, and the tool-call vs permission-request volume
  caveat.
-->
