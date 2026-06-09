# Phase 1: Foundation

> Config field, message type, MCP state enum.

## Context Loading

```bash
read internal/config/config.go
read internal/message/content.go
read internal/message/message.go
read internal/message/tree.go
read internal/agent/tools/mcp/init.go
read internal/proto/mcp.go
```

## Tasks

### Task 1: Add `lazy_description` to `MCPConfig`

**Context:** `internal/config/`

**Files:**
- Modify: `internal/config/config.go` (add field to `MCPConfig`)
- Test: `internal/config/config_test.go` (or new file)

**Steps:**

1. [ ] Add `LazyDescription string \`json:"lazy_description,omitempty"\`` field
   to the `MCPConfig` struct at `config.go:209`
2. [ ] Add a helper method `func (m MCPConfig) IsLazy() bool` that returns
   `m.LazyDescription != ""`
3. [ ] Add test verifying:
   - Config without `lazy_description` parses normally (`IsLazy() == false`)
   - Config with `lazy_description` parses and `IsLazy() == true`
   - Empty string `lazy_description` is treated as non-lazy

**Verify:**
```bash
go test ./internal/config/ -run TestLazy
```

### Task 2: Add `MessageTypeMCPToggle` message type

**Context:** `internal/message/`

**Files:**
- Modify: `internal/message/content.go` (add constant + content struct)
- Modify: `internal/message/message.go` (add JSON unmarshal case)
- Modify: `internal/message/tree.go` (update `FilterMetadataMessage`)
- Test: `internal/message/tree_test.go` or new file

**Steps:**

1. [ ] Add `MessageTypeMCPToggle MessageType = "mcp_toggle"` to the constants
   block at `content.go:45`
2. [ ] Add an `MCPToggleContent` struct with `ServerName string` and
   `Enabled bool` fields. Add a JSON unmarshal case for
   `MessageTypeMCPToggle` in the content deserialization switch at
   `message.go:781-787`, following the `ModelChangeContent` /
   `ThinkingLevelChangeContent` pattern. This is required for session
   restore to correctly deserialize toggle messages from the DB
3. [ ] Update `FilterMetadataMessage` at `tree.go:88` to return `nil` for
   `MessageTypeMCPToggle` — these messages should not be sent to the LLM as
   conversation context
4. [ ] Add test verifying:
   - `FilterMetadataMessage` strips `MCPToggle` messages
   - `MCPToggleContent` round-trips through JSON marshal/unmarshal
   - A message with `MessageTypeMCPToggle` and `MCPToggleContent` can be
     created and its content deserialized correctly

**Verify:**
```bash
go test ./internal/message/ -run TestMCPToggle
```

### Task 3: Add `StateIdle` to MCP state enum

**Context:** `internal/agent/tools/mcp/`, `internal/proto/`

**Files:**
- Modify: `internal/agent/tools/mcp/init.go` (add `StateIdle` to iota)
- Modify: `internal/proto/mcp.go` (add `MCPStateIdle` to iota)

**Steps:**

1. [ ] Add `StateIdle` to the `State` iota block at `init.go:66`. Place it
   after `StateError` (at the end) to avoid shifting `StateError`'s numeric
   value from 3 to 4, which could break any numeric comparisons:
   `StateDisabled`, `StateStarting`, `StateConnected`, `StateError`,
   `StateIdle`
2. [ ] Add `MCPStateIdle` to `proto.MCPState` at `proto/mcp.go:11` in the
   matching position (after `MCPStateError`)
3. [ ] Search for all switch statements and numeric comparisons on `State`
   (`grep -rn 'State' internal/agent/tools/mcp/ internal/proto/`). Add
   `StateIdle` cases where needed. If any `String()` method exists for
   `State`, add the `"idle"` case
4. [ ] Extend `ClientInfo` (`mcp/init.go:117`) with an `IsLazy bool` field.
   This field is set from config during `updateState` or `initClient` when
   the MCP has `lazy_description` set. The UI uses this to determine whether
   a connected MCP should render as idle
5. [ ] If a `String()` method exists, add test verifying
   `StateIdle.String() == "idle"`

**Verify:**
```bash
go build ./internal/agent/tools/mcp/ && go build ./internal/proto/
go test ./internal/agent/tools/mcp/ -run TestState
```
