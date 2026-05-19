# Phase 5: Skill Attachment + Picker

> **Status:** DRAFT

## Specification

**Problem:** Users can't manually attach skill instructions to a message.
Skills are only loaded via the `skill` tool (agent-initiated) or via
command frontmatter. There's no way to browse skills and select one to
include with a message.

**Goal:** Skills can be attached as chips above the textarea (alongside
file attachments). A "Browse Skills" palette command opens a picker modal.
Skill content is sent as a context block prepended to the user message.

**Scope:**

- `SkillAttachment` type separate from `message.Attachment`.
- Skill chips rendered in the attachment bar with distinct style.
- "Browse Skills" system command in palette.
- Skill picker dialog.
- Submit integration: skill instructions prepended to user message.
- Submit blocked when only skill attachments and no message body.

**Success Criteria:**

- [ ] Selecting a skill from autocomplete shows a chip above the textarea.
- [ ] "Browse Skills" in palette opens a skill picker.
- [ ] Selecting a skill from the picker adds a chip.
- [ ] Multiple skills can be attached.
- [ ] Skills are removable via `ctrl+r` delete mode.
- [ ] Submitting with skills prepends their content to the message.
- [ ] Submitting with only skills and no text is blocked.

## Context Loading

```bash
read internal/ui/attachments/attachments.go
read internal/message/attachment.go
read internal/ui/model/ui.go    # lines 2360-2380 (submit handler)
read internal/ui/dialog/commands.go  # defaultCommands
read internal/ui/dialog/actions.go
```

## Tasks

### Task 1: Skill attachment type and storage

**Context:** `internal/ui/attachments/`

**Files:**

- Modify: `internal/ui/attachments/attachments.go`
- Create: `internal/ui/attachments/skill.go` (or extend existing file)

**Steps:**

1. [ ] Define `SkillAttachment`:
   ```go
   type SkillAttachment struct {
       Name         string
       Instructions string
       Source       string // "builtin", "user", "plugin:ce", etc.
   }
   ```
2. [ ] Extend the `Attachments` struct to hold `[]SkillAttachment`
   alongside `[]message.Attachment`:
   ```go
   type Attachments struct {
       renderer      *Renderer
       keyMap        Keymap
       list          []message.Attachment
       skills        []SkillAttachment    // New
       deleting      bool
   }
   ```
3. [ ] Add `SkillList() []SkillAttachment` and `ResetSkills()` methods.
4. [ ] Update `Reset()` to also clear skills.
5. [ ] Handle `SkillAttachment` as a `tea.Msg` in `Update()` — append
   to the `skills` slice. Guard against duplicates (same skill name).
6. [ ] Update the delete mode key handling: digit keys now index across
   the combined list (`len(list) + len(skills)`). Indices 0..N-1 are
   file attachments, N..N+M-1 are skill attachments. When deleting a
   skill attachment, remove from the `skills` slice.

**Verify:**

```bash
go test ./internal/ui/attachments/... -v
```

### Task 2: Skill chip rendering

**Context:** `internal/ui/attachments/attachments.go` (the `Renderer`)

**Files:**

- Modify: `internal/ui/attachments/attachments.go` (or `render.go` if
  rendering is in a separate file)
- Modify: `internal/ui/styles/styles.go` (add skill chip style)

**Steps:**

1. [ ] Add a `SkillStyle` to the styles alongside `ImageStyle`,
   `TextStyle`, `NormalStyle`, `DeletingStyle`. Use a distinct color
   (e.g., yellow/amber) and icon (e.g., `⚡`).
2. [ ] Extend the `Renderer` to accept `[]SkillAttachment` in addition
   to `[]message.Attachment`. Update `Render()` to iterate both lists
   and render skill chips after file chips.
3. [ ] In delete mode, number skill chips continuing from where file
   chips left off.
4. [ ] Handle overflow ("N more…") across both chip types.

**Verify:**

```bash
go build .
# Manual: attach a file and a skill, verify both render as chips
# with distinct styles. Enter delete mode, verify numbering.
```

### Task 3: Wire `ActionAttachSkill` handler

**Context:** `internal/ui/model/ui.go`

**Files:**

- Modify: `internal/ui/model/ui.go`

**Steps:**

1. [ ] Handle `ActionAttachSkill` (defined in Phase 4) in the action
   dispatch switch:
   ```go
   case dialog.ActionAttachSkill:
       m.autocomplete.Hide()
       m.textarea.SetValue("")
       return func() tea.Msg {
           return attachments.SkillAttachment{
               Name:         msg.Name,
               Instructions: msg.Instructions,
               Source:       msg.Source,
           }
       }
   ```
   This sends a `SkillAttachment` as a `tea.Msg`, which the
   `Attachments.Update()` handler receives and appends.
2. [ ] Verify the autocomplete is hidden and textarea cleared after
   skill selection.

**Verify:**

```bash
go build .
# Manual: type "/grilling" in autocomplete, select the skill.
# Verify chip appears above textarea. Type a message and submit.
```

### Task 4: Submit integration

**Context:** `internal/ui/model/ui.go` (submit handler ~L2360)

**Files:**

- Modify: `internal/ui/model/ui.go`

**Steps:**

1. [ ] In the submit handler (~L2360), after extracting file attachments
   (`m.attachments.List()`) also extract skill attachments
   (`m.attachments.SkillList()`).
2. [ ] If skill attachments exist and the textarea value is empty (no
   message body), block the submit (return nil, do not send). Skills
   are context — they need an accompanying request.
3. [ ] If skill attachments exist and there IS a message body, prepend
   skill content to the message:
   ```go
   var sb strings.Builder
   for _, skill := range skillAttachments {
       fmt.Fprintf(&sb, "I've loaded the %s skill:\n\n", skill.Name)
       fmt.Fprintf(&sb, "<skill_content name=%q>\n%s\n</skill_content>\n\n",
           skill.Name, skill.Instructions)
   }
   sb.WriteString(value) // The user's actual message.
   value = sb.String()
   ```
4. [ ] Reset skill attachments after send:
   `m.attachments.ResetSkills()` (or rely on `Reset()` if it clears
   both).
5. [ ] Pass the combined value (skills + message) to `sendMessage`.
   File attachments continue to be passed as before.

**Verify:**

```bash
go build .
# Manual: attach a skill, type a message, submit. Verify the LLM
# receives the skill content prepended to the user message.
# Also verify: attach skill only, no message → submit is blocked.
```

### Task 5: Skill picker dialog

**Context:** `internal/ui/dialog/`

**Files:**

- Create: `internal/ui/dialog/skillpicker.go`
- Modify: `internal/ui/dialog/actions.go`
- Modify: `internal/ui/dialog/commands.go` (`defaultCommands`)
- Modify: `internal/ui/model/ui.go` (handle new action)

**Steps:**

1. [ ] Create `SkillPicker` dialog implementing the `Dialog` interface:
   ```go
   const SkillPickerID = "skillpicker"

   type SkillPicker struct {
       com   *common.Common
       input textinput.Model
       list  *list.FilterableList
       skills []*skills.Skill
   }
   ```
2. [ ] `NewSkillPicker(com, skills)` — builds the filterable list from
   the active skills. Each item shows: name, description, source
   (builtin/user/plugin).
3. [ ] Implement `HandleMsg` — filter input, list navigation, Enter
   dispatches `ActionAttachSkill` with the selected skill's name and
   instructions.
4. [ ] Define `ActionSkillPickerSelected` (or reuse `ActionAttachSkill`
   directly).
5. [ ] Add "Browse Skills" to `defaultCommands()`:
   ```go
   NewCommandItem(c.com.Styles, "browse_skills", "Browse Skills", "",
       ActionOpenDialog{SkillPickerID})
   ```
6. [ ] In `ui.go`, handle `ActionOpenDialog{SkillPickerID}` by creating
   and opening the `SkillPicker` dialog with the current active skills
   list.
7. [ ] Handle the skill picker's selection action — dispatch
   `SkillAttachment` tea.Msg so it's added as a chip.

**Verify:**

```bash
go build .
# Manual: Ctrl+P → "Browse Skills" → filter → select a skill.
# Verify chip appears. Type message, submit, verify skill content.
```
