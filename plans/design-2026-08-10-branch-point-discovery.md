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
    (case-insensitive substring); rank by term clustering with recency
    as tiebreak; expose matched ranges for highlighting.
  - Item display (branch + tree dialogs): message head + matched
    snippet (window centred on matched terms, highlighted) + metadata
    line (relative timestamp and position cue, e.g. "12 messages ago").
    Metadata line also shown when not filtering.
- **C — Ask mode (branch dialog only, initially)**
  - Tab toggles the input between Filter and Ask modes, using the same
    tab-switcher pattern as the model picker. Input text is preserved
    across mode switches. Visual state changes clearly (prompt label,
    placeholder with an example query).
  - When the filter yields poor/no results, show a hint suggesting tab
    to switch to Ask mode. No dedicated shortcut.
  - Ask is submit-on-enter (not live). Sends the NL query plus a
    numbered list of all user messages on the current branch path
    (truncated ~500 chars each) with ~200 chars of each corresponding
    assistant reply for context.
  - Model returns strict JSON: top 3 `{message_id, reason}`. Results
    render as a ranked list; the reason is displayed where the snippet
    appears in filter mode. Selection behaves identically to filter
    mode (emits `ActionNavigateTree`).
  - Model resolution reuses the small-model convention used by
    `generateTitle` (small model, big-model fallback). No new config.

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
- Candidate set is user turns only. Branch targets are user messages by
  construction (`navigateToTreeNode` targets the parent for
  re-submission), so assistant/tool messages are never listed; assistant
  text participates only as context in the ask prompt.
- Filtering stays in-memory over already-loaded items; no schema or
  query changes.
- Ask-mode failure handling: on API error or unparseable JSON, show an
  inline error ("Ask failed — try again or use filter"), fall back to
  filter mode, preserve the typed query, never block the dialog.
  Returned message IDs not on the path are silently dropped; if all are
  invalid, treat as "no results".
- Ask-mode latency target ~1–2s (one small-model call).
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
- [ ] A poor/no-result filter shows a hint suggesting Ask mode.
- [ ] Submitting an NL query returns up to 3 ranked user messages with
      one-line reasons; selecting one navigates exactly as filter-mode
      selection does.
- [ ] Ask mode failures degrade gracefully to filter mode with the query
      preserved.
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
  new config knob — YAGNI.

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
