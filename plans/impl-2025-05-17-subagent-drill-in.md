# Subagent Drill-In UI Implementation Plan

> **Status:** DRAFT

## Specification

**Problem:** The current task/subagent display shows too much noise (full
prompt, nested tool list) while lacking useful metadata (tokens, cost, time,
turns). There is no way to inspect a subagent's full thread.

**Goal:** A two-tier subagent view: compact stats-focused summary in the
parent session, with drill-in to any subagent for full thread-level
auditability. Sidebar becomes the single source of truth for session-level
stats.

**Scope:** See `plans/design-2025-05-16-subagent-drill-in.md` for full scope
definition.

**Success Criteria:**

- [ ] Collapsed task view shows only name line + stats line (no prompt, no
      nested tool tree, no result text)
- [ ] Stats line (turns, tools, tokens, cost, elapsed time) live-updates
      while subagent is running
- [ ] `→` on a focused drillable item replaces the chat viewport with the
      child session's full message thread
- [ ] `←` while drilled in returns to the parent session
- [ ] Mouse click on a drillable item drills into it
- [ ] Breadcrumb bar appears at top when drilled in
- [ ] Sidebar updates to reflect currently viewed session
- [ ] Sidebar shows turns, tool call count, and elapsed time
- [ ] Drill-in works on both running and completed subagents
- [ ] Nested subagents within a drilled-in view are also drillable
- [ ] Auto-scroll follows live output when viewing a running subagent
- [ ] Input editor disabled when viewing a subagent
- [ ] Status icons: MiniDot spinner → single-char shimmer → ✓/×

## Context Loading

_Run before starting each task group:_

```bash
read internal/ui/chat/agent.go
read internal/ui/chat/messages.go
read internal/ui/chat/tools.go
read internal/ui/model/ui.go
read internal/ui/model/chat.go
read internal/ui/model/sidebar.go
read internal/ui/model/keys.go
read internal/ui/common/elements.go
read internal/ui/anim/anim.go
read internal/ui/styles/styles.go
read internal/session/session.go
read internal/ui/AGENTS.md
read plans/design-2025-05-16-subagent-drill-in.md
```

## Tasks

### Task 1: DrillInHandler Interface and Collapsed View Types

**Context:** `internal/ui/chat/`

**Files:**
- Modify: `internal/ui/chat/messages.go` (add `DrillInHandler` interface)
- Modify: `internal/ui/chat/agent.go` (add stats fields, implement
  `DrillInHandler`, rewrite `RenderTool` for collapsed view)
- Modify: `internal/ui/styles/styles.go` (add stats line + breadcrumb
  styles)

**Steps:**

1. [ ] In `internal/ui/chat/messages.go`, add the `DrillInHandler` interface
   after the existing `KeyEventHandler` interface (around line 52):

   ```go
   // DrillInHandler is implemented by items that support drill-in
   // navigation. HandleDelayedClick checks for this interface before
   // Expandable — if the selected item implements DrillInHandler,
   // DrillIn() is called instead of ToggleExpanded().
   type DrillInHandler interface {
       // DrillIn returns the child session ID to drill into.
       DrillIn() string
       // DrillInLabel returns the breadcrumb label for this item
       // (e.g., "Explorer: Search auth").
       DrillInLabel() string
   }
   ```

2. [ ] In `internal/ui/chat/agent.go`, add stats and state fields to
   `AgentToolMessageItem` (line 28):

   ```go
   type AgentToolMessageItem struct {
       *baseToolMessageItem
       nestedTools []ToolMessageItem

       // Drill-in state.
       childSessionID  string
       hasChildMessages bool

       // Live stats updated via pubsub as child session messages arrive.
       turns           int
       toolCalls       int
       tokens          int64
       cost            float64
       countedToolIDs  map[string]bool // track already-counted tool call IDs
   }
   ```

   Add the same fields to `AgenticFetchToolMessageItem` (line 220).

   Add exported methods for both types. Use a shared `agentStats` embedded
   struct if the duplication is large, or just implement on both:
   ```go
   func (a *AgentToolMessageItem) Stats() (turns, toolCalls int) {
       return a.turns, a.toolCalls
   }
   func (a *AgentToolMessageItem) SetChildSessionID(id string) {
       a.childSessionID = id
   }
   func (a *AgentToolMessageItem) SetHasChildMessages(v bool) {
       a.hasChildMessages = v
       a.clearCache()
   }
   func (a *AgentToolMessageItem) IncrementTurns() {
       a.turns++
       a.clearCache()
   }
   func (a *AgentToolMessageItem) IncrementToolCalls(n int) {
       a.toolCalls += n
       a.clearCache()
   }
   func (a *AgentToolMessageItem) SetTokens(t int64) {
       a.tokens = t
       a.clearCache()
   }
   func (a *AgentToolMessageItem) SetCost(c float64) {
       a.cost = c
       a.clearCache()
   }
   ```

3. [ ] Implement `DrillInHandler` on `AgentToolMessageItem`. Reuse the
   existing `agentDisplayName()` helper (line 178) for label generation,
   but strip the model info suffix since the breadcrumb should be compact:

   ```go
   func (a *AgentToolMessageItem) DrillIn() string { return a.childSessionID }
   func (a *AgentToolMessageItem) DrillInLabel() string {
       var params agent.TaskParams
       _ = json.Unmarshal([]byte(a.toolCall.Input), &params)
       // Reuse the name-building logic from agentDisplayName but without
       // model info — breadcrumb labels should be concise.
       return agentBreadcrumbLabel(params.SubagentType, params.Description)
   }
   ```

   Extract a shared helper from `agentDisplayName()`:
   ```go
   // agentBreadcrumbLabel builds a breadcrumb label like "Explorer: Search auth".
   func agentBreadcrumbLabel(subagentType, description string) string {
       var name string
       if subagentType != "" {
           parts := strings.Split(subagentType, "-")
           for i, p := range parts {
               if len(p) > 0 { parts[i] = strings.ToUpper(p[:1]) + p[1:] }
           }
           name = strings.Join(parts, " ")
       } else {
           name = "Agent"
       }
       if description != "" {
           name += ": " + description
       }
       return name
   }
   ```

   For `AgenticFetchToolMessageItem`, `DrillInLabel()` returns
   `"Fetch: " + truncate(params.Prompt, 40)`.

4. [ ] Implement `KeyEventHandler` on both `AgentToolMessageItem` and
   `AgenticFetchToolMessageItem` to handle the `→` key for drill-in.

   First, define the message type in `internal/ui/util/` (create
   `internal/ui/util/drill.go` if needed, or add to existing messages
   file):

   ```go
   // DrillInMsg requests the UI to drill into a subagent session.
   type DrillInMsg struct {
       SessionID string
       Label     string
   }
   ```

   Then the handler:
   ```go
   func (a *AgentToolMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
       if key.Key().Code == tea.KeyRight && a.childSessionID != "" {
           return true, func() tea.Msg {
               return util.DrillInMsg{
                   SessionID: a.childSessionID,
                   Label:     a.DrillInLabel(),
               }
           }
       }
       return false, nil
   }
   ```

5. [ ] Rewrite `AgentToolRenderContext.RenderTool()` to produce the collapsed
   two-line format. Replace the current implementation (lines 101–169) with:

   - **Line 1:** Status icon + display name (via `toolHeader` with
     `compact: true`).
   - **Line 2:** Stats line: `  3 turns · 12 tools · 4.2k tokens · $0.02 · 14s`
     (indented to align under the name).
   - No prompt text, no nested tool tree, no result body, no trailing
     animation.

   Add a helper function `formatStatsLine` in `agent.go`:
   ```go
   func formatStatsLine(sty *styles.Styles, turns, toolCalls int, tokens int64, cost float64, elapsed string, width int) string
   ```

   Handle zero values gracefully (show `0 turns · 0 tools` at spawn).
   For narrow widths (<80 cols), use abbreviated format:
   `3t · 12tl · 4.2k · $0.02 · 14s`.

6. [ ] Apply the same collapsed rendering to
   `AgenticFetchToolRenderContext.RenderTool()`.

7. [ ] In `internal/ui/styles/styles.go`, add new styles:

   Inside the `Tool` struct:
   ```go
   StatsLine lipgloss.Style  // dim/muted for stats text
   StatsSep  lipgloss.Style  // for · separator
   ```

   Add a new top-level `Breadcrumb` struct:
   ```go
   Breadcrumb struct {
       Root  lipgloss.Style  // "Main" label — bold
       Label lipgloss.Style  // subagent label — normal
       Sep   lipgloss.Style  // ">" separator — muted
   }
   ```

   Inside the `ModelInfo` struct:
   ```go
   Stats   lipgloss.Style  // for "3 turns · 12 tools" sidebar line
   Elapsed lipgloss.Style  // for elapsed time
   ```

   Initialize all with appropriate muted/dim colors.

8. [ ] Update the `Animate` method on `AgentToolMessageItem` (line 56).
   Remove the nested tool animation dispatch loop — nested tools are no
   longer rendered in the collapsed view. Only handle the item's own
   animation:

   ```go
   func (a *AgentToolMessageItem) Animate(msg anim.StepMsg) tea.Cmd {
       if a.result != nil || a.Status() == ToolStatusCanceled {
           return nil
       }
       if msg.ID == a.ID() {
           return a.anim.Animate(msg)
       }
       return nil
   }
   ```

   Nested tools get their own animations when loaded into a drill-in
   `Chat` instance.

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/chat/... -count=1
```

---

### Task 2: Drill-In State Model and Navigation

**Context:** `internal/ui/model/`, `internal/ui/util/`

**Files:**
- Modify: `internal/ui/model/ui.go` (drillStack, helpers, key/mouse
  handling, DrillInMsg handler, editor disable, session lifecycle guards)
- Modify: `internal/ui/model/chat.go` (HandleDelayedClick for
  DrillInHandler, return signature change)

**Steps:**

1. [ ] In `internal/ui/model/ui.go`, define the `drillInEntry` type and
   add the `drillStack` field to the `UI` struct (after the `chat *Chat`
   field, around line 224):

   ```go
   // drillInEntry represents one level of drill-in navigation.
   type drillInEntry struct {
       sessionID string
       chat      *Chat
       label     string           // breadcrumb label, cached at drill-in time
       session   *session.Session // cached session for sidebar stats
   }
   ```

   Add to `UI` struct:
   ```go
   drillStack         []drillInEntry
   elapsedTickRunning bool
   ```

2. [ ] Add helper methods on `UI`:

   ```go
   // activeChat returns the currently visible Chat.
   func (m *UI) activeChat() *Chat {
       if len(m.drillStack) > 0 {
           return m.drillStack[len(m.drillStack)-1].chat
       }
       return m.chat
   }

   // viewedSessionID returns the session ID currently being viewed.
   func (m *UI) viewedSessionID() string {
       if len(m.drillStack) > 0 {
           return m.drillStack[len(m.drillStack)-1].sessionID
       }
       if m.session != nil {
           return m.session.ID
       }
       return ""
   }

   // isDrilledIn returns whether the user is viewing a subagent.
   func (m *UI) isDrilledIn() bool {
       return len(m.drillStack) > 0
   }

   // clearDrillStack pops all drill-in entries and restores root state.
   func (m *UI) clearDrillStack() {
       m.drillStack = nil
   }
   ```

3. [ ] **CRITICAL: Comprehensive `m.chat` → `m.activeChat()` replacement.**
   Run `grep 'm\.chat\.'` in `ui.go` to find ALL ~100 references. Replace
   the ones that should operate on the visible chat. Do NOT replace ones
   that must always operate on the root chat.

   **Replace with `m.activeChat()`** — these operate on whichever chat the
   user sees:
   - `Draw()` (line 2154): `m.chat.Draw(...)` → `m.activeChat().Draw(...)`
   - `uiFocusMain` key handling (lines 2004–2090): ALL `m.chat.` calls
     (`Blur`, `ToggleExpandedSelectedItem`, `ScrollByAndAnimate`,
     `SelectedItemInView`, `SelectPrev/Next/First/Last`,
     `ScrollToSelectedAndAnimate`, `ScrollToTopAndAnimate`,
     `ScrollToBottomAndAnimate`, `HandleKeyMsg`, `Height`)
   - `DelayedClickMsg` handler (line 708): `m.chat.HandleDelayedClick(msg)`
   - `tea.MouseClickMsg` handler (line 727): `m.chat.HandleMouseDown(x, y)`
   - **Mouse motion handler** (lines ~746–763): `m.chat.ScrollByAndAnimate`,
     `m.chat.SelectedItemInView`, `m.chat.SelectPrev/Next`,
     `m.chat.HandleMouseDrag`
   - **Mouse up handler** (line ~787): `m.chat.HandleMouseUp`,
     `m.chat.HasHighlight`
   - **Mouse wheel handler** (lines ~808–827): `m.chat.ScrollByAndAnimate`,
     `m.chat.SelectedItemInView`, `m.chat.SelectPrev/Next`
   - `WindowSizeMsg` handler (line 693): `m.chat.Follow()`,
     `m.chat.ScrollToBottomAndAnimate()`
   - `anim.StepMsg` handler: see step 4 below for special handling
   - `copyChatHighlight` if it references `m.chat`

   **Keep as `m.chat`** — these always operate on the root:
   - `handleChildSessionMessage()` — updates nested tool state on root chat
   - `appendSessionMessage()` / `updateSessionMessage()` for root session
     messages (where `SessionID == m.session.ID`)
   - `handlePermissionNotification()` — search both `m.chat` AND
     `m.activeChat()` (see step 8)
   - `loadNestedToolCalls()` if it exists

   **Update `updateSize()`** (line 2562) to size ALL chats:
   ```go
   func (m *UI) updateSize() {
       m.status.SetWidth(m.layout.status.Dx())
       chatWidth := m.layout.main.Dx()
       chatHeight := m.layout.main.Dy()
       if m.isDrilledIn() {
           chatHeight -= 1 // breadcrumb line
       }
       // Size the root chat.
       m.chat.SetSize(chatWidth, m.layout.main.Dy())
       // Size all drill-in chats (active one gets breadcrumb-adjusted height).
       for i, entry := range m.drillStack {
           if i == len(m.drillStack)-1 {
               entry.chat.SetSize(chatWidth, chatHeight)
           } else {
               entry.chat.SetSize(chatWidth, m.layout.main.Dy())
           }
       }
       // ... rest of updateSize
   }
   ```

   **Update `refreshStyles()`** to invalidate drill stack caches:
   ```go
   m.chat.InvalidateRenderCaches()
   for _, entry := range m.drillStack {
       entry.chat.InvalidateRenderCaches()
   }
   ```

4. [ ] **Animation dispatch to all chats.** The `anim.StepMsg` handler
   must dispatch to ALL chats, not just the active one. Otherwise,
   animations on non-visible chats freeze and look broken when the user
   navigates back:

   ```go
   case anim.StepMsg:
       // Dispatch animation step to all chats that might have active animations.
       if cmd := m.chat.Animate(msg); cmd != nil {
           cmds = append(cmds, cmd)
       }
       for _, entry := range m.drillStack {
           if cmd := entry.chat.Animate(msg); cmd != nil {
               cmds = append(cmds, cmd)
           }
       }
   ```

   Note: `Chat.Animate()` already handles pausing off-screen animations
   via `pausedAnimations`. Non-visible chats will have all items
   "off-screen" and pause them — but their `anim.StepMsg` will still
   cycle. This is acceptable overhead (each chat does a map lookup, finds
   nothing visible, returns nil). If this becomes a perf concern, add an
   `isActive bool` flag on Chat and skip entirely when inactive.

5. [ ] Add `DrillInMsg` handler in the `Update()` switch:

   ```go
   case util.DrillInMsg:
       if msg.SessionID == "" {
           break
       }
       newChat := NewChat(m.com)
       newChat.SetSize(m.layout.main.Dx(), m.layout.main.Dy()-1) // -1 for breadcrumb
       newChat.SetFollow(true)

       m.drillStack = append(m.drillStack, drillInEntry{
           sessionID: msg.SessionID,
           chat:      newChat,
           label:     msg.Label,
       })

       // Disable the editor.
       m.textarea.Blur()
       m.focus = uiFocusMain

       // Load child session messages + session metadata asynchronously.
       cmds = append(cmds, m.loadDrillInSession(msg.SessionID))
   ```

   The `loadDrillInSession` cmd loads BOTH messages and session metadata
   (fixing C1 from review):

   ```go
   type drillInSessionLoadedMsg struct {
       sessionID string
       messages  []message.Message
       session   *session.Session
   }

   func (m *UI) loadDrillInSession(sessionID string) tea.Cmd {
       return func() tea.Msg {
           msgs, err := m.com.Workspace.ListMessages(sessionID)
           if err != nil {
               return util.ReportError(err)
           }
           sess, err := m.com.Workspace.GetSession(sessionID)
           if err != nil {
               // Non-fatal — session stats won't be available.
               return drillInSessionLoadedMsg{
                   sessionID: sessionID,
                   messages:  msgs,
               }
           }
           return drillInSessionLoadedMsg{
               sessionID: sessionID,
               messages:  msgs,
               session:   sess,
           }
       }
   }
   ```

   Handle it:
   ```go
   case drillInSessionLoadedMsg:
       for i := range m.drillStack {
           if m.drillStack[i].sessionID != msg.sessionID {
               continue
           }
           m.drillStack[i].session = msg.session

           // Convert messages to chat items. Find and reuse the existing
           // message-to-item conversion logic (search for how
           // loadSessionMessages or the session load handler converts
           // []message.Message → []chat.MessageItem).
           items := m.messagesToChatItems(msg.messages)
           m.drillStack[i].chat.SetMessages(items...)

           // Start animations.
           for _, item := range items {
               if a, ok := item.(chat.Animatable); ok {
                   if cmd := a.StartAnimation(); cmd != nil {
                       cmds = append(cmds, cmd)
                   }
               }
           }

           // Update nested tool IDs for any agent items.
           for _, item := range items {
               if ntc, ok := item.(chat.NestedToolContainer); ok {
                   if tmi, ok := item.(chat.ToolMessageItem); ok {
                       _ = ntc
                       m.drillStack[i].chat.UpdateNestedToolIDs(tmi.ToolCall().ID)
                   }
               }
           }

           if m.drillStack[i].chat.Follow() {
               m.drillStack[i].chat.ScrollToBottom()
           }
           break
       }
   ```

6. [ ] Add `←` key handling. In the `uiFocusMain` switch block, add a
   new case right before `default:` (line 2084). Since the switch uses
   `switch { case ...: }` pattern, add:

   ```go
   case m.isDrilledIn() && key.Matches(msg, m.keyMap.Chat.PillLeft):
       m.drillStack = m.drillStack[:len(m.drillStack)-1]
       if !m.isDrilledIn() {
           cmds = append(cmds, m.textarea.Focus())
           m.focus = uiFocusEditor
       }
       m.updateLayoutAndSize() // recalculate for breadcrumb height change
   ```

   No `DrillBackMsg` type needed — handle `←` directly per AGENTS.md
   guidance ("Never use commands to send messages when you can directly
   mutate children or state").

7. [ ] Modify `HandleDelayedClick` in `internal/ui/model/chat.go` (line
   591). Change return type from `bool` to `(bool, tea.Cmd)` to support
   emitting `DrillInMsg`. Before the existing `Expandable` check, add:

   ```go
   if driller, ok := selectedItem.(chat.DrillInHandler); ok {
       sessionID := driller.DrillIn()
       if sessionID != "" {
           cmd := func() tea.Msg {
               return util.DrillInMsg{
                   SessionID: sessionID,
                   Label:     driller.DrillInLabel(),
               }
           }
           return true, cmd
       }
   }
   ```

   Update the caller in `ui.go` (line 708):
   ```go
   // Before: m.chat.HandleDelayedClick(msg)
   // After:
   if _, cmd := m.activeChat().HandleDelayedClick(msg); cmd != nil {
       cmds = append(cmds, cmd)
   }
   ```

   Update all other callers of `HandleDelayedClick` to use the new
   signature.

8. [ ] **CRITICAL: Guard session lifecycle transitions.** Clear
   `drillStack` whenever the session changes to prevent stale entries:

   - In `newSession()` (search for it): add `m.clearDrillStack()` at the
     top.
   - In `loadSessionMsg` handler (search for it): add
     `m.clearDrillStack()` before loading new session data.
   - In session switch handler (from session dialog): add
     `m.clearDrillStack()`.

9. [ ] **Guard `handlePermissionNotification`** to search both root and
   active chat. Find the method (search for `handlePermissionNotification`)
   and where it calls `m.chat.MessageItem(notification.ToolCallID)`, also
   check `m.activeChat()` if the first lookup returns nil:

   ```go
   item := m.chat.MessageItem(notification.ToolCallID)
   if item == nil && m.isDrilledIn() {
       item = m.activeChat().MessageItem(notification.ToolCallID)
   }
   ```

10. [ ] Hide editor and prevent input when drilled in. In `Draw()` at line
    2147, conditionally skip editor and pills (see Task 4 for the full
    Draw rewrite with breadcrumb). In key handling:

    - `uiFocusEditor`: early-return if drilled in:
      ```go
      case uiFocusEditor:
          if m.isDrilledIn() {
              m.focus = uiFocusMain
              break
          }
      ```
    - `Tab` key (uiFocusMain → editor): skip if drilled in:
      ```go
      case key.Matches(msg, m.keyMap.Tab):
          if m.isDrilledIn() { break }
          m.focus = uiFocusEditor
          // ...
      ```

    Also adjust `generateLayout()` to give the editor's space to the main
    area when drilled in (editor height = 0, main area expands).

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 3: Pubsub Routing and Live Stats

**Context:** `internal/ui/model/ui.go`

**Files:**
- Modify: `internal/ui/model/ui.go` (pubsub routing, stats updates,
  refactored message append/update)

**Steps:**

1. [ ] **Refactor `appendSessionMessage` and `updateSessionMessage` to
   accept a `*Chat` parameter** (fixing C2 from review). These methods
   currently hardcode `m.chat`. Extract the chat-specific logic:

   ```go
   // Before: func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd
   // After:
   func (m *UI) appendSessionMessageToChat(c *Chat, msg message.Message) tea.Cmd
   ```

   Keep the original as a thin wrapper:
   ```go
   func (m *UI) appendSessionMessage(msg message.Message) tea.Cmd {
       return m.appendSessionMessageToChat(m.chat, msg)
   }
   ```

   Do the same for `updateSessionMessage` → `updateSessionMessageToChat`.

   Inside the refactored methods, replace all `m.chat.` calls with the `c`
   parameter. **Important:** guard auto-scroll to only fire when the target
   chat is the active one (fixing I2):
   ```go
   // Only auto-scroll if this chat is currently visible.
   if c == m.activeChat() && c.Follow() {
       // scroll to bottom...
   }
   ```

2. [ ] In the `pubsub.Event[message.Message]` handler (line 611), add
   a new routing branch. Place `updateAgentItemStats` BEFORE the session
   ID check so it runs for ALL child session messages:

   ```go
   case pubsub.Event[message.Message]:
       if m.session == nil {
           break
       }

       // Update stats on agent items for any child session message.
       if msg.Payload.SessionID != m.session.ID {
           m.updateAgentItemStats(msg.Payload.SessionID, msg)
       }

       // Route to drilled-in Chat when viewing a subagent.
       if m.isDrilledIn() && msg.Payload.SessionID == m.viewedSessionID() {
           switch msg.Type {
           case pubsub.CreatedEvent:
               cmds = append(cmds, m.appendSessionMessageToChat(m.activeChat(), msg.Payload))
           case pubsub.UpdatedEvent:
               cmds = append(cmds, m.updateSessionMessageToChat(m.activeChat(), msg.Payload))
           case pubsub.DeletedEvent:
               m.activeChat().RemoveMessage(msg.Payload.ID)
           }
           // Fall through to also update root chat's collapsed view.
       }

       if msg.Payload.SessionID != m.session.ID {
           if cmd := m.handleChildSessionMessage(msg); cmd != nil {
               cmds = append(cmds, cmd)
           }
           break
       }
       // ... existing root session handling unchanged
   ```

3. [ ] Implement `updateAgentItemStats`. This must search ALL chats (root
   + drill stack) to find the parent agent item (fixing C3):

   ```go
   func (m *UI) updateAgentItemStats(childSessionID string, event pubsub.Event[message.Message]) {
       _, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
       if !ok { return }

       // Search root chat and all drill stack chats for the parent item.
       item := m.chat.MessageItem(toolCallID)
       if item == nil {
           for _, entry := range m.drillStack {
               item = entry.chat.MessageItem(toolCallID)
               if item != nil { break }
           }
       }
       if item == nil { return }

       type statsUpdater interface {
           IncrementTurns()
           IncrementToolCalls(n int)
       }
       updater, ok := item.(statsUpdater)
       if !ok { return }

       // Count assistant-role message creations as turns.
       if event.Type == pubsub.CreatedEvent && event.Payload.Role() == message.Assistant {
           updater.IncrementTurns()
       }

       // Count tool calls — track already-counted IDs to avoid double-counting
       // on UpdatedEvent (tool calls appear on Created, get updated later).
       type toolIDTracker interface {
           CountedToolIDs() map[string]bool
       }
       if tracker, ok := item.(toolIDTracker); ok {
           counted := tracker.CountedToolIDs()
           newCount := 0
           for _, tc := range event.Payload.ToolCalls() {
               if !counted[tc.ID] {
                   counted[tc.ID] = true
                   newCount++
               }
           }
           if newCount > 0 {
               updater.IncrementToolCalls(newCount)
           }
       }
   }
   ```

   Add `CountedToolIDs()` method to `AgentToolMessageItem`:
   ```go
   func (a *AgentToolMessageItem) CountedToolIDs() map[string]bool {
       if a.countedToolIDs == nil {
           a.countedToolIDs = make(map[string]bool)
       }
       return a.countedToolIDs
   }
   ```

4. [ ] In `handleChildSessionMessage` (line 1212), set the child session
   ID and hasChildMessages flag on the agent item. After finding
   `agentItem` (line 1244):

   ```go
   type childSessionSetter interface {
       SetChildSessionID(string)
       SetHasChildMessages(bool)
   }
   if setter, ok := agentItem.(childSessionSetter); ok {
       setter.SetChildSessionID(childSessionID)
       setter.SetHasChildMessages(true)
   }
   ```

5. [ ] Add token/cost updates via the session pubsub path. In the
   `pubsub.Event[session.Session]` handler (line 602), update both drill
   stack entries and agent items:

   ```go
   case pubsub.Event[session.Session]:
       // Update drill stack session cache.
       for i := range m.drillStack {
           if m.drillStack[i].sessionID == msg.Payload.ID {
               s := msg.Payload
               m.drillStack[i].session = &s
           }
       }

       // Update agent item token/cost stats for child sessions.
       if m.session != nil && msg.Payload.ID != m.session.ID {
           m.updateAgentItemSessionStats(msg.Payload)
       }

       // ... existing root session handling
   ```

   ```go
   func (m *UI) updateAgentItemSessionStats(s session.Session) {
       _, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(s.ID)
       if !ok { return }
       // Search all chats.
       item := m.chat.MessageItem(toolCallID)
       if item == nil {
           for _, entry := range m.drillStack {
               item = entry.chat.MessageItem(toolCallID)
               if item != nil { break }
           }
       }
       if item == nil { return }
       type sessionStatsUpdater interface {
           SetTokens(int64)
           SetCost(float64)
       }
       if u, ok := item.(sessionStatsUpdater); ok {
           u.SetTokens(s.PromptTokens + s.CompletionTokens)
           u.SetCost(s.Cost)
       }
   }
   ```

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 4: Breadcrumb Bar

**Context:** `internal/ui/model/`

**Files:**
- Modify: `internal/ui/model/ui.go` (breadcrumb rendering, Draw
  integration)

**Steps:**

1. [ ] Add a `renderBreadcrumb` method on `UI`. Use `ansi.Truncate` for
   reliable truncation when the breadcrumb exceeds terminal width:

   ```go
   func (m *UI) renderBreadcrumb(width int) string {
       if !m.isDrilledIn() {
           return ""
       }
       t := m.com.Styles
       sep := t.Breadcrumb.Sep.Render(" > ")
       parts := []string{t.Breadcrumb.Root.Render("Main")}
       for _, entry := range m.drillStack {
           parts = append(parts, t.Breadcrumb.Label.Render(entry.label))
       }
       full := strings.Join(parts, sep)
       // Simple truncation — keep as much as fits, add ellipsis.
       if ansi.StringWidth(full) > width {
           full = ansi.Truncate(full, width-1, "…")
       }
       return full
   }
   ```

2. [ ] Integrate breadcrumb into Draw. In `Draw()` at line 2147, rewrite
   the `uiChat` case:

   ```go
   case uiChat:
       if m.isCompact {
           m.drawHeader(scr, layout.header)
       } else {
           m.drawSidebar(scr, layout.sidebar)
       }

       if m.isDrilledIn() {
           breadcrumb := m.renderBreadcrumb(layout.main.Dx())
           bcHeight := max(lipgloss.Height(breadcrumb), 1)
           bcArea := image.Rect(
               layout.main.Min.X, layout.main.Min.Y,
               layout.main.Max.X, layout.main.Min.Y+bcHeight,
           )
           uv.NewStyledString(breadcrumb).Draw(scr, bcArea)

           chatArea := image.Rect(
               layout.main.Min.X, layout.main.Min.Y+bcHeight,
               layout.main.Max.X, layout.main.Max.Y,
           )
           m.activeChat().Draw(scr, chatArea)
       } else {
           m.activeChat().Draw(scr, layout.main)
           if layout.pills.Dy() > 0 && m.pillsView != "" {
               uv.NewStyledString(m.pillsView).Draw(scr, layout.pills)
           }
           editorWidth := scr.Bounds().Dx()
           if !m.isCompact {
               editorWidth -= layout.sidebar.Dx()
           }
           editor := uv.NewStyledString(m.renderEditorView(editorWidth))
           editor.Draw(scr, layout.editor)
       }

       if m.isCompact && m.detailsOpen {
           m.drawSessionDetails(scr, layout.sessionDetails)
       }
   ```

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 5: Sidebar Enhancements and Elapsed Time Tick

**Context:** `internal/ui/model/`, `internal/ui/common/`

**Files:**
- Modify: `internal/ui/model/sidebar.go` (session-aware modelInfo)
- Modify: `internal/ui/model/ui.go` (elapsed time tick lifecycle)
- Modify: `internal/ui/common/elements.go` (formatDuration helper)

**Steps:**

1. [ ] Modify `modelInfo()` in `sidebar.go` to be session-aware. Use the
   cached session from the drill stack (no IO in Draw):

   ```go
   func (m *UI) modelInfo(width int) string {
       // Determine which session to show stats for.
       var sess *session.Session
       if m.isDrilledIn() {
           entry := m.drillStack[len(m.drillStack)-1]
           sess = entry.session
       } else {
           sess = m.session
       }
       if sess == nil {
           // Fallback to m.session for model info.
           sess = m.session
       }
       // Use sess.PromptTokens, sess.CompletionTokens, sess.Cost
       // for the ModelContextInfo instead of always m.session.
       // ... adapt existing logic to use sess
   ```

2. [ ] Add turns and tool call count below the token/cost line in
   `modelInfo`. Derive from `m.activeChat()` items:

   ```go
   turns, toolCalls := m.viewedSessionStats()
   if turns > 0 || toolCalls > 0 {
       statsLine := fmt.Sprintf("%d turns · %d tools", turns, toolCalls)
       parts = append(parts, t.ModelInfo.Stats.Render(statsLine))
   }
   ```

   ```go
   func (m *UI) viewedSessionStats() (turns, toolCalls int) {
       c := m.activeChat()
       for i := range c.Len() {
           item := c.ItemAt(i)
           if item == nil { continue }
           // Count assistant messages as turns.
           if _, ok := item.(*chat.AssistantMessageItem); ok {
               turns++
           }
           // Count tool message items as tool calls.
           if _, ok := item.(chat.ToolMessageItem); ok {
               toolCalls++
           }
       }
       return
   }
   ```

   Note: `Chat` may need an `ItemAt(i)` method exposed, or use the list
   directly. Check what's available and add a thin wrapper if needed.

3. [ ] Add elapsed time for subagent sessions. Use the agent item's
   `ToolStatus` to determine if still running (fixing I7 — no time-based
   heuristic):

   ```go
   if m.isDrilledIn() {
       entry := m.drillStack[len(m.drillStack)-1]
       if entry.session != nil {
           start := time.Unix(entry.session.CreatedAt, 0)
           // Determine if running by checking the parent agent item's status.
           isRunning := m.isViewedSubagentRunning()
           var d time.Duration
           if isRunning {
               d = time.Since(start)
           } else {
               d = time.Unix(entry.session.UpdatedAt, 0).Sub(start)
           }
           parts = append(parts, t.ModelInfo.Elapsed.Render(common.FormatDuration(d)))
       }
   }
   ```

   Add `isViewedSubagentRunning()`:
   ```go
   func (m *UI) isViewedSubagentRunning() bool {
       if !m.isDrilledIn() { return false }
       sid := m.viewedSessionID()
       _, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(sid)
       if !ok { return false }
       item := m.chat.MessageItem(toolCallID)
       if item == nil {
           // Search parent drill stack entries.
           for i := len(m.drillStack) - 2; i >= 0; i-- {
               item = m.drillStack[i].chat.MessageItem(toolCallID)
               if item != nil { break }
           }
       }
       if tmi, ok := item.(chat.ToolMessageItem); ok {
           return tmi.Status() == chat.ToolStatusRunning
       }
       return false
   }
   ```

4. [ ] Add `FormatDuration` to `internal/ui/common/elements.go`:

   ```go
   // FormatDuration formats a duration compactly: 14s, 2m30s, 1h5m.
   func FormatDuration(d time.Duration) string {
       d = d.Round(time.Second)
       if d < time.Minute {
           return fmt.Sprintf("%ds", int(d.Seconds()))
       }
       if d < time.Hour {
           m := int(d.Minutes())
           s := int(d.Seconds()) % 60
           if s == 0 {
               return fmt.Sprintf("%dm", m)
           }
           return fmt.Sprintf("%dm%ds", m, s)
       }
       h := int(d.Hours())
       m := int(d.Minutes()) % 60
       return fmt.Sprintf("%dh%dm", h, m)
   }
   ```

5. [ ] Implement the elapsed time tick. Add types and handler:

   ```go
   type tickElapsedTimeMsg struct{}

   func tickElapsedTime() tea.Cmd {
       return tea.Tick(time.Second, func(time.Time) tea.Msg {
           return tickElapsedTimeMsg{}
       })
   }
   ```

   Handler in `Update()`:
   ```go
   case tickElapsedTimeMsg:
       shouldContinue := m.hasRunningSubagents() ||
           (m.isDrilledIn() && m.isViewedSubagentRunning())
       if shouldContinue {
           cmds = append(cmds, tickElapsedTime())
       } else {
           m.elapsedTickRunning = false
       }
       // Invalidate agent item caches so elapsed time updates.
       m.invalidateRunningAgentCaches()
   ```

   Start the tick in `handleChildSessionMessage` when the first child
   message arrives for any agent:
   ```go
   if !m.elapsedTickRunning {
       m.elapsedTickRunning = true
       cmds = append(cmds, tickElapsedTime())
   }
   ```

   ```go
   func (m *UI) hasRunningSubagents() bool {
       for i := range m.chat.Len() {
           if item, ok := m.chat.ItemAt(i).(chat.ToolMessageItem); ok {
               if _, isAgent := item.(chat.NestedToolContainer); isAgent {
                   if item.Status() == chat.ToolStatusRunning {
                       return true
                   }
               }
           }
       }
       return false
   }

   func (m *UI) invalidateRunningAgentCaches() {
       for i := range m.chat.Len() {
           if item, ok := m.chat.ItemAt(i).(chat.ToolMessageItem); ok {
               if _, isAgent := item.(chat.NestedToolContainer); isAgent {
                   if item.Status() == chat.ToolStatusRunning {
                       if cached, ok := item.(interface{ ClearCache() }); ok {
                           cached.ClearCache()
                       }
                   }
               }
           }
       }
   }
   ```

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 6: Icon State Transitions

**Context:** `internal/ui/chat/`, `internal/ui/anim/`

**Files:**
- Modify: `internal/ui/chat/agent.go` (icon state logic, anim setup)
- Modify: `internal/ui/chat/tools.go` (toolHeader icon selection, if needed)

**Steps:**

1. [ ] Modify `NewAgentToolMessageItem` to use `Size: 1` for the animation
   (single-char shimmer for the Working state):

   ```go
   t.anim = anim.New(anim.Settings{
       ID:   t.ID(),
       Size: 1,
   })
   ```

   Same for `NewAgenticFetchToolMessageItem`.

2. [ ] In `AgentToolRenderContext.RenderTool()`, use the `hasChildMessages`
   field to select the icon:

   - `!hasResult && !isCanceled && !hasChildMessages`: Spawned state →
     render static `SpinnerIcon` (⋯) via `sty.Tool.IconPending`
   - `!hasResult && !isCanceled && hasChildMessages`: Working state →
     render `opts.Anim.Render()` (single-char shimmer)
   - `hasResult && isError`: Error → `ToolError` (×)
   - `hasResult && !isError`: Success → `ToolSuccess` (✓)

   Modify the `toolHeader` call or construct the icon manually before
   passing to the header builder. If `toolHeader` doesn't support custom
   icon injection, either modify it or build the header string directly
   in `RenderTool`.

3. [ ] Apply the same icon logic to `AgenticFetchToolRenderContext.RenderTool()`.

4. [ ] Ensure the `spinningFunc` still works correctly. Currently it
   returns `true` when `!HasResult() && !IsCanceled()`. The Spawned state
   uses a static icon, not the anim — but `spinningFunc` controls whether
   `baseToolMessageItem.isSpinning()` returns true, which affects
   `StartAnimation()`. The animation should still start at creation time
   (so it's ready when `hasChildMessages` flips), but the render just
   chooses which visual to show.

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/chat/... -count=1
```

---

### Task 7: Tests for Drill-In State Machine

**Context:** `internal/ui/model/`

**Files:**
- Create or modify: `internal/ui/model/drill_test.go`

**Steps:**

1. [ ] Write unit tests for the state helpers:
   - `activeChat()` returns `m.chat` when stack is empty, top entry when
     non-empty
   - `viewedSessionID()` returns root ID when empty, top entry ID when
     non-empty
   - `isDrilledIn()` reflects stack state
   - `clearDrillStack()` empties the stack

2. [ ] Write tests for breadcrumb rendering:
   - Single level: `"Main > Explorer: Search auth"`
   - Two levels: `"Main > Explorer: Search auth > Fixer: Update tests"`
   - Truncation at narrow widths
   - Empty when not drilled in

3. [ ] Write tests for `FormatDuration`:
   - Seconds: `14s`
   - Minutes+seconds: `2m30s`
   - Hours+minutes: `1h5m`
   - Edge cases: 0s, exactly 1m, exactly 1h

4. [ ] Write tests for `formatStatsLine`:
   - Zero values: `0 turns · 0 tools`
   - Normal values
   - Token formatting: k/M suffixes
   - Narrow width abbreviation

**Verify:**
```bash
go test ./internal/ui/model/... -count=1 -run TestDrill
go test ./internal/ui/common/... -count=1 -run TestFormatDuration
```

---

## Execution Order

```
Task 1 (Chat Layer: interfaces, types, collapsed rendering)
    ↓
Task 2 (Core: state model, navigation, editor disable, lifecycle guards)
    ↓
Task 3 (Pubsub routing, refactored message methods, live stats)
    ↓
Task 4 (Breadcrumb bar)  ←→  Task 5 (Sidebar + elapsed tick)  [parallel possible]
    ↓                              ↓
Task 6 (Icon state transitions)
    ↓
Task 7 (Tests)
```

Tasks 4 and 5 can potentially run in parallel — Task 4 modifies the
`Draw()` method in `ui.go`, Task 5 modifies `sidebar.go` + adds tick
handlers in `ui.go`. If parallelizing, coordinate on the `Draw()` block
to avoid conflicts.

<!-- Review notes:
Devil's advocate review caught 6 critical and 7 important issues.
All addressed:
- C1: loadDrillInSession now loads both messages and session metadata
- C2: appendSessionMessage/updateSessionMessage refactored to accept *Chat
- C3: updateAgentItemStats searches all chats (root + drill stack)
- C4: Stale drill-in messages safely ignored (entry not found in stack)
- C5: Animation dispatch to ALL chats, not just active
- C6: clearDrillStack() called on newSession, loadSession, session switch
- I1: ALL mouse handlers (motion, drag, up, wheel) included in replacement list
- I2: Auto-scroll guarded to only fire when target chat is active
- I3: Tool call counting uses countedToolIDs map to handle UpdatedEvent
- I4: DrillInLabel uses shared agentBreadcrumbLabel helper
- I5: HandleDelayedClick returns (bool, tea.Cmd)
- I6: Breadcrumb truncation uses ansi.Truncate (simple, reliable)
- I7: Elapsed time uses ToolStatus, not time heuristic
- M3: DrillBackMsg removed, ← handled directly
- M4: refreshStyles invalidates drill stack caches
- M5: Task 7 added for tests
- M6: UpdateNestedToolIDs called for drill-in chat items
- M7: handlePermissionNotification searches both chats
-->
