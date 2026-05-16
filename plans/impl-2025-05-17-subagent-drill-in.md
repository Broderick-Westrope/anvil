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
- Modify: `internal/ui/chat/tools.go` (add `ToolRenderOpts.StatsLine`
  field or equivalent)
- Modify: `internal/ui/styles/styles.go` (add stats line styles)

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

2. [ ] In `internal/ui/chat/agent.go`, add stats fields to
   `AgentToolMessageItem` (line 28):

   ```go
   type AgentToolMessageItem struct {
       *baseToolMessageItem
       nestedTools []ToolMessageItem

       // Live stats updated via pubsub as child session messages arrive.
       turns     int
       toolCalls int
   }
   ```

   Add the same fields to `AgenticFetchToolMessageItem` (line 220).

   Add exported setters and getters for both:
   ```go
   func (a *AgentToolMessageItem) Stats() (turns, toolCalls int) {
       return a.turns, a.toolCalls
   }
   func (a *AgentToolMessageItem) IncrementTurns() { a.turns++; a.clearCache() }
   func (a *AgentToolMessageItem) IncrementToolCalls(n int) { a.toolCalls += n; a.clearCache() }
   ```

3. [ ] Implement `DrillInHandler` on `AgentToolMessageItem`. The child
   session ID is derived from `m.com.Workspace.CreateAgentToolSessionID`
   using the tool call's parent message ID and tool call ID. However, the
   item doesn't have access to `Workspace` — so instead, store the session
   ID on the item. Add a `childSessionID string` field, set it when the
   first child session message arrives (in `handleChildSessionMessage` in
   Task 4). For now, add the field and the interface methods:

   ```go
   func (a *AgentToolMessageItem) SetChildSessionID(id string) { a.childSessionID = id }
   func (a *AgentToolMessageItem) DrillIn() string { return a.childSessionID }
   func (a *AgentToolMessageItem) DrillInLabel() string {
       var params agent.TaskParams
       _ = json.Unmarshal([]byte(a.toolCall.Input), &params)
       name := strings.Split(params.SubagentType, "-")
       for i, p := range name {
           if len(p) > 0 { name[i] = strings.ToUpper(p[:1]) + p[1:] }
       }
       label := strings.Join(name, " ")
       if label == "" { label = "Agent" }
       if params.Description != "" {
           label += ": " + params.Description
       }
       return label
   }
   ```

   Do the same for `AgenticFetchToolMessageItem`, where `DrillInLabel()`
   returns `"Fetch: " + params.Prompt` (truncated to ~40 chars) and
   `DrillIn()` returns `a.childSessionID`.

4. [ ] Implement `KeyEventHandler` on `AgentToolMessageItem` to handle
   the `→` key for drill-in. Return `true` to consume the key, along with
   a `tea.Cmd` that sends a new `DrillInMsg` (a new message type to be
   defined — just a struct with the session ID and label). The message type
   should be defined in `internal/ui/util/` alongside other message types:

   ```go
   // In internal/ui/util/messages.go (or a new file)
   type DrillInMsg struct {
       SessionID string
       Label     string
   }
   type DrillBackMsg struct{}
   ```

   The `HandleKeyEvent` on `AgentToolMessageItem`:
   ```go
   func (a *AgentToolMessageItem) HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd) {
       if key.Key().Code == tea.KeyRight && a.childSessionID != "" {
           return true, func() tea.Msg {
               return util.DrillInMsg{SessionID: a.childSessionID, Label: a.DrillInLabel()}
           }
       }
       return false, nil
   }
   ```

   Same for `AgenticFetchToolMessageItem`.

5. [ ] Rewrite `AgentToolRenderContext.RenderTool()` to produce the collapsed
   two-line format. The current implementation (lines 101–169) shows a header,
   prompt tag, and nested tool tree. Replace with:

   - **Line 1:** Status icon + display name (reuse existing `toolHeader`
     with `compact: true` — but without params, so the header is just the
     icon + name).
   - **Line 2:** Stats line: `  3 turns · 12 tools · 4.2k tokens · $0.02 · 14s`
     (indented to align under the name).
   - No prompt text, no nested tool tree, no result body.
   - The animation (`opts.Anim.Render()`) is no longer appended — the status
     icon in the header handles visual state.

   The stats line should use data from `r.agent.turns`, `r.agent.toolCalls`,
   and the session's token/cost data. Tokens and cost come from the tool
   result when available, or can be derived from the session. For now, use
   the agent item's `ToolCall()` and available data. The format function
   should handle zero values gracefully (show `0 turns · 0 tools` when
   spawned, which updates live).

   Add a helper function `formatStatsLine` in `agent.go`:
   ```go
   func formatStatsLine(sty *styles.Styles, turns, toolCalls int, tokens int64, cost float64, elapsed string, width int) string
   ```

   For narrow widths (<80), use abbreviated format:
   `3t · 12tl · 4.2k · $0.02 · 14s`.

6. [ ] Apply the same collapsed rendering treatment to
   `AgenticFetchToolRenderContext.RenderTool()`. Same two-line format:
   icon + "Agentic Fetch" (or "Fetch: <url>") on line 1, stats on line 2.

7. [ ] In `internal/ui/styles/styles.go`, add styles for the stats line
   inside the `Tool` struct. A muted/dim style for the stats text, and
   separator style for the `·` dots:

   ```go
   StatsLine    lipgloss.Style  // dim/muted for stats text
   StatsSep     lipgloss.Style  // for · separator
   ```

   Initialize them in the `New()` or theme setup function with appropriate
   muted colors.

8. [ ] Update the `Animate` method on `AgentToolMessageItem` (line 56).
   Currently it delegates to nested tools — since nested tools are no longer
   rendered in the collapsed view, the `Animate` method should only handle
   the item's own animation (for the status icon shimmer). Remove the
   nested tool animation loop. Nested tools will get their own animation
   when viewed via drill-in (they'll be in a separate `Chat` instance).

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/chat/... -count=1
```

---

### Task 2: Drill-In State Model and Navigation

**Context:** `internal/ui/model/`, `internal/ui/util/`

**Files:**
- Modify: `internal/ui/model/ui.go` (add drillStack, helpers, key/mouse
  handling, DrillInMsg/DrillBackMsg handlers, editor disable)
- Modify: `internal/ui/model/chat.go` (HandleDelayedClick for
  DrillInHandler)
- Create: `internal/ui/util/drill.go` (DrillInMsg, DrillBackMsg types)

**Steps:**

1. [ ] Create `internal/ui/util/drill.go` with the message types:

   ```go
   package util

   // DrillInMsg requests the UI to drill into a subagent session.
   type DrillInMsg struct {
       SessionID string
       Label     string
   }

   // DrillBackMsg requests the UI to navigate back from a drilled-in view.
   type DrillBackMsg struct{}
   ```

2. [ ] In `internal/ui/model/ui.go`, define the `drillInEntry` type and add
   the `drillStack` field to the `UI` struct (after the `chat *Chat` field,
   around line 224):

   ```go
   // drillInEntry represents one level of drill-in navigation into a
   // subagent session.
   type drillInEntry struct {
       sessionID string
       chat      *Chat
       label     string // breadcrumb label, e.g., "Explorer: Search auth"
   }
   ```

   Add to `UI` struct:
   ```go
   drillStack []drillInEntry
   ```

3. [ ] Add helper methods on `UI`:

   ```go
   // activeChat returns the currently visible Chat — either the top of the
   // drill stack or the root chat.
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
   ```

4. [ ] Replace direct `m.chat` references with `m.activeChat()` in the
   following locations in `ui.go`. This is critical — miss one and the
   drill-in view won't work. The key replacements are:

   **In `Draw()` (line 2154):**
   ```go
   // Before: m.chat.Draw(scr, layout.main)
   // After:
   m.activeChat().Draw(scr, layout.main)
   ```

   **In `uiFocusMain` key handling (lines 2004–2090):** Replace all
   `m.chat.` calls with `m.activeChat().`:
   - `m.chat.Blur()` → `m.activeChat().Blur()`
   - `m.chat.ToggleExpandedSelectedItem()` → `m.activeChat().ToggleExpandedSelectedItem()`
   - `m.chat.ScrollByAndAnimate(...)` → `m.activeChat().ScrollByAndAnimate(...)`
   - `m.chat.SelectedItemInView()` → `m.activeChat().SelectedItemInView()`
   - `m.chat.SelectPrev/Next/First/Last()` → `m.activeChat().Select...()`
   - `m.chat.ScrollToSelectedAndAnimate()` → etc.
   - `m.chat.ScrollToTopAndAnimate()` → etc.
   - `m.chat.ScrollToBottomAndAnimate()` → etc.
   - `m.chat.HandleKeyMsg(msg)` → `m.activeChat().HandleKeyMsg(msg)`
   - `m.chat.Height()` → `m.activeChat().Height()`

   **In mouse handling (lines 706–733):**
   - `m.chat.HandleDelayedClick(msg)` → `m.activeChat().HandleDelayedClick(msg)`
   - `m.chat.HandleMouseDown(x, y)` → `m.activeChat().HandleMouseDown(x, y)`

   **In `WindowSizeMsg` handler (line 693):**
   - `m.chat.Follow()` → `m.activeChat().Follow()`
   - `m.chat.ScrollToBottomAndAnimate()` → `m.activeChat().ScrollToBottomAndAnimate()`

   **In `anim.StepMsg` handler** (find where `m.chat.Animate(msg)` is
   called): → `m.activeChat().Animate(msg)`

   **In `updateSize()` (line 2566):**
   - `m.chat.SetSize(...)` → `m.activeChat().SetSize(...)`
   - BUT also set size on all drill stack chats when resizing:
   ```go
   m.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
   for _, entry := range m.drillStack {
       entry.chat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
   }
   ```

   **IMPORTANT:** Do NOT replace `m.chat` in:
   - `handleChildSessionMessage()` — this must always update the root chat
   - `appendSessionMessage()` / `updateSessionMessage()` / `RemoveMessage()`
     for `m.session.ID` messages — these are root session messages
   - `m.chat.InvalidateRenderCaches()` in `refreshStyles()` — also
     invalidate drill stack chats
   - `m.chat.Follow()` checks in pubsub message handler (line 632 area)
     — these are for the root session

5. [ ] Add `DrillInMsg` handler in the `Update()` switch (after existing
   message type handlers):

   ```go
   case util.DrillInMsg:
       if msg.SessionID == "" {
           break
       }
       // Create a new Chat for the drilled-in session.
       newChat := NewChat(m.com)
       newChat.SetSize(m.layout.main.Dx(), m.layout.main.Dy())
       newChat.SetFollow(true)

       m.drillStack = append(m.drillStack, drillInEntry{
           sessionID: msg.SessionID,
           chat:      newChat,
           label:     msg.Label,
       })

       // Disable the editor (blur it, switch focus to main).
       m.textarea.Blur()
       m.focus = uiFocusMain

       // Load the child session's messages asynchronously.
       cmds = append(cmds, m.loadDrillInMessages(msg.SessionID))
   ```

   Add the `loadDrillInMessages` method:
   ```go
   func (m *UI) loadDrillInMessages(sessionID string) tea.Cmd {
       return func() tea.Msg {
           msgs, err := m.com.Workspace.ListMessages(sessionID)
           if err != nil {
               return util.ReportError(err)
           }
           return drillInMessagesLoadedMsg{
               sessionID: sessionID,
               messages:  msgs,
           }
       }
   }
   ```

   Define `drillInMessagesLoadedMsg` as a private type in `ui.go`:
   ```go
   type drillInMessagesLoadedMsg struct {
       sessionID string
       messages  []message.Message
   }
   ```

   Handle it in `Update()`:
   ```go
   case drillInMessagesLoadedMsg:
       // Find the matching drill stack entry.
       for _, entry := range m.drillStack {
           if entry.sessionID != msg.sessionID {
               continue
           }
           // Convert messages to chat items and set them.
           items := m.messagesToChatItems(msg.messages)
           entry.chat.SetMessages(items...)
           // Start animations for any running tools.
           for _, item := range items {
               if a, ok := item.(chat.Animatable); ok {
                   if cmd := a.StartAnimation(); cmd != nil {
                       cmds = append(cmds, cmd)
                   }
               }
           }
           if entry.chat.Follow() {
               entry.chat.ScrollToBottom()
           }
           break
       }
   ```

   The `messagesToChatItems` method likely already exists in some form (used
   for the main session load). Find and reuse it, or extract a shared
   helper. Look for how `loadSessionMessages` or similar converts
   `[]message.Message` to `[]chat.MessageItem`.

6. [ ] Add `DrillBackMsg` handler (or handle `←` directly):

   ```go
   case util.DrillBackMsg:
       if len(m.drillStack) > 0 {
           m.drillStack = m.drillStack[:len(m.drillStack)-1]
           // Re-enable editor if back at root.
           if !m.isDrilledIn() {
               cmds = append(cmds, m.textarea.Focus())
               m.focus = uiFocusEditor
           }
       }
   ```

7. [ ] Add `←` key handling in the `uiFocusMain` switch block (lines
   2004–2090). Add a new case BEFORE the `default` branch:

   ```go
   case key.Matches(msg, m.keyMap.Chat.PillLeft):
       if m.isDrilledIn() {
           m.drillStack = m.drillStack[:len(m.drillStack)-1]
           if !m.isDrilledIn() {
               // Back at root — re-enable editor.
               cmds = append(cmds, m.textarea.Focus())
               m.focus = uiFocusEditor
           }
       } else {
           // Fall through to default for pill handling.
           if ok, cmd := m.activeChat().HandleKeyMsg(msg); ok {
               cmds = append(cmds, cmd)
           } else {
               handleGlobalKeys(msg)
           }
       }
   ```

   Wait — this conflicts with how the switch works. The `PillLeft` match
   would always match `←`. Instead, handle it in the `default` branch:

   In the `default` block (line 2084), before delegating to
   `m.chat.HandleKeyMsg`:

   ```go
   default:
       // Check for drill-back navigation.
       if m.isDrilledIn() && key.Matches(msg, m.keyMap.Chat.PillLeft) {
           m.drillStack = m.drillStack[:len(m.drillStack)-1]
           if !m.isDrilledIn() {
               cmds = append(cmds, m.textarea.Focus())
               m.focus = uiFocusEditor
           }
           break  // from the inner switch
       }
       if ok, cmd := m.activeChat().HandleKeyMsg(msg); ok {
           cmds = append(cmds, cmd)
       } else {
           handleGlobalKeys(msg)
       }
   ```

   Actually, the issue is that there's already a switch/case structure here.
   The `default` block in the existing `switch` at line 2004 uses a
   `switch { case ...: }` pattern (not `switch msg { case ...: }`), so we
   need to add a new `case` for the left-arrow-when-drilled-in condition.
   Add it right before the `default:` at line 2084:

   ```go
   case m.isDrilledIn() && key.Matches(msg, m.keyMap.Chat.PillLeft):
       m.drillStack = m.drillStack[:len(m.drillStack)-1]
       if !m.isDrilledIn() {
           cmds = append(cmds, m.textarea.Focus())
           m.focus = uiFocusEditor
       }
   ```

8. [ ] Modify `HandleDelayedClick` in `internal/ui/model/chat.go` (line
   591) to check for `DrillInHandler` before `Expandable`:

   Find the section where it calls `ToggleExpanded()` on the selected item
   after a single click. Before that check, add:

   ```go
   if driller, ok := selectedItem.(chat.DrillInHandler); ok {
       sessionID := driller.DrillIn()
       if sessionID != "" {
           return true  // or however the return works — need to emit DrillInMsg
       }
   }
   ```

   The challenge: `HandleDelayedClick` currently returns `bool`. It needs
   to return a `tea.Cmd` to emit `DrillInMsg`. Check the current signature
   and adjust. If it returns `bool`, change it to return `(bool, tea.Cmd)`
   and update the caller in `ui.go` (line 708):

   ```go
   // Before:
   m.chat.HandleDelayedClick(msg)
   // After:
   if cmd := m.activeChat().HandleDelayedClick(msg); cmd != nil {
       cmds = append(cmds, cmd)
   }
   ```

9. [ ] Hide the editor when drilled in. In the `Draw()` method at line
   2147 (`case uiChat:`), conditionally skip drawing the editor and pills:

   ```go
   case uiChat:
       if m.isCompact {
           m.drawHeader(scr, layout.header)
       } else {
           m.drawSidebar(scr, layout.sidebar)
       }

       m.activeChat().Draw(scr, layout.main)

       if !m.isDrilledIn() {
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
   ```

   Also adjust `generateLayout()` to give the editor's space to the main
   area when drilled in (so the chat viewport is taller). This means
   `layout.editor` should have zero height and `layout.main` should extend
   to fill the space. Add a parameter or check `m.isDrilledIn()` in the
   layout calculation.

10. [ ] Prevent input when drilled in. In the `uiFocusEditor` key handling
    section, early-return if drilled in to prevent typing:

    ```go
    case uiFocusEditor:
        if m.isDrilledIn() {
            // Switch to main focus — editor is disabled while viewing a subagent.
            m.focus = uiFocusMain
            break
        }
        // ... existing editor handling
    ```

    Similarly, when `Tab` key switches from main to editor focus, skip if
    drilled in:
    ```go
    case key.Matches(msg, m.keyMap.Tab):
        if m.isDrilledIn() {
            break
        }
        m.focus = uiFocusEditor
        // ...
    ```

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 3: Pubsub Routing for Drilled-In View

**Context:** `internal/ui/model/ui.go`

**Files:**
- Modify: `internal/ui/model/ui.go` (pubsub message routing, stats
  updates, child session ID tracking)

**Steps:**

1. [ ] In the `pubsub.Event[message.Message]` handler (line 611), add a
   new routing branch BEFORE the existing `SessionID != m.session.ID` check.
   When drilled in and the message matches the viewed session, route it to
   the active chat:

   ```go
   case pubsub.Event[message.Message]:
       if m.session == nil {
           break
       }

       // Route messages to the drilled-in Chat when viewing a subagent.
       if m.isDrilledIn() && msg.Payload.SessionID == m.viewedSessionID() {
           switch msg.Type {
           case pubsub.CreatedEvent:
               cmds = append(cmds, m.appendDrillInMessage(msg.Payload))
           case pubsub.UpdatedEvent:
               cmds = append(cmds, m.updateDrillInMessage(msg.Payload))
           case pubsub.DeletedEvent:
               m.activeChat().RemoveMessage(msg.Payload.ID)
           }
           // Auto-scroll if following.
           if m.activeChat().Follow() {
               if cmd := m.activeChat().ScrollToBottomAndAnimate(); cmd != nil {
                   cmds = append(cmds, cmd)
               }
               m.activeChat().SelectLast()
           }
           // Fall through — also update the root chat's collapsed view
           // via handleChildSessionMessage below.
       }

       if msg.Payload.SessionID != m.session.ID {
           if cmd := m.handleChildSessionMessage(msg); cmd != nil {
               cmds = append(cmds, cmd)
           }
           break
       }
       // ... existing root session handling
   ```

   The `appendDrillInMessage` and `updateDrillInMessage` methods mirror
   `appendSessionMessage` and `updateSessionMessage` but operate on
   `m.activeChat()` instead of `m.chat`. Extract the shared logic or
   create thin wrappers.

2. [ ] In `handleChildSessionMessage` (line 1212), set the
   `childSessionID` on the agent item when first encountered. After finding
   `agentItem` (line 1244), add:

   ```go
   // Set the child session ID on the agent item for drill-in navigation.
   if setter, ok := agentItem.(interface{ SetChildSessionID(string) }); ok {
       setter.SetChildSessionID(childSessionID)
   }
   ```

3. [ ] In `handleChildSessionMessage`, update the stats tallies. After
   updating nested tools (line 1289), increment counts:

   ```go
   // Update live stats on the agent item.
   // Count new tool calls added this update.
   newToolCalls := len(event.Payload.ToolCalls())
   // Only count genuinely new ones (not updates to existing).
   existingIDs := make(map[string]bool)
   for _, t := range previousNestedTools {
       existingIDs[t.ToolCall().ID] = true
   }
   newCount := 0
   for _, tc := range event.Payload.ToolCalls() {
       if !existingIDs[tc.ID] { newCount++ }
   }
   ```

   For turn counting: increment when an assistant-role message is first
   created (not updated). This requires checking `event.Type ==
   pubsub.CreatedEvent` and `event.Payload.Role == "assistant"`. But
   `handleChildSessionMessage` currently filters to tool-call/result
   messages only (line 1216). The turn counting must happen elsewhere — in
   the new drill-in routing branch (step 1), or via a separate counter.

   Better approach: maintain turn and tool counts on the agent item, updated
   from the drill-in pubsub routing. When a message arrives for a child
   session:
   - If it's a `CreatedEvent` with role `assistant` → increment turns
   - If it has new tool calls → increment tool call count

   This logic should run regardless of whether the user is drilled in.
   Add a new method `updateAgentStats` called from both the drill-in
   routing and `handleChildSessionMessage`:

   ```go
   func (m *UI) updateAgentItemStats(childSessionID string, event pubsub.Event[message.Message]) {
       _, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(childSessionID)
       if !ok { return }
       item := m.chat.MessageItem(toolCallID)
       if item == nil { return }

       type statsUpdater interface {
           IncrementTurns()
           IncrementToolCalls(n int)
       }
       updater, ok := item.(statsUpdater)
       if !ok { return }

       if event.Type == pubsub.CreatedEvent && event.Payload.Role() == message.Assistant {
           updater.IncrementTurns()
       }
       newToolCalls := len(event.Payload.ToolCalls())
       if newToolCalls > 0 && event.Type == pubsub.CreatedEvent {
           updater.IncrementToolCalls(newToolCalls)
       }
   }
   ```

   Call this from the top of the `pubsub.Event[message.Message]` handler,
   for ALL child session messages (not just when drilled in):

   ```go
   if msg.Payload.SessionID != m.session.ID {
       m.updateAgentItemStats(msg.Payload.SessionID, msg)
   }
   ```

4. [ ] Add token/cost data to the stats line. The session's
   `PromptTokens`, `CompletionTokens`, and `Cost` are available on the
   `session.Session` struct. When the collapsed view renders, it needs
   these. Options:
   - Store them on the agent item (updated via `pubsub.Event[session.Session]`
     handler at line 602).
   - Query the session from the DB.

   Use the pubsub session update path. In the
   `pubsub.Event[session.Session]` handler (line 602), when the session
   update is for a child session (not `m.session.ID`), update the agent
   item's token/cost data. Add `tokens int64` and `cost float64` fields
   to `AgentToolMessageItem` with setters. Update them when the child
   session's pubsub event arrives:

   ```go
   case pubsub.Event[session.Session]:
       // ... existing root session handling ...

       // Update child session stats on agent items.
       if msg.Payload.ID != m.session.ID {
           m.updateAgentItemSessionStats(msg.Payload)
       }
   ```

   ```go
   func (m *UI) updateAgentItemSessionStats(s session.Session) {
       _, toolCallID, ok := m.com.Workspace.ParseAgentToolSessionID(s.ID)
       if !ok { return }
       item := m.chat.MessageItem(toolCallID)
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
- Modify: `internal/ui/model/ui.go` (breadcrumb rendering + draw
  integration)

**Steps:**

1. [ ] Add a `renderBreadcrumb` method on `UI` that builds the breadcrumb
   string from the drill stack:

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
       // Truncate if too wide — keep the last segments visible.
       if ansi.StringWidth(full) > width {
           // Show "… > last two entries" at minimum.
           for len(parts) > 2 {
               parts = append([]string{parts[0], t.Breadcrumb.Sep.Render("…")}, parts[len(parts)-1:]...)
               full = strings.Join(parts, sep)
               if ansi.StringWidth(full) <= width { break }
               parts = parts[1:]  // drop from front
           }
       }
       return full
   }
   ```

2. [ ] Add breadcrumb styles to `internal/ui/styles/styles.go`:

   ```go
   Breadcrumb struct {
       Root  lipgloss.Style  // "Main" label style
       Label lipgloss.Style  // subagent label style
       Sep   lipgloss.Style  // ">" separator style
       Bar   lipgloss.Style  // background bar style
   }
   ```

   Initialize with appropriate colors — Root bold, Label normal, Sep muted.

3. [ ] Integrate breadcrumb into the Draw pipeline. In `Draw()` at line
   2147, when drilled in, draw the breadcrumb above the chat area. Adjust
   the layout to allocate one line for the breadcrumb:

   ```go
   case uiChat:
       if m.isCompact {
           m.drawHeader(scr, layout.header)
       } else {
           m.drawSidebar(scr, layout.sidebar)
       }

       if m.isDrilledIn() {
           // Draw breadcrumb at top of main area.
           breadcrumb := m.renderBreadcrumb(layout.main.Dx())
           bcHeight := lipgloss.Height(breadcrumb)
           bcArea := image.Rect(
               layout.main.Min.X, layout.main.Min.Y,
               layout.main.Max.X, layout.main.Min.Y+bcHeight,
           )
           uv.NewStyledString(breadcrumb).Draw(scr, bcArea)

           // Draw chat below breadcrumb.
           chatArea := image.Rect(
               layout.main.Min.X, layout.main.Min.Y+bcHeight,
               layout.main.Max.X, layout.main.Max.Y,
           )
           m.activeChat().Draw(scr, chatArea)
       } else {
           m.activeChat().Draw(scr, layout.main)
       }
       // ... rest of draw
   ```

   Note: The chat's `SetSize` in `updateSize()` should account for the
   breadcrumb height when drilled in. Alternatively, just draw into the
   sub-rectangle and let the chat render at whatever size it gets. The
   simplest approach is to adjust the draw area at render time (as above)
   and set the chat size to match:

   In `updateSize()`:
   ```go
   chatHeight := m.layout.main.Dy()
   if m.isDrilledIn() {
       chatHeight -= 1 // breadcrumb line
   }
   m.activeChat().SetSize(m.layout.main.Dx(), chatHeight)
   ```

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/... -count=1
```

---

### Task 5: Sidebar Enhancements and Elapsed Time

**Context:** `internal/ui/model/`, `internal/ui/common/`

**Files:**
- Modify: `internal/ui/model/sidebar.go` (extended stats, session-aware)
- Modify: `internal/ui/model/ui.go` (elapsed time tick lifecycle)
- Modify: `internal/ui/common/elements.go` (new helper functions)

**Steps:**

1. [ ] Modify `modelInfo()` in `sidebar.go` (line 17) to be session-aware.
   When drilled in, show the subagent's model, tokens, and cost instead of
   the main session's. The subagent's session can be loaded from the DB:

   ```go
   func (m *UI) modelInfo(width int) string {
       // Determine which session's data to show.
       var tokens int64
       var cost float64
       if m.isDrilledIn() {
           // For drilled-in sessions, use cached stats from the agent item.
           // Or load the session from the DB.
           sid := m.viewedSessionID()
           if s, err := m.com.Workspace.GetSession(sid); err == nil {
               tokens = s.PromptTokens + s.CompletionTokens
               cost = s.Cost
           }
       } else if m.session != nil {
           tokens = m.session.PromptTokens + m.session.CompletionTokens
           cost = m.session.Cost
       }
       // ... rest of model info rendering, using tokens and cost
   ```

   Wait — `GetSession` would be I/O in `View`/`Draw`, which violates the
   "never do IO in Update" rule (and Draw is even worse). Instead, cache
   the subagent session data. When drill-in happens, load the session and
   store it. When session update pubsub events arrive for the viewed
   session, update the cached data.

   Better approach: Add a `viewedSession *session.Session` field that's
   populated when drilling in (from the `loadDrillInMessages` cmd) and
   updated via the `pubsub.Event[session.Session]` handler:

   ```go
   // In drillInEntry, add:
   type drillInEntry struct {
       sessionID string
       chat      *Chat
       label     string
       session   *session.Session // cached session for sidebar stats
   }
   ```

   In the session pubsub handler:
   ```go
   case pubsub.Event[session.Session]:
       // Update drill stack entries if applicable.
       for i := range m.drillStack {
           if m.drillStack[i].sessionID == msg.Payload.ID {
               s := msg.Payload
               m.drillStack[i].session = &s
           }
       }
       // ... existing handling
   ```

   In `drillInMessagesLoadedMsg` handler, also load and cache the session:
   ```go
   // Include the session in the loaded message.
   type drillInMessagesLoadedMsg struct {
       sessionID string
       messages  []message.Message
       session   *session.Session
   }
   ```

   Then `modelInfo` reads from `m.drillStack[top].session` when drilled in.

2. [ ] Add turns and tool call count to the sidebar `modelInfo` output.
   Below the token/cost line, add:

   ```go
   // After the context info...
   if turnsCount > 0 || toolCallCount > 0 {
       statsLine := fmt.Sprintf("%d turns · %d tools", turnsCount, toolCallCount)
       parts = append(parts, t.ModelInfo.Stats.Render(statsLine))
   }
   ```

   For the root session, derive these from the chat items. For subagent
   sessions, derive from the drilled-in chat items or the cached agent item
   stats. Add a helper:

   ```go
   func (m *UI) viewedSessionStats() (turns, toolCalls int) {
       chat := m.activeChat()
       for i := range chat.Len() {
           item, ok := chat.list.ItemAt(i).(chat.MessageItem)
           if !ok { continue }
           // Count assistant messages as turns.
           if _, ok := item.(*chat.AssistantMessageItem); ok {
               turns++
           }
           // Count tool calls.
           if tmi, ok := item.(chat.ToolMessageItem); ok {
               _ = tmi
               toolCalls++
           }
       }
       return
   }
   ```

3. [ ] Add elapsed time for subagent sessions. When viewing a subagent,
   show elapsed time below the stats. Use `session.CreatedAt` and either
   `time.Now()` (running) or `session.UpdatedAt` (completed):

   ```go
   if m.isDrilledIn() {
       entry := m.drillStack[len(m.drillStack)-1]
       if entry.session != nil {
           elapsed := m.formatElapsedTime(entry.session)
           parts = append(parts, t.ModelInfo.Elapsed.Render(elapsed))
       }
   }
   ```

   ```go
   func (m *UI) formatElapsedTime(s *session.Session) string {
       start := time.Unix(s.CreatedAt, 0)
       var d time.Duration
       // Check if session is still active (heuristic: no completion yet,
       // or last update was very recent).
       // For now, use UpdatedAt if it differs from CreatedAt by a meaningful amount,
       // otherwise use time.Now().
       end := time.Unix(s.UpdatedAt, 0)
       if time.Since(end) < 5*time.Second {
           // Likely still running — use now.
           d = time.Since(start)
       } else {
           d = end.Sub(start)
       }
       return formatDuration(d)
   }

   func formatDuration(d time.Duration) string {
       d = d.Round(time.Second)
       if d < time.Minute {
           return fmt.Sprintf("%ds", int(d.Seconds()))
       }
       if d < time.Hour {
           m := int(d.Minutes())
           s := int(d.Seconds()) % 60
           return fmt.Sprintf("%dm%ds", m, s)
       }
       h := int(d.Hours())
       m := int(d.Minutes()) % 60
       return fmt.Sprintf("%dh%dm", h, m)
   }
   ```

4. [ ] Implement the elapsed time tick. Add a `tickElapsedTimeMsg` type
   and the tick command:

   ```go
   type tickElapsedTimeMsg struct{}

   func tickElapsedTime() tea.Cmd {
       return tea.Tick(time.Second, func(time.Time) tea.Msg {
           return tickElapsedTimeMsg{}
       })
   }
   ```

   Handle it in `Update()`:
   ```go
   case tickElapsedTimeMsg:
       // Re-render the sidebar/stats for elapsed time updates.
       // Re-schedule if any subagent is still running.
       if m.hasRunningSubagents() || (m.isDrilledIn() && m.isViewedSubagentRunning()) {
           cmds = append(cmds, tickElapsedTime())
       }
       // Invalidate cached renders on running agent items so stats line updates.
       m.invalidateRunningAgentCaches()
   ```

   Start the tick when a subagent spawns (in `handleChildSessionMessage`
   when the first message arrives for a new agent tool session):
   ```go
   // If this is the first message for this agent, start the elapsed tick.
   if !m.elapsedTickRunning {
       m.elapsedTickRunning = true
       cmds = append(cmds, tickElapsedTime())
   }
   ```

   Add `elapsedTickRunning bool` to `UI` struct. Set it to false in the
   `tickElapsedTimeMsg` handler when no running subagents remain.

   Add helper:
   ```go
   func (m *UI) hasRunningSubagents() bool {
       for i := range m.chat.Len() {
           item, ok := m.chat.list.ItemAt(i).(chat.ToolMessageItem)
           if !ok { continue }
           if _, ok := item.(chat.NestedToolContainer); !ok { continue }
           if item.Status() == chat.ToolStatusRunning {
               return true
           }
       }
       return false
   }
   ```

5. [ ] Add new styles for sidebar stats and elapsed time to
   `styles/styles.go`:

   ```go
   // Inside ModelInfo struct:
   Stats   lipgloss.Style  // for "3 turns · 12 tools" line
   Elapsed lipgloss.Style  // for elapsed time
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
- Modify: `internal/ui/chat/agent.go` (icon state logic)
- Modify: `internal/ui/chat/tools.go` (toolHeader icon selection)

**Steps:**

1. [ ] The current icon states use `ToolPending` (●) while running and
   `ToolSuccess`/`ToolError` when done. The spec requires:
   - **Spawned:** MiniDot braille spinner (⠋⠙⠹...)
   - **Working:** Single-char shimmer (`anim.Anim` Size:1)
   - **Done:** ✓ or ×

   The transition from Spawned → Working happens when the first child
   session message arrives. Track this on the agent item:

   ```go
   // Add to AgentToolMessageItem:
   hasChildMessages bool
   ```

   Set `hasChildMessages = true` in `handleChildSessionMessage` when
   updating the agent item (alongside `SetChildSessionID`).

2. [ ] Modify the animation setup. Currently, `newBaseToolMessageItem`
   creates an `anim.Anim` with default settings. For agent tool items,
   override with `Size: 1`:

   In `NewAgentToolMessageItem`:
   ```go
   t.anim = anim.New(anim.Settings{
       ID:   t.ID(),
       Size: 1,
   })
   ```

   This gives the single-char shimmer for the Working state.

3. [ ] For the Spawned state (before any child messages), use a
   `spinner.MiniDot`. Add a `spinner spinner.Model` field to
   `AgentToolMessageItem`. Initialize it in the constructor. The spinner
   needs to tick — when the item starts animation, start the spinner.

   Actually, the existing `anim.Anim` is already used for the spinning
   state. The distinction between Spawned and Working is about WHICH
   animation plays. Simplest approach: use the existing `anim.Anim` for
   both states but change the rendered character set. Or use a conditional
   in the render:

   In `RenderTool`, when rendering the status icon:
   ```go
   if !opts.HasResult() && !opts.IsCanceled() {
       if r.agent.hasChildMessages {
           // Working state — single-char shimmer (from anim.Anim Size:1)
           icon = opts.Anim.Render()
       } else {
           // Spawned state — braille spinner
           icon = sty.Tool.IconPending.Render(styles.SpinnerIcon)
           // Or use the MiniDot spinner character
       }
   }
   ```

   The simplest implementation: use the existing spinner icon (⋯) for
   spawned state and the `anim.Anim` shimmer for working state. The
   `toolHeader` function already handles the icon based on status — modify
   it to accept an extra parameter or have the agent render context supply
   the icon directly.

4. [ ] Apply the same icon state logic to `AgenticFetchToolMessageItem`.

**Verify:**
```bash
go build ./internal/ui/...
go test ./internal/ui/chat/... -count=1
```

---

## Execution Order

```
Task 1 (Chat Layer: interfaces, types, collapsed rendering)
    ↓
Task 2 (Core: state model, navigation, editor disable)
    ↓
Task 3 (Pubsub routing, stats updates)
    ↓
Task 4 (Breadcrumb bar)  ←→  Task 5 (Sidebar + elapsed tick)  [parallel possible]
    ↓                              ↓
Task 6 (Icon state transitions)  — last, depends on stats fields from Task 3
```

Tasks 4 and 5 touch different files (breadcrumb is in ui.go Draw,
sidebar is in sidebar.go) but Task 5's tick handler is in ui.go. If
parallelizing, ensure Task 5's ui.go changes don't conflict with Task 4's
ui.go Draw changes.

<!-- Review notes: TBD — will be populated after devil's advocate review -->
