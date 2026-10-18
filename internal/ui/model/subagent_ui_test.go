package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// subagentUIWorkspace serves one running task subagent and its stored
// history to a UI with the steer and call tracker features registered.
type subagentUIWorkspace struct {
	*composerWorkspace
	messages map[string][]message.Message
	sessions map[string]session.Session
	calls    map[string]session.SubagentCall
}

func (w *subagentUIWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return messageID + "$$" + toolCallID
}

func (w *subagentUIWorkspace) ParseAgentToolSessionID(id string) (string, string, bool) {
	msg, call, ok := strings.Cut(id, "$$")
	return msg, call, ok
}

func (w *subagentUIWorkspace) ListMessages(_ context.Context, id string) ([]message.Message, error) {
	return w.messages[id], nil
}

func (w *subagentUIWorkspace) GetSession(_ context.Context, id string) (session.Session, error) {
	s, ok := w.sessions[id]
	if !ok {
		return session.Session{}, sql.ErrNoRows
	}
	return s, nil
}

func (w *subagentUIWorkspace) GetSubagentCall(_ context.Context, id string) (session.SubagentCall, error) {
	c, ok := w.calls[id]
	if !ok {
		return session.SubagentCall{}, sql.ErrNoRows
	}
	return c, nil
}

func (w *subagentUIWorkspace) AgentSubagentRunning(string) bool { return true }

func (w *subagentUIWorkspace) AgentSubagentPendingSteers(string) []string { return nil }

func (w *subagentUIWorkspace) AgentSteerSubagent(string, string, bool) error { return nil }

func (w *subagentUIWorkspace) AgentCancelSubagent(string) bool { return true }

func (w *subagentUIWorkspace) AgentTakeReturnedSteers(string) []agent.SubagentSteersReturned {
	return nil
}

func newSubagentUI(t *testing.T) (*UI, *subagentUIWorkspace) {
	t.Helper()
	u, cw := newComposerTestUI()
	ws := &subagentUIWorkspace{
		composerWorkspace: cw,
		messages:          map[string][]message.Message{},
		sessions:          map[string]session.Session{},
		calls:             map[string]session.SubagentCall{},
	}
	u.com.Workspace = ws
	u.session = &session.Session{ID: "root"}
	u.keyMap = DefaultKeyMap()
	u.status = NewStatus(u.com, u)
	u.features.register(newSubagentSteer(uiHost{u}, u.com, ws))
	u.features.register(newSubagentCalls())
	return u, ws
}

func TestDrilledInSubagentMakesRoomForTheSteerFooter(t *testing.T) {
	t.Parallel()
	u, _ := newSubagentUI(t)
	u.updateLayoutAndSize()

	u.Update(util.AgentDrillInMsg{SessionID: "m1$$c1", Label: "Worker"})
	u.updateLayoutAndSize()

	area := u.activeChatArea()
	require.Equal(t, u.layout.main.Dy()-drillInChatTopMargin-subagentSteerFooterLines, area.Dy())
	require.Equal(t, area.Dy(), u.drillStack[0].chat.Height())
	require.Contains(t, ansi.Strip(u.View().Content), "message subagent")
}

func TestRestoreToComposerAppendsToTheDraft(t *testing.T) {
	t.Parallel()
	u, _ := newSubagentUI(t)
	u.textarea.SetValue("draft")

	uiHost{u}.RestoreToComposer("returned")
	require.Equal(t, "draft\n\nreturned", u.textarea.Value())
}

func TestReloadedResumeShowsItsOwnStatsAndOwnsTheChild(t *testing.T) {
	t.Parallel()
	u, ws := newSubagentUI(t)
	child, callID := "m1$$c1", "m2$$c2"
	resume := message.Message{
		ID: "r", SessionID: child, Role: message.User, MessageType: message.MessageTypeSubagentInjection,
		Parts: message.InjectionParts([]message.InjectionContent{{
			Source: message.InjectionSourceOrchestrator, Kind: message.InjectionKindResume, Text: "more", CallID: callID,
		}}),
	}
	ws.messages[child] = []message.Message{{ID: "p", SessionID: child, Role: message.User}, resume}
	ws.sessions[child] = session.Session{ID: child, ParentSessionID: "root", Cost: 0.40, PromptTokens: 1000, CompletionTokens: 500, CreatedAt: 1000}
	ws.calls[callID] = session.SubagentCall{ID: callID, SessionID: child, Kind: session.SubagentCallResume, Cost: 0.15, CreatedAt: 2000}

	input, err := json.Marshal(agent.TaskParams{Resume: "worker-41e2", Prompt: "more", Description: "Go on"})
	require.NoError(t, err)
	assistant := &message.Message{ID: "m2", Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "c2", Name: agent.TaskToolName, Input: string(input), Finished: true},
	}}
	items := chat.ExtractMessageItems(u.com.Styles, assistant, nil, nil)
	u.loadNestedToolCalls(items)

	rendered := ansi.Strip(items[0].Render(120))
	require.Contains(t, rendered, "1.5k tokens")
	require.Contains(t, rendered, "$0.15")
	require.Equal(t, callID, u.features.calls.OwnerCall(child))
	stats := u.features.calls.CallStats(session.Session{ID: child, Cost: 0.50})
	require.InDelta(t, 0.25, stats.cost, 1e-9, "later cost counts against the resume from its loaded baseline")
}

func (w *subagentUIWorkspace) PermissionBouncerMode() permission.BouncerMode {
	var zero permission.BouncerMode
	return zero
}
