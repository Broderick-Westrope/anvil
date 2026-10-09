package model

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/csync"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/list"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

// These tests drive the UI through a small stand-in for the Bubble Tea
// runtime: commands run on their own goroutines and their messages are fed
// back through Update, followed by View, exactly as tea.Program does. They
// only observe Update traffic and rendered output, so they hold regardless of
// how spinners are scheduled internally.

// animWorkspace reports a busy agent and maps one child session to the
// agent tool that owns it.
type animWorkspace struct {
	*composerWorkspace
	busy     atomic.Bool
	children map[string][2]string // child session -> {message ID, tool call ID}
	drill    []message.Message
}

func (w *animWorkspace) AgentIsReady() bool { return true }
func (w *animWorkspace) AgentIsBusy() bool  { return w.busy.Load() }

func (w *animWorkspace) ParseAgentToolSessionID(sessionID string) (string, string, bool) {
	ids, ok := w.children[sessionID]
	return ids[0], ids[1], ok
}

func (*animWorkspace) CreateAgentToolSessionID(messageID, toolCallID string) string {
	return messageID + "$$" + toolCallID
}

func (w *animWorkspace) ListMessages(_ context.Context, sessionID string) ([]message.Message, error) {
	if _, ok := w.children[sessionID]; ok {
		return w.drill, nil
	}
	return nil, nil
}

func (*animWorkspace) ListUserMessages(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (*animWorkspace) ListUserMessagesByWorkingDir(context.Context, string) ([]message.Message, error) {
	return nil, nil
}

func (*animWorkspace) FileTrackerListReadFiles(context.Context, string) ([]string, error) {
	return nil, nil
}

// animRuntime is a minimal tea.Program: one goroutine owns the model and
// every command runs on its own goroutine.
type animRuntime struct {
	t  *testing.T
	u  *UI
	ch chan tea.Msg

	// inflight counts command goroutines that have not returned, which is
	// how a still-armed timer shows up.
	inflight atomic.Int64
	panics   atomic.Int64
	panicMsg atomic.Value

	mu      sync.Mutex
	updates map[string]int
	total   int
	sample  func()
}

func newAnimRuntime(t *testing.T, u *UI) *animRuntime {
	return &animRuntime{t: t, u: u, ch: make(chan tea.Msg, 1024), updates: map[string]int{}}
}

func (r *animRuntime) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	r.inflight.Add(1)
	go func() {
		defer r.inflight.Add(-1)
		defer func() {
			if p := recover(); p != nil {
				r.panics.Add(1)
				r.panicMsg.Store(fmt.Sprintf("%v\n%s", p, debug.Stack()))
			}
		}()
		r.route(cmd())
	}()
}

func (r *animRuntime) route(msg tea.Msg) {
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			r.exec(c)
		}
		return
	}
	// tea.Sequence produces an unexported []tea.Cmd; run it in order.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeFor[tea.Cmd]() {
		for i := range v.Len() {
			c, _ := v.Index(i).Interface().(tea.Cmd)
			if c != nil {
				r.route(c())
			}
		}
		return
	}
	r.ch <- msg
}

// send delivers msg through Update and View on the calling goroutine.
func (r *animRuntime) send(msg tea.Msg) {
	r.t.Helper()
	_, cmd := r.u.Update(msg)
	_ = r.u.View()
	r.exec(cmd)
}

// run processes messages for d and returns how many Updates ran, by type.
func (r *animRuntime) run(d time.Duration) map[string]int {
	r.t.Helper()
	counts := map[string]int{}
	deadline := time.After(d)
	for {
		select {
		case msg := <-r.ch:
			counts[fmt.Sprintf("%T", msg)]++
			_, cmd := r.u.Update(msg)
			_ = r.u.View()
			if r.sample != nil {
				r.sample()
			}
			r.exec(cmd)
		case <-deadline:
			if n := r.panics.Load(); n > 0 {
				r.t.Fatalf("%d command(s) panicked; last: %v", n, r.panicMsg.Load())
			}
			return counts
		}
	}
}

func sum(counts map[string]int) int {
	n := 0
	for _, c := range counts {
		n += c
	}
	return n
}

func describe(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, counts[k])
	}
	return b.String()
}

// spinScene builds a session with eight independent spinners: a thinking
// assistant message, four bash calls still streaming their input, an agent
// call awaiting its result, and two tool calls inside that agent.
type spinScene struct {
	u  *UI
	ws *animWorkspace
	r  *animRuntime
}

const (
	sceneSession = "s1"
	sceneChild   = "child-1"
	sceneAgentTC = "tc-agent"
	sceneToolsID = "a-tools"
)

var sceneBashIDs = []string{"tc-b1", "tc-b2", "tc-b3", "tc-b4"}

var sceneNestedIDs = []string{"tc-n1", "tc-n2"}

func newSpinScene(t *testing.T) *spinScene {
	t.Helper()
	u, cw := newComposerTestUI()
	ws := &animWorkspace{
		composerWorkspace: cw,
		children:          map[string][2]string{sceneChild: {sceneToolsID, sceneAgentTC}},
	}
	ws.busy.Store(true)
	u.com.Workspace = ws
	u.session = &session.Session{ID: sceneSession}
	u.state = uiChat
	u.keyMap = DefaultKeyMap()
	u.status = NewStatus(u.com, u)
	u.updateLayoutAndSize()
	return &spinScene{u: u, ws: ws, r: newAnimRuntime(t, u)}
}

func created(sessionID string, msg message.Message) pubsub.Event[message.Message] {
	msg.SessionID = sessionID
	return pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: msg}
}

func updated(sessionID string, msg message.Message) pubsub.Event[message.Message] {
	msg.SessionID = sessionID
	return pubsub.Event[message.Message]{Type: pubsub.UpdatedEvent, Payload: msg}
}

func sceneToolsMessage(finished bool) message.Message {
	parts := make([]message.ContentPart, 0, len(sceneBashIDs)+1)
	for _, id := range sceneBashIDs {
		parts = append(parts, message.ToolCall{ID: id, Name: tools.BashToolName, Input: `{"command":"sleep 1"}`, Finished: finished})
	}
	parts = append(parts, message.ToolCall{ID: sceneAgentTC, Name: agent.TaskToolName, Input: `{"description":"x","prompt":"y"}`, Finished: true})
	return message.Message{ID: sceneToolsID, Role: message.Assistant, Parts: parts}
}

func sceneNestedMessage(finished bool) message.Message {
	parts := make([]message.ContentPart, 0, len(sceneNestedIDs))
	for _, id := range sceneNestedIDs {
		parts = append(parts, message.ToolCall{ID: id, Name: tools.BashToolName, Input: `{"command":"ls"}`, Finished: finished})
	}
	return message.Message{ID: "child-a1", Role: message.Assistant, Parts: parts}
}

// start puts all eight spinners on screen through the normal event path.
func (s *spinScene) start() {
	s.r.send(created(sceneSession, message.Message{ID: "u1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}))
	s.r.send(created(sceneSession, sceneToolsMessage(false)))
	s.r.send(created(sceneChild, sceneNestedMessage(false)))
	s.r.send(created(sceneSession, message.Message{ID: "a-think", Role: message.Assistant}))
}

// finish stops every spinner the way a completed turn does.
func (s *spinScene) finish() {
	s.ws.busy.Store(false)
	s.r.send(updated(sceneChild, sceneNestedMessage(true)))
	s.r.send(updated(sceneSession, sceneToolsMessage(true)))
	s.r.send(created(sceneSession, message.Message{
		ID: "tool-results", Role: message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: sceneAgentTC, Name: agent.TaskToolName, Content: "done"}},
	}))
	s.r.send(updated(sceneSession, message.Message{
		ID: "a-think", Role: message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "all done"}, message.Finish{Reason: message.FinishReasonEndTurn}},
	}))
}

// renders tracks the distinct renders of the scene's spinners.
type renders map[string]map[string]struct{}

func (s *spinScene) spinners(c *Chat) map[string]list.Item {
	out := map[string]list.Item{}
	if item := c.MessageItem("a-think"); item != nil {
		out["a-think"] = item
	}
	for _, id := range append(append([]string{}, sceneBashIDs...), sceneNestedIDs...) {
		if item := c.MessageItem(id); item != nil {
			out[id] = item
		}
	}
	if item := c.MessageItem(sceneAgentTC); item != nil {
		out[sceneAgentTC] = item
		if container, ok := item.(chat.NestedToolContainer); ok {
			for _, nested := range container.NestedTools() {
				out[nested.ToolCall().ID] = nested
			}
		}
	}
	return out
}

func (s *spinScene) track(c func() *Chat) renders {
	seen := renders{}
	s.r.sample = func() {
		for id, item := range s.spinners(c()) {
			if seen[id] == nil {
				seen[id] = map[string]struct{}{}
			}
			seen[id][item.Render(100)] = struct{}{}
		}
	}
	return seen
}

// awaitQuiet runs until no message arrives for a full frame-free window and
// every command goroutine has returned, failing if that never happens.
func (r *animRuntime) awaitQuiet(timeout time.Duration, ignore ...string) {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		counts := r.run(250 * time.Millisecond)
		for _, k := range ignore {
			delete(counts, k)
		}
		if sum(counts) == 0 && r.inflight.Load() == 0 {
			return
		}
	}
	r.t.Fatalf("UI never went quiet: %d command goroutine(s) still running", r.inflight.Load())
}

// TestAnimation_SpinnersAnimate pins that every kind of spinner keeps
// moving: assistant, streaming tool calls, the agent item and the tools
// nested inside it.
func TestAnimation_SpinnersAnimate(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	seen := s.track(func() *Chat { return s.u.chat })
	s.start()
	s.r.run(600 * time.Millisecond)

	ids := append([]string{"a-think", sceneAgentTC}, sceneBashIDs...)
	ids = append(ids, sceneNestedIDs...)
	for _, id := range ids {
		require.GreaterOrEqual(t, len(seen[id]), 3, "spinner %s should animate", id)
	}
}

// TestAnimation_StopsWhenNothingSpins pins that finishing a turn leaves no
// timer behind: the UI goes quiet and every command goroutine returns.
func TestAnimation_StopsWhenNothingSpins(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	s.start()
	s.r.run(300 * time.Millisecond)

	s.finish()
	s.r.awaitQuiet(3*time.Second, "model.tickElapsedTimeMsg")
}

// TestAnimation_GhostSpinnerStaysStill pins the reload guard: a session
// killed mid-turn has an assistant message with no Finish part, which still
// reports spinning. Reloading it while the agent is idle must not animate it.
func TestAnimation_GhostSpinnerStaysStill(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	s.ws.busy.Store(false)
	seen := s.track(func() *Chat { return s.u.chat })

	s.r.exec(s.u.setSessionMessages([]message.Message{
		{ID: "u1", SessionID: sceneSession, Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}},
		{ID: "a-think", SessionID: sceneSession, Role: message.Assistant},
	}))
	s.r.send(tea.FocusMsg{})
	s.r.awaitQuiet(2 * time.Second)
	require.LessOrEqual(t, len(seen["a-think"]), 1, "ghost spinner must not animate")
}

// TestAnimation_ScrolledBackIntoView pins that a spinner scrolled out of
// view stops costing frames and resumes once it is visible again.
func TestAnimation_ScrolledBackIntoView(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	s.r.send(created(sceneSession, message.Message{ID: "a-think", Role: message.Assistant}))
	for i := range 40 {
		s.r.send(created(sceneSession, message.Message{
			ID: fmt.Sprintf("u%d", i), Role: message.User,
			Parts: []message.ContentPart{message.TextContent{Text: "filler"}},
		}))
	}
	s.u.chat.ScrollToBottom()
	s.r.run(300 * time.Millisecond)
	counts := s.r.run(time.Second)
	require.LessOrEqual(t, sum(counts), 3, "off-screen spinner should not drive frames: %s", describe(counts))

	seen := s.track(func() *Chat { return s.u.chat })
	s.u.focus = uiFocusMain
	s.r.send(tea.KeyPressMsg{Code: tea.KeyHome})
	s.r.run(600 * time.Millisecond)
	require.GreaterOrEqual(t, len(seen["a-think"]), 3, "spinner should resume once visible")
}

// TestAnimation_DrillIn pins the drilled-in view: the subagent's spinners
// animate, the tick rate stays bounded (the instance gate once fixed a storm
// here that grew every frame), and backing out resumes the root spinners.
func TestAnimation_DrillIn(t *testing.T) {
	t.Parallel()
	s := newSpinScene(t)
	s.start()
	s.ws.drill = []message.Message{sceneNestedMessage(false)}
	s.ws.drill[0].SessionID = sceneChild

	s.r.send(drillIn(sceneChild))
	s.r.run(200 * time.Millisecond)
	require.True(t, s.u.isDrilledIn())
	seen := s.track(func() *Chat { return s.u.activeChat() })

	first := s.r.run(500 * time.Millisecond)
	second := s.r.run(500 * time.Millisecond)
	for _, id := range sceneNestedIDs {
		require.GreaterOrEqual(t, len(seen[id]), 3, "drilled-in spinner %s should animate", id)
	}
	require.LessOrEqual(t, sum(second), sum(first)*3/2+3,
		"tick rate must not grow: first %s second %s", describe(first), describe(second))

	s.r.send(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.False(t, s.u.isDrilledIn())
	seen = s.track(func() *Chat { return s.u.chat })
	s.r.run(600 * time.Millisecond)
	require.GreaterOrEqual(t, len(seen["a-think"]), 3, "root spinner should animate after backing out")
}

func drillIn(sessionID string) tea.Msg {
	return util.DrillInMsg{SessionID: sessionID, Label: "agent"}
}

func (*animWorkspace) AgentQueuedPrompts(string) int          { return 0 }
func (*animWorkspace) AgentQueuedPromptsList(string) []string { return nil }
func (w *animWorkspace) AgentIsSessionBusy(string) bool       { return w.busy.Load() }

func (*animWorkspace) AgentModel() workspace.AgentModel { return workspace.AgentModel{} }

func (*animWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{}, Providers: csync.NewMap[string, config.ProviderConfig]()}
}

func (*animWorkspace) PermissionBouncerMode() permission.BouncerMode { return permission.BouncerOff }
