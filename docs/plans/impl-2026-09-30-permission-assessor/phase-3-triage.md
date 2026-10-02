# Phase 3: Triage and Stats

> **Status:** DRAFT
> Depends on phase 1 being merged. Independent of phase 2. It can land
> first, and should, because it saves prompts even with the assessor off.
> Create a PR for human review when done.

## Specification

**Problem:** Explicit rules are the fastest and most predictable way to
decide a permission request, and they cost nothing. But nobody knows which
rules to add: the calls that repeat often without a rule are buried in the
decision log. Once the assessor exists, every uncovered call also costs a
classifier round trip and some latency.

**Goal:**
- `anvil permissions triage` reads the last N days of the decision log.
- It finds tool calls that repeat often, were always approved, and aren't
  covered by a rule.
- It proposes **narrow** rules for them. Suggestions come in two tiers:
  - **Tier A**: patterns from a curated table of command families that
    are safe for any arguments. Selectable, and applied by `--yes`.
  - **Tier B**: other simple patterns and MCP tools. Shown with a warning
    and selectable one at a time. `--yes` never applies them.
- It also proposes narrow `deny` rules for calls that are repeatedly
  denied.
- Before writing anything, it simulates the resulting config against all
  logged evidence.
- `anvil permissions stats` reports how requests were resolved. It also
  keeps three things separate: shadow agreement between the assessor and
  the human, enforcement outcomes, and assessor errors.

**Scope:**
- In:
  - a pure `internal/permission/triage` package: grouping, a curated
    safe-family table, validation over the full match set, and simulation;
  - analytics queries in `internal/permission/decisionlog`;
  - the `anvil permissions triage` and `anvil permissions stats` commands;
  - interactive numeric selection, plus `--json` and `--yes`.
- Out:
  - automatic rule creation without confirmation;
  - the TUI nudge (phase 4);
  - path-based rules for file-editing tools;
  - asking the classifier to vet patterns (possible follow-up).

**Success Criteria:**
- [ ] `go test -race ./internal/permission/triage/... ./internal/permission/decisionlog/... ./internal/cmd/...` passes.
- [ ] Tier A only ever contains patterns from the curated `safeFamilies`
      table. Every entry in that table has a test proving it can't express
      a known-dangerous variant. Examples: `find` is absent because of
      `-delete`/`-exec`, `sed` because of `-i`, and `rg` because of `--pre`.
- [ ] A candidate is dropped when any logged request its pattern would
      match was denied, including requests from other groups or denied by
      a rule. It is also dropped when the current config already denies
      any of its examples.
- [ ] Deny candidates come only from single-segment calls, at
      verb-specific depth (e.g. `gh pr merge *`, never `gh pr *`).
- [ ] `--scope workspace` only considers decisions whose `working_dir` is
      the current project. Global suggestions show how many projects each
      one came from.
- [ ] Rules are written via `ConfigStore.SetPermissionRule(scope, tool, input, action)`.
      After writing, triage reloads the config and re-evaluates every
      example, then reports any example that doesn't evaluate to the
      intended action.
- [ ] `stats` warns when there are fewer than 200 shadow comparisons, and
      never mixes rows from different `schema_version`/`battery_version`
      values.

## Context Loading

_Run before starting:_

```bash
read internal/permission/segment/segment.go   # Split, Generalize (2-token max!), IsRedirect (phase 2) / redirPrefixRe
read internal/permission/evaluate.go          # Evaluate, EvaluateAll, FromSession (phase 1)
read internal/permission/match/match.go       # "*" matches "/" too
read internal/permission/decision.go          # DecisionSource values, AssessmentRecord (phase 1)
read internal/config/permissions.go           # PermissionRule shape
read internal/config/store_permissions.go     # SetPermissionRule ~23, scope → file ~90
read internal/config/scope.go
read internal/agent/tools/safe.go             # existing safe read-only prefixes
read internal/agent/tools/mcp-tools.go        # 110-128: MCP rules are tool-level only
read internal/cmd/session.go                  # cobra + sessionSetup + --json conventions
read internal/cmd/root.go                     # AddCommand list
read internal/db/permission_decisions.sql.go  # generated in phase 1
```

## Triage Logic Tasks

### Task 1: Candidates, curated families, validation, simulation

> **As implemented (2026-10-01):** Tier A shipped with only `git status *`
> and `git rev-parse *`. Every other family below was dropped because some
> flag writes arbitrary files or runs arbitrary programs, e.g. `git diff
> --output`, `go test -toolexec`, `go vet -vettool`, `gofumpt -w`, or reads
> secrets (`head`/`tail`/`wc`). `families_test.go` asserts each one is
> absent and gives the reason. Allow candidates in either tier are also
> never proposed when:
> - the command is on `neverPropose` (destructive, exec, network, or
>   mutating `git`/`gh`/package-manager verbs);
> - the first token is a wrapper from `segment.IsWrapper` (`nohup`,
>   `watch`, `timeout`, ...);
> - any token in the pattern prefix isn't a bare word (quoted or escaped
>   heads like `'rm'` bypass name checks).
>
> **Known issue found during review, not fixed here:** the existing rule
> evaluator matches raw segment text. `'rm' -rf x`, `/bin/rm -rf x`, and
> `r''m -rf x` don't match a `rm *` deny rule and fall through to ask, and
> `segment.Split("\\rm -rf x")` returns `m -rf x`. Fix this before phase
> 2's `enforce` mode, because ask calls would then go to the assessor
> instead of the human.

**Context:** `internal/permission/triage/` (new)

**Files:**
- Create: `internal/permission/triage/triage.go`, `internal/permission/triage/families.go`
- Test: `internal/permission/triage/triage_test.go`, `internal/permission/triage/families_test.go`

**Steps:**

1. [ ] Types:

   ```go
   // Package triage finds repeated, unresolved permission requests that a
   // narrow explicit rule could cover.
   package triage

   type Record struct {
   	SessionID     string
   	WorkingDir    string
   	ToolName      string
   	Input         string
   	InputSegments []string
   	DecidedBy     string   // permission.DecisionSource value
   	Verdict       string   // permission.Verdict value
   	MaxHazard     *float64 // From AssessmentRecord.Nouls when present.
   }

   type Kind string

   const (
   	KindAllow Kind = "allow"
   	KindDeny  Kind = "deny"
   )

   type Tier string

   const (
   	TierA Tier = "A" // Curated safe family.
   	TierB Tier = "B" // Needs human judgment.
   )

   type Candidate struct {
   	Kind         Kind
   	Tier         Tier
   	ToolPattern  string
   	InputPattern string // Empty for tool-level rules.
   	Count        int
   	Sessions     int
   	Projects     int
   	Examples     []string // Up to 3 distinct raw inputs/segments.
   	Warning      string   // Tier B explanation.
   }

   type Options struct {
   	MinCount       int     // Default 5.
   	MaxHazardAllow float64 // Default 0.2.
   	WorkingDir     string  // Non-empty: only records from this project.
   }

   // Analyze returns candidates sorted by Tier, then Count desc, then pattern.
   func Analyze(records []Record, rules []config.PermissionRule, opts Options) (allow, deny []Candidate)
   ```

2. [ ] `families.go` holds the curated allowlist for **Tier A**. Each entry
       is a pattern whose every expansion is safe inside a dev workspace.
       Keep the list short, and only add an entry together with a test in
       `families_test.go` explaining why no argument makes it dangerous.

   ```go
   // safeFamilies maps a pattern to its rationale. A segment belongs to
   // a family when match.Match(pattern, segment) is true.
   var safeFamilies = map[string]string{
   	"git status *":        "read-only",
   	"git diff *":          "read-only (no --output writes: see test)",
   	"git log *":           "read-only",
   	"git show *":          "read-only",
   	"git blame *":         "read-only",
   	"git rev-parse *":     "read-only",
   	"git ls-files *":      "read-only",
   	"go build *":          "compiles; -o writes are local build output",
   	"go test *":           "tests; -exec is blocked by bash blockFuncs",
   	"go vet *":            "read-only analysis",
   	"go list *":           "read-only",
   	"go mod tidy *":       "rewrites go.mod/go.sum only",
   	"gofumpt *":           "formats files in place, VCS-recoverable",
   	"gh pr view *":        "read-only",
   	"gh pr list *":        "read-only",
   	"gh pr diff *":        "read-only",
   	"gh pr checks *":      "read-only",
   	"gh issue view *":     "read-only",
   	"gh issue list *":     "read-only",
   	"gh run view *":       "read-only",
   	"gh run list *":       "read-only",
   	"task test *":         "project test target",
   	"task lint *":         "project lint target",
   	"task fmt *":          "project fmt target",
   	"wc *":                "read-only",
   	"head *":              "read-only",
   	"tail *":              "read-only (no -f in non-interactive shell: see test)",
   }
   ```

   Deliberately excluded, with a test asserting their absence:
   - `find` (`-delete`, `-exec`), `sed` (`-i`), `rg` (`--pre`),
     `awk` (`system()`), `xargs`;
   - `cat`/`less` (reads secrets outside the repo into the LLM context);
   - interpreters, and package-manager `run`/`exec`/`dlx`;
   - `make`, `npm`, `docker`, `kubectl`, `gcloud`, `aws`, `terraform`;
   - `git diff --output`: add an exclusion check if the family stays, or
     drop `git diff *`.

   The implementer must verify each rationale. Where a flag breaks it, drop
   the family rather than adding a flag blacklist.

3. [ ] Grouping in `Analyze`:
   - **Evidence set**: all records in the window, including rule-decided
     ones, filtered by `opts.WorkingDir` when set. It's used for
     validation.
   - **Candidate source**: records with `DecidedBy` in {`human`,
     `assessor`, `session_grant`, `session_rule`}, excluding `cancelled`.
     Repeated `session_rule` grants are the strongest signal.
   - **bash**, per segment of `InputSegments` (or `segment.Split(Input)`):
     - Skip redirect segments and segments containing
       `` ` $ | & ; < > ( ) { } ``.
     - If some `safeFamilies` pattern matches the segment, use it as the
       Tier A key.
     - Otherwise the Tier B key is `segment.Generalize(seg)`. Drop it if
       the result is a bare `*` or has a first token of `*`.
   - **mcp_\***: a Tier B tool-level key `(ToolName, "")`, with the warning
     "MCP rules cover every argument this tool accepts".
   - **Everything else** (`edit`, `write`, `view`, `ls`, `fetch`, ...):
     skip.

4. [ ] Allow candidate acceptance, all of which must hold:
   - count >= `MinCount`;
   - every source verdict is allow;
   - max hazard is nil or <= `MaxHazardAllow`;
   - **Full-match validation**: no record in the evidence set whose input
     (or any segment, for bash) matches the candidate pattern has
     `Verdict == deny`. Match with `match.Match`, not group membership.
   - **Current-policy check**: `permission.Evaluate(tool, example, rules, nil)`
     returns something other than deny for every example. Drop the
     candidate if it already returns allow for all examples (already
     covered).

5. [ ] Deny candidates:
   - Only from records with exactly **one** non-redirect segment,
     `Verdict == deny`, and `DecidedBy` in {`human`, `assessor`}. A denied
     chain says nothing about which segment was objectionable.
   - The pattern is the first three tokens when token 2 and token 3 both
     match `^[a-z][a-z-]*$`, otherwise the first two, followed by ` *`.
     So `gh pr merge 42` gives `gh pr merge *`.
   - count >= `MinCount`.
   - No record matching the pattern was ever allowed.

6. [ ] `Simulate(rules []config.PermissionRule, chosen []Candidate, evidence []Record) []Conflict`
   - Append the chosen rules as config rules in the same shape
     `SetPermissionRule` writes.
   - Report every evidence record that was human-denied but now evaluates
     to allow.
   - Report every chosen example that doesn't evaluate to the candidate's
     action.

   The CLI refuses to write when there are conflicts, unless `--force` is
   passed.

7. [ ] Tests (table-driven, `t.Parallel()`):
   - 6× `go test ./...` allowed → Tier A `go test *`.
   - 6× `git -C /x status` → not Tier A (the family is `git status *`), and
     Generalize gives `git *`, which is a Tier B key. Assert it's dropped
     by adding `git *`-style one-token wildcards for `needsSubcommand`
     commands to the Tier B rejection. Keep a small
     `needsSubcommand = {git, gh, go, npm, pnpm, yarn, cargo, docker, kubectl, gcloud, aws, terraform, task, uv, pip, brew, bq}`
     for this.
   - 6× `gh pr view 1` allowed plus 1× `gh pr merge 1` denied →
     `gh pr view *` is Tier A. `gh pr *` is never proposed.
   - 6× `cat file.txt` allowed plus 1× `cat .env` denied → no `cat *`
     candidate (full-match validation).
   - 6× `find . -name x` → Tier B `find *`. Assert it's absent from Tier A.
   - `go test ./... | tee out.txt`: `go test *` counts, and `tee` becomes a
     Tier B candidate.
   - 5 allows plus 1 deny of `npm install` → neither list.
   - 6× single-segment denied `gh pr merge N` → deny `gh pr merge *`.
   - 6× denied `git status && gh pr merge 1` → no deny candidates.
   - 6× `mcp_Linear_get_issue` → Tier B with a warning.
   - An existing rule `bash: {"go test *": "allow"}` → the candidate is
     hidden.
   - An existing rule `bash: {"go test *": "deny"}` → the candidate is
     dropped.
   - Records from another `WorkingDir` with `opts.WorkingDir` set →
     excluded.
   - `Simulate` flags a chosen `tee *` when a denied `tee /etc/hosts`
     exists.

   `families_test.go`: for each family, a list of dangerous inputs that
   must **not** match it (e.g. `git push`, and `go run x.go` against
   `go test *`). Also assert that `find`, `sed`, `rg`, `cat`, `awk`,
   `xargs`, `make`, `python*`, and `npm run *` aren't families.

**Verify:**
```bash
go test ./internal/permission/triage/ -v -race
```

## CLI Tasks

### Task 2: Analytics queries, `permissions triage`, and `permissions stats`

**Context:** `internal/permission/decisionlog/`, `internal/cmd/`

**Files:**
- Create: `internal/permission/decisionlog/analytics.go` (load `[]triage.Record`, compute `Stats`)
- Create: `internal/cmd/permissions.go`
- Test: `internal/permission/decisionlog/analytics_test.go`, `internal/cmd/permissions_test.go`
- Modify: `internal/cmd/root.go` (`AddCommand(permissionsCmd)`)

**Steps:**

1. [ ] `analytics.go`:
   - `LoadRecords(ctx, q, since time.Time) ([]triage.Record, error)` maps
     rows. It unmarshals `input_segments`, and parses `assessment` into
     `permission.AssessmentRecord`, setting `MaxHazard` to the max over
     the hazard keys. Bad JSON leaves it nil, with a `slog.Debug`.
   - `ComputeStats(rows) Stats` returns:
     - `ByDecidedBy map[string]int` and the total. This is the real
       permission-request volume. Label it that way, not as "tool
       calls".
     - `Shadow` holds rows where `decided_by = human` and
       `assessment.mode = shadow`, as a matrix of assessor outcome
       (`allow`/`escalate`/`deny`/`error`/`skipped`) × human verdict, with
       the sample size.
     - `Enforce` holds rows where `assessment.mode = enforce`: assessor
       allow/deny counts (`decided_by = assessor`) and escalations resolved
       by the human (a verdict matrix).
     - Error and skip counts by `skip_reason`/error prefix.
     - Token totals and means, and latency mean and p95, for rows with
       outcome allow/escalate/deny.
     - All of the above grouped by `(schema_version, battery_version)`.
       Never merge groups.

2. [ ] Command tree:

   ```
   anvil permissions            (alias: perms)
     triage [--days 7] [--min-count 5] [--scope global|workspace] [--json] [--yes] [--force]
     stats  [--days 30] [--json]
   ```

   Setup mirrors `sessionSetup`: `config.Init("", dataDir, false)`,
   `db.ConnectGlobal`, `db.New`. The current project dir is
   `cfg.WorkingDir()`. `--scope workspace` sets `Options.WorkingDir` to it.

3. [ ] `triage` TTY output:

   ```
   Permission triage: last 7 days, 1,284 unresolved requests

   Suggested allow rules (tier A: curated safe families)
     #  count  sess  proj  rule                        examples
     1    212    18     3  bash: "go test *"           go test ./internal/... | go test -run X ./pkg
     2     97    11     2  bash: "git status *"        git status --short

   Needs your judgment (tier B: never applied by --yes)
     3     41     6     1  mcp_Linear_get_issue        MCP rules cover every argument this tool accepts
     4     18     4     2  bash: "tee *"               tee out.txt

   Suggested deny rules
     5     12     3     1  bash: "gh pr merge *"       gh pr merge 42

   Select rules to add (e.g. 1,2,5; "a" for all tier A; blank to cancel):
   ```

   Behaviour:
   - Run `Simulate` on the selection and print any conflicts. Refuse to
     write when there are conflicts, unless `--force`.
   - Write each rule with `store.SetPermissionRule(scope, tool, input, action)`,
     then reload the store and re-run `Evaluate` on each example. Print a
     ✓ or ✗ line per rule, then the config file path.
   - `--yes` applies all Tier A allow candidates without prompting. It
     never applies Tier B or deny candidates.
   - `--json` prints `{"allow": [...], "deny": [...]}` with tiers and
     doesn't prompt.
   - If stdin isn't a TTY and neither `--json` nor `--yes` is set, print
     and exit without writing.
   - Truncate examples to 60 runes, and use `lipgloss` styles consistent
     with `session.go`.

4. [ ] `stats` TTY output prints each `ComputeStats` section.
   - Highlight the **shadow: assessor allow × human deny** cell, and print
     a warning when the shadow sample is under 200: "not enough evidence
     to enable enforce".
   - `--json` emits `Stats`.

5. [ ] Testability:
   - Factor `runTriage(ctx, q db.Querier, store *config.ConfigStore, opts triageOpts, in io.Reader, out io.Writer) error`
     and a matching `runStats`.
   - Tests seed a temp DB (`db.Connect(t.Context(), t.TempDir())`) via
     `InsertPermissionDecision`, and use a temp-dir config store (see how
     `store_permissions` tests construct one).
   - Input `"1\n"` → the rule is present in the scope file, and the
     re-evaluation check passes.
   - A conflicting selection without `--force` → no write.
   - `--yes` → only Tier A written.
   - Stats JSON: the shadow matrix cells, a separate enforce section, and
     groups split by `battery_version`.

**Verify:**
```bash
go test ./internal/permission/decisionlog/ ./internal/cmd/ -run 'Permissions|Stats|LoadRecords' -v
go build . && ./anvil permissions stats --days 30
./anvil permissions triage --days 7 --json | jq '.allow | map(select(.Tier=="A")) | length'
task lint
```
