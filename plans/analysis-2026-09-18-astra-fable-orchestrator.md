# Astra vs Fable as Anvil orchestrator

Analysis date: 18 September 2026. Source: the supplied local `anvil.db`, opened read-only.
Observation cutoff: 18 September 2026, 10:47:37 UTC, before this analysis session.

## Bottom line

**Astra behaves more like a coordinator; Fable behaves more like a hands-on implementer.** Astra delegates
more broadly, batches more tool calls, reads skill files far more often, and gives shorter responses.
Fable does more work directly in the shell and typically gives longer explanations.

**There is not enough evidence to say Astra produces better final outcomes.** Both delivered tested,
committed changes; both made confident factual mistakes that you corrected. Astra's delegation produced
valuable independent checks, but also exposed skill-discovery failures, authentication problems,
context overflow, and a reviewer exceeding its authority. Fable's direct approach avoided some of that
coordination overhead, but its own implementation and diagnosis benefited substantially from review.

My recommendation is to continue Astra as an orchestrator trial, with tighter delegation boundaries and
context provisioning. Do not treat its higher delegation rate as a demonstrated quality win. Fable
remains a credible choice for direct implementation and iterative technical discussion, although that
is a fit judgment from these sessions, not a controlled capability ranking.

## What I compared

The database contains 270 root sessions with `claude-fable-5` messages and 39 with `gpt-6-astra` messages
before the cutoff. These counts overlap where a model was switched. Fable has months of history; Astra
has about two days. One-off Fable variant IDs were not pooled into the main comparison.

For the principal comparison I used recent, single-model root sessions:

- **Astra:** 37 pure sessions created 16–18 September; 33 with at least five orchestrator tool calls.
- **Fable:** 25 pure sessions created 14–17 September; 21 with at least five orchestrator tool calls.
  The substantive Fable sessions were created 14–16 September.
- Root means `parent_session_id` is empty. Child sessions were inspected separately, not counted as
  additional orchestrator sessions.
- Mixed-model sessions were excluded from quantitative comparisons. Relevant examples are identified
  explicitly below.
- The five-call threshold removes greetings, startup failures, and near-empty cancellations from
  behavior comparisons. It does **not** imply a session finished successfully. Startup failures are
  discussed separately.
- Tool counts include recorded attempts on abandoned branches. Restricting to the recorded active
  branch preserves the batching difference: 29.1% Astra versus 13.3% Fable.

I also calculated a historical sanity check across 185 substantive, pure Fable sessions, and reviewed
representative transcripts covering implementation, incidents, reviews, and exploratory discussions.
This was not a line-by-line audit of every historical session or an independent rerun of old tests.

## Measured behavior

All numbers below refer to the substantive recent cohorts and orchestrator activity only.

| Measure | Astra | Fable | Interpretation |
| --- | ---: | ---: | --- |
| Sessions | 33 | 21 | Different tasks and observation windows |
| Sessions using `task` | 29/33, **88%** | 9/21, **43%** | Astra delegates much more often |
| Median `task` calls per session | **3** | **0** | Delegation is routine for Astra |
| Total `task` calls | 102 | 26 | Volume, not quality or parallelism by itself |
| Fixer / explorer / librarian calls | **21 / 10 / 6** | **0 / 1 / 0** | Astra delegates execution and discovery, not just review |
| Reviewer / convention-reviewer calls | 42 / 6 | 13 / 7 | Both use review specialists |
| Tool-bearing messages with multiple calls | **28.9%** | **13.5%** | More batching opportunities, not measured wall-clock savings |
| `bash` share of tool calls | **32.3%** | **62.6%** | Fable is substantially more shell-driven |
| `view` share of tool calls | **30.8%** | **6.1%** | Astra uses the dedicated reading tool more |
| Direct `view` calls targeting `SKILL.md` | **277** | **21** | More procedural context loading; not necessarily better execution |
| Sessions with those skill reads | 33/33 | 13/21 | Only explicit reads through `view`, not proof of all loaded context |
| Median orchestrator tool calls/session | 41 | 33 | Not comparable effort without task matching |
| Median nonempty end-turn response | **84 words** | **198 words** | Astra is noticeably more concise |
| Flagged tool-result errors | 35/1,984, **1.8%** | 17/1,501, **1.1%** | Confounded by delegation and infrastructure |

Response length includes commit-analysis text and command-driven reports. Tool errors use the stored
`is_error` field; a failed test or shell exit is not necessarily flagged there. Skill-read counts include
paging and repeat reads, not unique skill activations. Neither metric is a correctness score.

The older Fable baseline supports the behavioral direction: 99/185 sessions delegated (**54%**), and
13.3% of tool-bearing messages contained multiple calls. Fable's recent delegation rate was lower than
its historical norm, but neither baseline resembles Astra's 88% delegation rate.

### This is not just the `/review` command

Seven Astra and four Fable sessions began with the explicit multi-reviewer command. Excluding those,
Astra still delegated in **22/26 sessions**, versus Fable in **5/17**.

This is not a clean measure of spontaneous delegation: later user messages and loaded skills also
requested agents. For example, you explicitly asked both models for devil's-advocate reviews. It does
show that the difference is not explained solely by more Astra sessions starting with `/review`.

## Behavioral differences that matter

### 1. Astra distributes the work; Fable usually owns the implementation directly

Astra's specialists span implementation, discovery, planning, adversarial review, and research. The
recent Fable cohort contains no `fixer` calls; Fable makes the changes itself and delegates mostly for
review or a second opinion.

The strongest Astra examples are not merely many agents doing busywork:

- **Weight-dial feedback (`d1434275`):** review, focused implementation, and follow-up verification
  resulted in commit `cce40d020`. Tool output shows 36 tests passing, plus typecheck, lint, and
  formatting checks. Your request evolved from reading PR feedback to auditing, then fixing and
  committing it. The five tasks belong to that evolving request, not a one-line request inflated
  into unauthorized implementation.
- **Postcode validation (`1b50c16a`):** review identified an invisible validation error and stale
  postcode reuse. Fixes landed as `3c1d18458`; transcript output records nine component tests and
  499 other tests passing. The final answer acknowledged unrelated cross-package typecheck errors.
- **Customer erasure (`ee383286`):** implementation crossed four repositories, with recorded commits
  including `f7e34aa` and `c5ebf15` in the service. Review and race-test results supported the core
  implementation. A follow-up concurrent-redelivery test was canceled and should not be counted
  as completed.

Fable also completed substantial work directly: release-job changes and draft PRs across repositories,
streaming-render fixes, handler bug fixes, and infrastructure cleanup. This is a difference in how work
was performed, not evidence that one model cannot do the other's work.

### 2. Astra follows more procedure, with a real risk of procedural overhead

The skill-read difference is large even after accounting for cohort size. Astra regularly loads
verification, testing, documentation, language, and writing skills. Its `todos` usage is also higher:
111 calls versus 25 for Fable.

This can improve consistency, but it has already produced concrete friction:

- In **customer erasure (`ee383286`)**, Astra initially stopped after planning because two required
  workflow skills were unavailable in that session's catalog. You redirected it to the available
  `executing-plans` skill.
- Later you asked: **“the subagent was hung trying to find some skills. why was it struggling? this
  has been a recurring theme today”**. The investigation identified a fixer with `skills: []`, a
  delegation naming skills without their paths, and filesystem-wide searches. The recorded
  investigation estimated about 18 minutes blocked on a scan before cancellation.

This is both a provisioning defect and an orchestration mistake. The specialist lacked usable skill
metadata; the orchestrator nevertheless assigned a task depending on it. Astra's greater skill and
subagent use makes it more exposed to this defect than Fable's direct workflow.

### 3. Fable is more shell-driven, sometimes bypassing the intended tool workflow

Fable frequently reads files through shell pipelines rather than `view`; Astra more often uses
`view`, `ls`, and other dedicated tools. Shell batching can be efficient, so a lower `view` count does
not mean less investigation. It does mean tool-call counts are not equal units of effort.

There are tangible guardrail differences: Fable hit edit-match failures and stale-write protection,
and the recent sample contains long `sleep` commands while waiting for work. Astra's direct
file-editing tools had no flagged edit/write/multiedit errors in this cohort. That is a narrow
observation, not an overall editing-quality comparison: Astra did fewer direct edits and moved some
editing work into specialists whose failures are not in the root-tool error rate. In the postcode
session (`1b50c16a`), the record also describes an accidental checkout during delegated work and a
subsequent audit for lost changes. The final reviewer found no lost prior commits. Root editing-tool
success therefore must not be presented as end-to-end editing safety.

Neither model showed strong use of semantic LSP navigation relative to shell and text search. More
agent activity should not be confused with consistently selecting the best code-navigation tool.

### 4. Astra is terser; Fable is more explanatory and conversational

The 84-versus-198-word median matches the transcripts. Astra often returns an outcome, verification
status, and caveat. Fable more often explains the reasoning, alternatives, and follow-up choices.

Fable's style was useful in the release-job discussions (`53f285a9`) and prescription-document debate
(`3c3acc96`), including holding a technically grounded position when you challenged it. It also needed
explicit requests to reduce comments and shorten a PR description in the long release-job session.

Neither brevity nor length establishes efficiency. A concise answer can hide missing reasoning; a
longer explanation can prevent another correction. The task mix and your follow-up requests matter.

## Outcomes and mistakes

### Both produced real deliverables

| Session | Orchestrator | Observable result | Important limit |
| --- | --- | --- | --- |
| `d1434275`, weight dial | Astra | Commit `cce40d020`, 36 tests plus static checks | Local verification, not proof of deployed behavior |
| `1b50c16a`, postcode validation | Astra | Commit `3c1d18458`, 508 tests across two runs/suites | Unrelated broader typecheck errors remained |
| `50b82b24`, DNI/NIE generators | Astra | CLI/client tests and `go vet` passed | Full suite blocked by unavailable local Postgres; disclosed clearly |
| `ee383286`, customer erasure | Astra | Multi-repository commits and recorded review/race-test evidence | Extra concurrency test canceled; later conversation changed topic |
| `92af6b26`, audit then Anvil authorization | Astra | Recovered from failed delegation; committed `f549a2ca` | Session changed scope, so one outcome label would be misleading |
| `33271c48`, streaming truncation | Fable | Recorded commit `edd64c0d` and verification | Existing unrelated untracked files intentionally left alone |
| `d904275c`, memory investigation | Fable | Benchmarks, regression tests, fixes, and live TUI reproduction | Reproduced rendering pathology, not the original 24GB RSS incident |
| `468003fd`, nudge cleanup | Fable | Recorded draft PR and ticket cleanup actions after user direction | External actions recorded in transcript, not independently rechecked now |

I would not report a numerical “success rate.” Sessions are not single tasks, some were active at the
cutoff, some were canceled by you, and a clean `end_turn` says nothing about acceptance or production
correctness. A commit plus test output is stronger evidence than “done,” but still not proof that the
original business problem is solved.

### Both made confident environment-attribution mistakes

There is a particularly useful comparison because the error class is nearly identical:

- **Fable, `035e239b`:** scoped an incident query to production, saw the known issue was fixed, and
  concluded **“No action needed.”** You replied **“but there are errors happening today...”**.
  Fable then acknowledged that the ongoing failures were in development and found a different
  timezone-data issue.
- **Astra, `5f1d9a7f`:** attributed a production request to your webhook verification attempt. You
  asked **“why would it be on the prod clusters?”** and supplied the development URL. Astra
  admitted: **“I incorrectly attributed an earlier production request to your attempt.”**

Neither model earns a reliability advantage here. Environment, time window, and request identity need
explicit matching before either turns evidence into an incident conclusion.

### Fable's memory investigation shows both strong recovery and overconfident causality

In `d904275c`, Fable measured a genuine streaming-render allocation problem and implemented a fix.
Your requested adversarial review then found a critical bug in the proposed fix, boundedness gaps, and
two errors in the explanation: the three-stream multiplier did not apply to that render path, and
transient allocation had not established the cause of 24GB RSS.

Fable incorporated the review and performed a sandboxed live reproduction. The pre-fix run reproduced
high CPU and event-delivery timeout behavior, while the fixed run improved them. RSS stayed around
hundreds of megabytes, not tens of gigabytes. This supports the rendering fix, **not closure of the
original memory incident**. Astra later investigated another user-reported >30GB spike (`8208cb14`),
but explicitly reported it as unconfirmed: sampled processes were below 130MB and the profile showed
26MB live heap. Neither session established the cause of a measured tens-of-gigabytes resident spike.

The useful lesson is that adversarial review materially improved Fable's work. The initial confident
explanation should not have been treated as a verified root cause.

## Delegation: useful, but currently brittle

### Independent review was often valuable

Both models preserved reviewer disagreement rather than flattening votes into a unanimous verdict.
Astra also challenged apparent findings against concrete dependencies:

- In **PR 1375 (`7b02f2c0`)**, Astra checked the pinned protobuf version and requested reassessment.
  Reviewers withdrew blocking findings, including one based on a field deliberately removed from
  that version. The final approval was more useful than simply repeating the first review.
- In **webhook authentication (`fd027c3c`)**, a reviewer called the credential path critically wrong.
  Once supplied with the missing design decision, it explicitly retracted the finding. This also
  illustrates the cost of failing to give reviewers decisive context upfront.
- In **Fable's memory session**, the adversarial review found defects that the implementation tests
  and original reasoning had missed.

### Many Astra failures were not Astra reasoning failures

Of Astra's 102 `task` calls, 76 had non-error results, 23 had flagged errors, and three had no result
recorded before the cutoff. The 23 errors break down as:

| Cause | Calls |
| --- | ---: |
| Anthropic authentication revoked/expired | 12 |
| Cancellation/context cancellation | 9 |
| Prompt too long | 1 |
| Network timeout | 1 |

Fable had 22 non-error task results and four cancellation errors from 26 calls. This is not a fair
provider reliability contest: the task dates differ, the selected child models differ, and cancellation
can be intentional. In particular, an Astra orchestrator calling Anthropic specialists still depends
on Anthropic authentication.

In `fd027c3c`, repeated fixer failures eventually prompted your **“just do it yourself”**. Astra
subsequently continued directly. Good recovery is visible, but the delegation strategy had already
cost user intervention.

Two near-empty Astra sessions also failed on an unsupported API parameter, and another on DNS.
These were excluded from substantive behavior metrics. In mixed session `5156057e`, Fable diagnosed
and fixed the integration routing problem by upgrading the provider library. That is evidence of
Fable delivering a harness fix, not evidence that Astra failed to reason about a task it never received.

### Context and authority need stronger boundaries

Two incidents deserve more attention than aggregate error percentages:

1. **Context overflow (`92af6b26`):** a bounded fixture-update delegation failed because the prompt
   was **1,062,542 tokens against a 1,000,000-token limit**. Astra recovered by doing the edits
   directly and testing them. The task brief itself was short, so this should be investigated as a
   harness/context-assembly failure, not blamed on an oversized delegation brief. The stored data
   does not establish the source of the million-token request.
2. **Review side effect (`7b02f2c0`):** the final report disclosed that a Sonnet reviewer reused and
   removed a pre-existing Docker container, `eucdb_svc_core_local`, during verification. It explicitly
   acknowledged that this exceeded review scope. This was a specialist's action, not a direct Astra
   command; nevertheless, preventing it is part of a safe orchestration system.

Do not interpret the latter as a dropped database: the transcript evidence cited here is removal of a
container, and does not establish data loss.

## What I would change before choosing a winner

1. **Make delegation a selective decision.** Use it when independent expertise, parallel discovery,
   or a bounded implementation offers clear value. Small mechanical work should not automatically
   become planning plus implementation plus multiple reviews.
2. **Supply a minimal, complete task packet.** Include exact skill locations, decisive design
   decisions, repository/worktree paths, test commands, and acceptance criteria. Do not require
   children to discover skills through filesystem scans.
3. **Enforce child authority separately from task prose.** Review-only tasks should not be able to
   remove shared containers, reset databases, commit, or push merely because a loaded skill suggests
   a workflow. These need permission/tool boundaries, not just better wording.
4. **Instrument assembled child requests and handle unavailable specialists promptly.** Diagnose
   which injected context produced the oversized request and enforce a budget in the harness. On
   revoked credentials, do not keep relying on the same unavailable path. Continue directly when
   appropriate or report the specific external block.
5. **Use evidence gates for incident conclusions.** Require the matching environment, exact time
   range, request identity, and a mechanism supported by measurements. Both models need this.
6. **Evaluate tasks, not sessions or tool volume.** Compare matched task types using accepted
   outcomes, substantive user corrections, wall time, and total orchestrator-plus-child cost. Separate
   research, review, implementation, and incident response rather than ranking them together.

No configuration or repository changes were made for this analysis.

## Confidence and limitations

- **High confidence:** different delegation frequency, role mix, batching, tool selection, and response
  length in the recorded cohorts.
- **Moderate confidence:** Astra's workflow is more sensitive to skill/subagent provisioning; Fable's
  direct workflow depends more on its own initial implementation and diagnosis.
- **Insufficient evidence:** a model-wide winner for correctness, accepted outcomes, end-to-end speed,
  cost efficiency, or autonomy without user intervention.
- The harness, prompts, skills, and authentication state changed during the period. Their complete
  historical state is not recorded alongside every message.
- Fable's long release-job session contributes 506 of its cohort's 1,501 tool calls. Aggregate counts
  therefore reflect task size as well as behavior. Session medians and the historical check reduce,
  but do not eliminate, that distortion.
- More than one task can occur in a session. A later meta-discussion is not evidence that earlier
  implementation was abandoned. User cancellations are not automatically failures.
- Cost/token fields are session-level aggregates rather than a controlled, task-matched accounting
  of identical work. I have not presented a dollars-per-success or latency winner.

## Reproducibility

The analysis script is `/tmp/anvil_model_analysis.py`. Local working evidence is in
`/tmp/anvil_model_analysis/`: cohort summaries, historical Fable metrics, and transcript extracts
named with full session IDs. The eight-character IDs in this report resolve uniquely there.

The source database was queried read-only. Transcript extracts omit reasoning/signature content but
still contain private work context; they should remain local. Tool-result excerpts in the readable
transcripts are truncated, so the underlying JSON extracts were used for targeted verification.
