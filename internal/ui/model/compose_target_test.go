package model

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

// openSubagent drills into a read-only session, such as a subagent's.
func (f *workflowFixture) openSubagent() {
	f.u.drillStack = append(f.u.drillStack, drillInEntry{sessionID: "child", chat: NewChat(f.u.com)})
	f.u.updateLayoutAndSize()
}

func TestBranchingIsOnlyForTheUsersRootChat(t *testing.T) {
	t.Parallel()

	const refused = "Branching is only available in the root chat."
	tests := map[string]struct {
		setup func(f *workflowFixture)
		want  string
	}{
		"a subagent's session": {
			setup: func(f *workflowFixture) { f.u.session.ParentSessionID = "parent" },
			want:  refused,
		},
		"a drill-in over the user's session": {
			setup: (*workflowFixture).openSubagent,
			want:  refused,
		},
		"a workflow session the user talks to": {
			setup: func(f *workflowFixture) {
				f.u.session.ParentSessionID, f.u.session.WorkflowKind = "parent", session.WorkflowKindSession
			},
			want: "Select a user or assistant message to branch from.",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			tc.setup(f)
			infos := f.run(f.u.branchFromSelectedMessage())
			require.Len(t, infos, 1)
			require.Equal(t, tc.want, infos[0].Msg)
		})
	}
}

func TestPromptHistoryWhileABuilderLoadsIsTheUsersOwn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.drillStack = append(f.u.drillStack, drillInEntry{sessionID: "sess-2", chat: NewChat(f.u.com), conversational: true})
	f.ws.historyFor = nil

	f.run(f.u.loadPromptHistory())
	require.Equal(t, []string{"sess-1"}, f.ws.historyFor)
}

func TestOpeningAReadOnlySessionLeavesThePromptHistoryAlone(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.historyFor = nil
	_, cmd := f.u.Update(util.AgentDrillInMsg{SessionID: "msg$$agent1", Label: "explorer"})
	f.run(cmd)

	require.Len(t, f.u.drillStack, 1)
	require.Empty(t, f.ws.historyFor)
}

func TestTheTodoSpinnerFollowsOnlyTheUsersSession(t *testing.T) {
	t.Parallel()

	working := []session.Todo{{Content: "a", Status: session.TodoStatusInProgress}}
	waiting := []session.Todo{{Content: "a", Status: session.TodoStatusPending}}
	tests := map[string]struct {
		todos []session.Todo
		busy  map[string]bool
		want  bool
	}{
		"the user's session works on a todo":      {todos: working, busy: map[string]bool{"sess-1": true}, want: true},
		"the user's session works with none":      {todos: waiting, busy: map[string]bool{"sess-1": true}},
		"only a builder works on the user's todo": {todos: working, busy: map[string]bool{"sess-2": true}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			f.u.session.Todos = tc.todos
			f.ws.busy = tc.busy
			user := message.Message{ID: "u-1", SessionID: "sess-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "go"}}}
			_, _ = f.u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: user})

			require.Equal(t, tc.want, f.u.todoIsSpinning)
		})
	}
}

func TestAWorkflowNodeArrivingScrollsOnlyAFollowingChat(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"following": true, "scrolled up": false}
	for name, follow := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			f.u.chat.SetMessages(scrollTestItems(200)...)
			f.u.updateLayoutAndSize()
			f.u.chat.ScrollToBottom()
			if !follow {
				f.u.chat.ScrollBy(-10)
			}
			before := f.u.chat.list.Offset()

			f.markerArrives()
			if follow {
				require.True(t, f.u.chat.AtBottom(), "the new node is in view")
				return
			}
			require.Equal(t, before, f.u.chat.list.Offset())
			require.False(t, f.u.chat.Follow())
		})
	}
}

func TestTheReturnToBranchHintNeedsAPendingBranchAndAnIdleUserSession(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		pending bool
		busy    map[string]bool
		want    bool
	}{
		"no pending branch":                          {},
		"a pending branch":                           {pending: true, want: true},
		"a pending branch, a builder works":          {pending: true, busy: map[string]bool{"sess-2": true}, want: true},
		"a pending branch, the user's session works": {pending: true, busy: map[string]bool{"sess-1": true}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			f.u.focus = uiFocusEditor
			f.ws.busy = tc.busy
			if tc.pending {
				f.u.pendingBranch = &branchReturnSnapshot{sessionID: "sess-1", leafID: "old"}
			}
			const hint = "return to previous branch"
			require.Equal(t, tc.want, strings.Contains(fmt.Sprint(f.u.ShortHelp()), hint))
			require.Equal(t, tc.want, strings.Contains(fmt.Sprint(f.u.FullHelp()), hint))
		})
	}
}

// todosInFlight are todos with work left, so the pills have something to
// show.
var todosInFlight = []session.Todo{
	{Content: "a", Status: session.TodoStatusInProgress},
	{Content: "b", Status: session.TodoStatusPending},
	{Content: "c", Status: session.TodoStatusPending},
}

func TestThePillsOpenOnlyWhereTheEditorIs(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.session.Todos = todosInFlight
	f.u.togglePillsExpanded()
	require.True(t, f.u.pillsExpanded)
	require.Equal(t, pillSectionTodos, f.u.focusedPillSection)

	f.u.togglePillsExpanded()
	f.openSubagent()
	f.u.togglePillsExpanded()
	require.False(t, f.u.pillsExpanded, "a read-only drill-in has no pills")
}

func TestThePillSectionsSwitchOnlyWhileOpen(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.session.Todos = todosInFlight
	f.u.promptQueue = 2
	f.u.focusedPillSection = pillSectionQueue

	f.u.switchPillSection(-1)
	require.Equal(t, pillSectionQueue, f.u.focusedPillSection, "closed pills don't switch")

	f.u.pillsExpanded = true
	f.u.switchPillSection(-1)
	require.Equal(t, pillSectionTodos, f.u.focusedPillSection)

	f.openSubagent()
	f.u.switchPillSection(1)
	require.Equal(t, pillSectionTodos, f.u.focusedPillSection, "a read-only drill-in has no pills")
}

func TestOpenTodoPillsMakeRoomForEveryTodo(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.session.Todos = todosInFlight
	closed := f.u.pillsAreaHeight()
	require.Positive(t, closed)

	f.u.pillsExpanded = true
	f.u.focusedPillSection = pillSectionTodos
	require.Equal(t, closed+len(todosInFlight), f.u.pillsAreaHeight())
}

func TestABuildersPillsAreDrawn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.width, f.u.height = 100, 30
	f.drillIntoBuilder(t)
	f.ws.anyBusy = true
	f.ws.queued = map[string]int{"sess-2": 2}
	f.press(tea.KeyPressMsg{Code: 'x'})

	f.u.header = newHeader(f.u.com)
	f.u.status = NewStatus(f.u.com, f.u)
	scr := uv.NewScreenBuffer(f.u.width, f.u.height)
	f.u.Draw(scr, scr.Bounds())
	require.Contains(t, ansi.Strip(scr.Render()), "2 Queued")
}
