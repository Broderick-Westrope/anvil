package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/stretchr/testify/require"
)

// stubAgent is a minimal chat item that satisfies MessageItem, NestedToolContainer,
// and ToolMessageItem so it can be placed in a Chat for testing
// invalidateRunningAgentsInChat. Embedding *list.Versioned allows callers to
// detect ClearItemCaches calls by comparing Version() before and after.
type stubAgent struct {
	*list.Versioned
	id      string
	running bool // true → ToolStatusRunning; false → ToolStatusSuccess
}

// Compile-time assertions: detection in invalidateRunningAgentsInChat relies
// on these interface checks passing, so a silently failing type assertion
// (e.g. after an interface change upstream) must break the build, not the
// test's semantics.
var (
	_ chat.ToolMessageItem     = (*stubAgent)(nil)
	_ chat.NestedToolContainer = (*stubAgent)(nil)
)

func newStubAgent(id string, running bool) *stubAgent {
	return &stubAgent{
		Versioned: list.NewVersioned(),
		id:        id,
		running:   running,
	}
}

// list.Item
func (s *stubAgent) Render(width int) string { return "" }
func (s *stubAgent) Finished() bool          { return !s.running }

// list.RawRenderable
func (s *stubAgent) RawRender(width int) string { return "" }

// chat.Identifiable
func (s *stubAgent) ID() string { return s.id }

// chat.ToolMessageItem
func (s *stubAgent) ToolCall() message.ToolCall {
	return message.ToolCall{Finished: !s.running}
}
func (s *stubAgent) SetToolCall(_ message.ToolCall)  {}
func (s *stubAgent) SetResult(_ *message.ToolResult) {}
func (s *stubAgent) MessageID() string               { return s.id }
func (s *stubAgent) SetMessageID(_ string)           {}
func (s *stubAgent) SetStatus(_ chat.ToolStatus)     {}
func (s *stubAgent) HasResult() bool                 { return !s.running }
func (s *stubAgent) Status() chat.ToolStatus {
	if s.running {
		return chat.ToolStatusRunning
	}
	return chat.ToolStatusSuccess
}

// chat.NestedToolContainer
func (s *stubAgent) NestedTools() []chat.ToolMessageItem     { return nil }
func (s *stubAgent) SetNestedTools(_ []chat.ToolMessageItem) {}
func (s *stubAgent) AddNestedTool(_ chat.ToolMessageItem)    {}

// chatWithStubs builds a Chat populated with the given stub items.
func chatWithStubs(t *testing.T, stubs ...*stubAgent) *Chat {
	t.Helper()
	c := NewChat(nil)
	items := make([]chat.MessageItem, len(stubs))
	for i, s := range stubs {
		items[i] = s
	}
	c.AppendMessages(items...)
	return c
}

// versionOf returns the current version of the stub.
func versionOf(s *stubAgent) uint64 { return s.Version() }

// TestInvalidateRunningAgentsInChat_NoRunningAgents asserts that a chat
// containing only finished agents triggers no ClearItemCaches calls and
// the function returns false.
func TestInvalidateRunningAgentsInChat_NoRunningAgents(t *testing.T) {
	t.Parallel()

	finished1 := newStubAgent("a", false)
	finished2 := newStubAgent("b", false)
	c := chatWithStubs(t, finished1, finished2)

	v1Before := versionOf(finished1)
	v2Before := versionOf(finished2)

	found := invalidateRunningAgentsInChat(c)

	require.False(t, found, "expected no running agents to be found")
	require.Equal(t, v1Before, versionOf(finished1), "version of finished agent should not change")
	require.Equal(t, v2Before, versionOf(finished2), "version of finished agent should not change")
}

// TestInvalidateRunningAgentsInChat_WithRunningAgent asserts that a chat
// containing a running agent has its cache invalidated and the function
// returns true.
func TestInvalidateRunningAgentsInChat_WithRunningAgent(t *testing.T) {
	t.Parallel()

	running := newStubAgent("run", true)
	finished := newStubAgent("fin", false)
	c := chatWithStubs(t, running, finished)

	vRunBefore := versionOf(running)
	vFinBefore := versionOf(finished)

	found := invalidateRunningAgentsInChat(c)

	require.True(t, found, "expected running agent to be found")
	require.Greater(t, versionOf(running), vRunBefore, "running agent version should be bumped (cache cleared)")
	require.Equal(t, vFinBefore, versionOf(finished), "finished agent version must not change")
}

// TestInvalidateRunningAgentCaches_AllFinished asserts that when all chats
// contain only finished agents the method returns false and no cache
// invalidation occurs.
func TestInvalidateRunningAgentCaches_AllFinished(t *testing.T) {
	t.Parallel()

	fin := newStubAgent("f1", false)
	c := chatWithStubs(t, fin)
	ui := &UI{chat: c}

	vBefore := versionOf(fin)
	found := ui.invalidateRunningAgentCaches()

	require.False(t, found)
	require.Equal(t, vBefore, versionOf(fin), "no cache invalidation expected when no running agents")
}

// TestInvalidateRunningAgentCaches_WithRunningAgent asserts that when a chat
// contains a running agent the method returns true and bumps the agent's version.
func TestInvalidateRunningAgentCaches_WithRunningAgent(t *testing.T) {
	t.Parallel()

	run := newStubAgent("r1", true)
	c := chatWithStubs(t, run)
	ui := &UI{chat: c}

	vBefore := versionOf(run)
	found := ui.invalidateRunningAgentCaches()

	require.True(t, found)
	require.Greater(t, versionOf(run), vBefore, "running agent version should be bumped")
}

// TestInvalidateRunningAgentCaches_DrillStack asserts that running agents in
// drill-stack chats are detected and invalidated even when the root chat has
// none.
func TestInvalidateRunningAgentCaches_DrillStack(t *testing.T) {
	t.Parallel()

	rootFin := newStubAgent("root-fin", false)
	rootChat := chatWithStubs(t, rootFin)

	drillRun := newStubAgent("drill-run", true)
	drillChat := chatWithStubs(t, drillRun)

	ui := &UI{
		chat:       rootChat,
		drillStack: []drillInEntry{{chat: drillChat}},
	}

	vRootBefore := versionOf(rootFin)
	vDrillBefore := versionOf(drillRun)

	found := ui.invalidateRunningAgentCaches()

	require.True(t, found, "running agent in drill stack should be detected")
	require.Equal(t, vRootBefore, versionOf(rootFin), "root finished agent must not be invalidated")
	require.Greater(t, versionOf(drillRun), vDrillBefore, "drill-stack running agent should be bumped")
}

// TestInvalidateRunningAgentsInChat_EmptyChat asserts that an empty chat
// returns false without panic.
func TestInvalidateRunningAgentsInChat_EmptyChat(t *testing.T) {
	t.Parallel()

	c := chatWithStubs(t)
	found := invalidateRunningAgentsInChat(c)
	require.False(t, found)
}
