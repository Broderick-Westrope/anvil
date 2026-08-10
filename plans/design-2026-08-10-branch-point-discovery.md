# Branch-Point Discovery Design Spec

**Problem:** When using session trees, branching back from a point many
messages ago is difficult even when the user knows the moment they want.
The branch dialog's character-level fuzzy filter (`sahilm/fuzzy`) is
designed for short labels and degrades on paragraph-length messages:
match scores are noisy, and the list shows only the head of each message,
so the user can't see *why* an item matched or *when* it happened.

**Goal:** The user can reliably find a branch point two ways: (A) a
token-based filter with matched snippets and time/position metadata, and
(C) a natural-language "take me back to" ask mode where a small model
ranks candidate messages and returns the top matches with reasons (e.g.
"the last message where I decided to focus on the dedicated
cadence-timing.md file rather than editing the TSD directly").

**Scope:**

In:
- **A — Filter improvements**
  - New token-based matching strategy for `FilterableList`
    (`internal/ui/list/filterable.go`): matching is opt-in per list;
    existing fuzzy remains the default for command palette, model
    picker, file picker, sessions.
  - Branch dialog (`internal/ui/dialog/branch.go`) and tree dialog
    (`internal/ui/dialog/tree.go`) opt into token matching.
  - Token matcher: split query on whitespace; all terms must match
    (case-insensitive substring, no stemming in v1); rank by term
    clustering with recency as tiebreak; expose matched ranges for
    highlighting. Match strategy abstraction must not change fuzzy
    behaviour for non-opted lists — the shared machinery
    (`MatchSettable`, `renderItem` highlight indexes) is coupled to
    `fuzzy.Match`, so cover with golden tests. Snippet windowing
    requires remapping highlight indexes from the full `Filter()`
    string into the window (rune-safe).
  - Item display (branch + tree dialogs): message head + matched
    snippet (window centred on matched terms, highlighted) + metadata
    line (relative timestamp and position cue, e.g. "12 messages ago").
    Metadata line also shown when not filtering. Position cue counts
    user turns on the *unfiltered* branch path, relative to the leaf.
    For tree-dialog nodes not on the leaf's path (sibling branches),
    show timestamp only — "N messages ago" is meaningless off-path.
    Multi-line items roughly halve visible rows within
    `navDialogHeight` (30) — acceptable; verify the list supports
    variable-height items (`BranchItem.Render` is currently
    single-line-with-info).
- **C — Ask mode (branch dialog only, initially)**
  - Tab/shift+tab toggles the input between Filter and Ask modes,
    mirroring the model picker's tab-switcher (`models.go:121`).
    **Breaking rebind:** the branch dialog currently binds tab to
    Select (`branch.go:70-73`, keys `enter/tab/ctrl+y`) — tab MUST be
    removed from Select there; `enter` and `ctrl+y` remain. Input text
    is preserved across mode switches. Visual state changes clearly
    (prompt label, placeholder with an example query).
  - When the filter yields zero results, show a hint suggesting tab to
    switch to Ask mode. No dedicated shortcut. The hint is suppressed
    immediately after a failed ask (see failure handling).
  - Ask is submit-on-enter (not live). Sends the NL query plus a
    numbered (indexed) list of all user messages on the current branch
    path (truncated ~500 chars each) with ~200 chars of the on-path
    assistant reply for context (the first text-bearing assistant
    message following the user turn on the path; assistant chains are
    not concatenated).
  - Token budget (in-scope constraint, not future work): cap the
    prompt at ~20K tokens; when exceeded, truncate oldest turns harder
    (drop assistant context first, then shrink user excerpts) so the
    prompt always fits one call.
  - Model returns strict JSON: top 3 `{index, reason}` — list indexes,
    not message IDs (small models mangle opaque IDs; indexes are
    bounds-checked locally and mapped back to messages). Results
    render as a ranked list; the reason is displayed where the snippet
    appears in filter mode. Selection behaves identically to filter
    mode (emits `ActionNavigateTree`).
  - Model resolution reuses the small-model convention used by
    `generateTitle` (small model, big-model fallback). No new config.
    `generateTitle` itself is private to `sessionAgent`, so new
    plumbing is required: an exported method on the coordinator, e.g.
    `RankMessages(ctx, sessionID, query, candidates) ([]RankResult,
    error)`, reusing the same model-resolution/auth internals. The
    dialog invokes it via a `tea.Cmd` (through `ActionCmd`), reaching
    the coordinator through the existing `Workspace` facade (as
    `AgentSummarize` does, `ui.go:2017`) — add a `RankMessages`
    pass-through rather than wiring the dialog to the coordinator
    directly. Calls feed the same usage/cost accounting as title
    generation.
  - **Ask lifecycle:** submit shows a spinner/pending state in the
    dialog; further submits are disabled while in flight; closing the
    dialog cancels the request — since `Dialog` has no teardown hook,
    the cancel lives in the UI-side close handling (the UI owns the
    request's `context.CancelFunc`); each submit carries a request ID
    and results for stale IDs are dropped; hard timeout ~10s covering
    the whole call including big-model fallback. Ask works regardless
    of whether the session agent is streaming (it is an independent
    small-model call).
  - **Result routing:** the rank-result msg MUST be handled explicitly
    in `UI.Update` and dispatched via the by-ID dialog lookup
    (`m.dialog.Dialog(dialog.BranchID)`, `dialog.go:169`) — NOT via
    default front-dialog routing (`dialog.go:210`), which drops the
    msg when another dialog (e.g. a permission prompt from a streaming
    agent) stacks on top, permanently wedging the pending state. The
    timeout tick msg is routed identically (handled in `UI.Update`,
    dispatched via the same by-ID lookup) so the backstop cannot be
    dropped by a stacked dialog either. As a belt-and-braces measure
    the dialog also checks elapsed time lazily on its next
    `HandleMsg`, clearing pending and re-enabling submit if the
    deadline passed.
  - **Results display:** `FilterableList.Render()` re-filters from the
    stored query every frame (`filterable.go:127-130`), so ask results
    cannot be shown by simply setting items while the NL query is set
    — the next frame would clobber them with (empty) token-filter
    results. In ask-results state, the list query is cleared (the
    input text is kept) and results are set as the item set directly.
    Typing any character while results are shown discards them,
    restores the full candidate item set, and returns to live
    filtering with the edited query; tab returns to filter mode
    likewise discarding results and restoring the candidate set.
    UI-side close handling owns the request's `CancelFunc` across all
    close paths (`ActionClose`, `closeDialogMsg`, selection); any
    missed cancel is bounded by the ~10s timeout.

Out (noted as future exploration if A+C prove insufficient):
- **B — Auto-gists**: small-model one-line summary per user turn stored
  on the message (background pipeline, migration for old messages).
- **D — Manual labels** (Pi-style): solves the proactive case; separate
  small feature.
- Ask mode in the tree dialog (mechanical follow-up once ranking
  plumbing exists).
- Cross-session or DB-side search (FTS5) — different feature.
- Two-pass lexical-prefilter + rerank pipeline — fallback only if very
  long sessions blow the token budget; prefer harder truncation of
  oldest messages first.

**Constraints:**
- **Branch dialog and ask-mode candidates are user turns only.** Branch
  targets are user messages by construction (`navigateToTreeNode`
  targets the parent for re-submission); assistant text participates
  only as context in the ask prompt. The **tree dialog** already lists
  and allows selecting assistant nodes (`tree.go:95-108, 317-325`) —
  that stays; token filtering there matches whatever nodes the tree
  displays (user + assistant), and snippet/metadata rendering must
  handle assistant messages.
- Filtering stays in-memory over already-loaded items; no schema or
  query changes.
- Ask-mode failure handling: on API error, timeout, or unparseable
  JSON, **stay in Ask mode** with the query intact and show an inline
  error ("Ask failed — try again or press tab to filter"). Never fall
  back to filter mode automatically (an NL query through the
  all-terms filter yields zero results, whose hint would point back at
  Ask — a dead-end loop). Suppress the "try Ask" hint right after a
  failure. Out-of-bounds returned indexes are dropped; if all are
  invalid, treat as "no results" with a message saying so.
- Ask-mode latency target ~1–2s typical; big-model fallback may take
  longer, bounded by the ~10s timeout.
- Fuzzy matching behaviour must be unchanged for all lists that don't
  opt into token matching.

**Success Criteria:**
- [ ] Branch and tree dialog filters require all query terms to match
      and rank by term clustering with recency tiebreak.
- [ ] Filtered items show a highlighted matched snippet plus relative
      timestamp and position cue; unfiltered items show the metadata
      line too.
- [ ] Command palette, model picker, sessions, and file picker filtering
      behaviour is unchanged.
- [ ] Tab toggles Filter ↔ Ask in the branch dialog, preserving input
      text, with clear visual mode indication.
- [ ] A zero-result filter shows a hint suggesting Ask mode (suppressed
      immediately after a failed ask).
- [ ] Tab no longer selects in the branch dialog; enter/ctrl+y still
      select.
- [ ] In-flight asks show a pending state, block duplicate submits, are
      cancelled on dialog close, and stale results are dropped; results
      are delivered via by-ID dialog lookup so a stacked dialog (e.g.
      permission prompt) cannot wedge the pending state.
- [ ] Submitting an NL query returns up to 3 ranked user messages with
      one-line reasons; selecting one navigates exactly as filter-mode
      selection does.
- [ ] Ask mode failures keep the user in Ask mode with the query and an
      inline error; the dialog is never blocked.
- [ ] Ask mode uses the existing small-model resolution with no new
      config surface.

**Design Decisions:**
- A+C chosen over labels (D) and auto-gists (B): labels only solve the
  proactive case; the stated pain is retroactive. Gists add a
  generation pipeline whose value is unproven until C is tested on raw
  text. Do the easy parts first and prove whether they're enough.
- Per-list matching strategy (not global replacement): fuzzy is correct
  for short labels; token matching is correct for prose. Configurable
  strategy avoids degrading other pickers and avoids hybrid-scoring
  ambiguity.
- QMD-style hybrid search (BM25 + embeddings + reranker) rejected as
  overkill: a branch path is tens of messages and fits in one prompt;
  borrow the ideas (lexical + semantic + rerank), skip the infra.
- Assistant-reply context included in the ask prompt because decisions
  often crystallise in the assistant's response, not the user's message
  ("where I decided X" recall). User messages remain the only anchors.
- Tab toggle over prefix (`?`) or separate command: filter and ask have
  different interaction contracts (live/free vs submit/costly), so an
  explicit, reversible mode switch is clearest; prefix is accidental-
  trigger-prone, a separate dialog fragments the try-filter-then-ask
  flow.
- Small-model convention reused (as `generateTitle` does) rather than a
  new config knob — YAGNI. Exposed via a new exported coordinator
  method since `generateTitle`'s resolution logic is private.
- Indexes over message IDs in the model's JSON response: bounds-checked
  locally, immune to ID hallucination/truncation.
- Fail-in-place over fall-back-to-filter on ask errors: avoids the
  circular empty-filter → "try Ask" hint loop.

**Context Files:**
- `internal/ui/list/filterable.go` — `FilterableList`, fuzzy matching
  (`fuzzy.FindFrom` at :110), match ranges for highlighting.
- `internal/ui/dialog/branch.go` — branch dialog (`NewBranch` :34-85,
  enter → `ActionNavigateTree` :100-108).
- `internal/ui/dialog/branch_item.go` — `Filter()` matches message text
  (:30-32).
- `internal/ui/dialog/tree.go` — tree dialog, filtering state (:44,
  keybindings :47-59).
- `internal/ui/dialog/sessions_item.go` — match-highlight rendering
  pattern (:143-198).
- `internal/ui/model/ui.go` — `openBranchDialog` (:4691),
  `handleNavigateTree` (:4712), `navigateToTreeNode` (:4754).
- `internal/agent/agent.go` — `generateTitle` small-model resolution
  (:1231-1320).
- `internal/db/sql/messages.sql` — `GetBranchPath` recursive CTE
  (:58-73).
- Model picker tab-switcher — pattern to mirror for Filter/Ask toggle.
