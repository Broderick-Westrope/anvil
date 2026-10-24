package model

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/chat"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
)

func runningView() workflowhost.RunView {
	return workflowhost.RunView{
		RunID: "run-1", InvokingSessionID: "sess-1", Command: "demo",
		Input: "track downloads", Status: workflowhost.StatusRunning, StartedAt: time.Unix(0, 0),
		Phases: []workflowhost.RunPhase{
			{Step: "build", Detail: "approach 1/3", Items: []workflowhost.RunItem{
				{Kind: workflowhost.RunItemSession, SessionID: "sess-2", Title: "/demo approach 1", Status: workflowhost.StatusCompleted},
			}},
			{Step: "devils-advocate", Detail: "round 1/5", Items: []workflowhost.RunItem{
				{Kind: workflowhost.RunItemAgent, SessionID: "da-1", Agent: "devils-advocate", Model: "gpt-6", Status: workflowhost.StatusRunning},
				{Kind: workflowhost.RunItemAsk, Question: "Which repo?", Answer: "customer-frontend", Status: workflowhost.StatusCompleted},
			}},
		},
	}
}

// markerArrives delivers the run's marker as a new message in the invoking
// session.
func (f *workflowFixture) markerArrives() {
	marker := message.Message{
		ID: "marker-1", SessionID: "sess-1", Role: message.System, MessageType: message.MessageTypeWorkflowRun,
		Parts: []message.ContentPart{message.WorkflowRunContent{RunID: "run-1", Command: "demo", Input: "track downloads"}},
	}
	_, cmd := f.u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: marker})
	f.run(cmd)
}

func (f *workflowFixture) node(t *testing.T) *chat.WorkflowRunMessageItem {
	t.Helper()
	node, ok := f.u.chat.MessageItem("marker-1").(*chat.WorkflowRunMessageItem)
	require.True(t, ok, "the marker must show as the run's node")
	return node
}

func TestWorkflowRunNodeShowsWhereTheRunIsUpTo(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	out := ansi.Strip(f.node(t).Render(100))
	require.Contains(t, out, "Workflow  /demo track downloads")
	require.Contains(t, out, "devils-advocate · round 1/5 · running")
}

func TestWorkflowRunNodeFollowsTheRunsEvents(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	done := runningView()
	done.Status, done.Error = workflowhost.StatusFailed, "boom"
	done.FinishedAt = done.StartedAt.Add(90 * time.Second)
	f.ws.views["run-1"] = done
	f.event(workflowhost.Event{Kind: workflowhost.EventFinished, RunID: "run-1", Command: "demo", SessionID: "sess-1", Status: workflowhost.StatusFailed})

	out := ansi.Strip(f.node(t).Render(100))
	require.Contains(t, out, "failed: boom · 1m30s")
	require.True(t, f.node(t).Finished())
}

func TestWorkflowRunNodeDrillsIntoTheRunViewAndOnIntoItsSessions(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	f.u.focus = uiFocusMain
	f.u.chat.Focus()
	f.u.chat.SetSelected(f.u.chat.Len() - 1)

	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 1)
	view := f.u.drillStack[0]
	require.Equal(t, "run-1", view.runID)
	require.Equal(t, "/demo", view.label)

	var rows []string
	for _, item := range view.chat.MessageItems() {
		rows = append(rows, ansi.Strip(item.Render(100)))
	}
	require.Len(t, rows, 6)
	require.Contains(t, rows[0], "Workflow  /demo track downloads")
	require.Contains(t, rows[1], "● build · approach 1/3")
	require.Contains(t, rows[2], "  ◆ /demo approach 1 · session")
	require.NotContains(t, rows[2], "✓", "a session row's marker is its only icon")
	require.Contains(t, rows[3], "● devils-advocate · round 1/5")
	require.Contains(t, rows[4], "/demo: devils-advocate gpt-6 · running")
	require.Contains(t, rows[5], "  ? Which repo? → customer-frontend")
	require.NotContains(t, rows[5], "●", "a question's marker is its only icon")

	view.chat.SetSelected(4)
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)
	require.Equal(t, "da-1", f.u.drillStack[1].sessionID)
	require.Equal(t, "/demo: devils-advocate", f.u.drillStack[1].label)
}

func TestWorkflowRunViewKeepsItsSelectionWhenTheRunMovesOn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.SetSelected(2)

	next := runningView()
	next.Phases = append(next.Phases, workflowhost.RunPhase{Step: "review", Detail: "pass 1/3"})
	f.ws.views["run-1"] = next
	f.event(workflowhost.Event{Kind: workflowhost.EventChanged, RunID: "run-1", Command: "demo", SessionID: "sess-1"})

	require.Equal(t, 7, view.Len())
	require.Equal(t, 2, view.Selected())
	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "review · pass 1/3 · running")
}

func TestWorkflowRunNodeSaysWhenTheRunIsWaitingForYou(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	view := runningView()
	view.Phases[1].Items[1].Status, view.Phases[1].Items[1].Answer = workflowhost.StatusRunning, ""
	f.ws.views = map[string]workflowhost.RunView{"run-1": view}
	f.markerArrives()

	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "devils-advocate · round 1/5 · waiting for you")
}

func TestWorkflowRunViewLoadsNeverLetAnOlderViewWin(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	older := f.u.loadWorkflowRunView("run-1")
	require.NotNil(t, older)
	stale := older()
	next := runningView()
	next.Phases = append(next.Phases, workflowhost.RunPhase{Step: "review"})
	f.ws.views["run-1"] = next
	require.Nil(t, f.u.loadWorkflowRunView("run-1"), "a second load waits for the one in flight")

	_, cmd := f.u.Update(stale)
	f.run(cmd)
	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "review · running")
	require.Empty(t, f.u.workflowRuns.loads)
}

func TestWorkflowRunNodeIsFoundByItsRunWhateverMessageCarriesIt(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	// A fork copies the marker under a new message ID.
	copied := message.Message{
		ID: "copy-1", SessionID: "sess-1", Role: message.System, MessageType: message.MessageTypeWorkflowRun,
		Parts: []message.ContentPart{message.WorkflowRunContent{RunID: "run-1", Command: "demo"}},
	}
	_, cmd := f.u.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: copied})
	f.run(cmd)

	node, ok := f.u.chat.MessageItem("copy-1").(*chat.WorkflowRunMessageItem)
	require.True(t, ok)
	require.True(t, node.HasView())
}

func TestWorkflowRunViewShowsCallsLeftOpenByAnInterruptedRun(t *testing.T) {
	t.Parallel()

	sty := newTestUI().com.Styles
	view := runningView()
	view.Status = workflowhost.StatusInterrupted
	view.Phases[1].Items[0].Status = workflowhost.StatusInterrupted
	view.Phases[1].Items[1] = workflowhost.RunItem{Kind: workflowhost.RunItemAsk, Question: "Which repo?", Status: workflowhost.StatusInterrupted}

	var rows []string
	for _, item := range chat.BuildWorkflowRunItems(sty, "demo", view, func(string) bool { return false }) {
		rows = append(rows, ansi.Strip(item.Render(100)))
	}
	require.Contains(t, rows[4], "/demo: devils-advocate gpt-6 · interrupted")
	require.Contains(t, rows[5], "? Which repo? → unanswered (interrupted)")
}

func TestWorkflowRunNodeShowsWhatACompletedRunReturned(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	done := runningView()
	done.Status, done.FinishedAt = workflowhost.StatusCompleted, done.StartedAt.Add(time.Minute)
	done.Result = []byte(`{
  "outcome": "stopped",
  "reason": "cap hit"
}`)
	f.ws.views = map[string]workflowhost.RunView{"run-1": done}
	f.markerArrives()

	require.Contains(t, ansi.Strip(f.node(t).Render(200)), `completed: {"outcome":"stopped","reason":"cap hit"} · 1m0s`)
}

func TestWorkflowRunViewKeepsItsScrollWhenTheRunMovesOn(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	long := runningView()
	for i := range 30 {
		long.Phases = append(long.Phases, workflowhost.RunPhase{Step: "round", Detail: time.Duration(i).String()})
	}
	f.ws.views = map[string]workflowhost.RunView{"run-1": long}
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	require.Zero(t, view.list.Offset(), "a run view opens at its top")
	view.ScrollBy(5)
	view.SetSelected(8)
	before := view.list.Offset()

	more := long
	more.Phases = append(append([]workflowhost.RunPhase{}, long.Phases...), workflowhost.RunPhase{Step: "review"})
	f.ws.views["run-1"] = more
	f.event(workflowhost.Event{Kind: workflowhost.EventChanged, RunID: "run-1", Command: "demo", SessionID: "sess-1"})

	require.Equal(t, len(more.Phases)+6-2, view.Len())
	require.Equal(t, before, view.list.Offset())
	require.Equal(t, 8, view.Selected())
}

func TestWorkflowRunNodeShowsTheRunsTotalCost(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	view := runningView()
	view.Cost = 0.4231
	f.ws.views = map[string]workflowhost.RunView{"run-1": view}
	f.markerArrives()

	require.Regexp(t, `round 1/5 · running · \S+ · \$0\.42`, ansi.Strip(f.node(t).Render(120)))
}

// drillIntoBuilder opens the run view and then the builder's session, the
// way the user would.
func (f *workflowFixture) drillIntoBuilder(t *testing.T) {
	t.Helper()
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.Focus()
	view.SetSelected(2)
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)
	require.True(t, f.u.drillStack[1].conversational)
}

func (f *workflowFixture) typeAndSend(t *testing.T, text string) {
	t.Helper()
	f.press(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, uiFocusEditor, f.u.focus, "the editor takes focus")
	f.u.textarea.SetValue(text)
	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func TestTypingInADrilledInBuilderSendsToTheBuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	require.True(t, f.u.composeTarget().shown, "a builder can be talked to")

	f.typeAndSend(t, "use the shared analytics hook")
	require.Equal(t, []string{"use the shared analytics hook"}, f.ws.runs)
	require.Equal(t, []string{"sess-2"}, f.ws.runSessions)
	require.Equal(t, "sess-2", f.u.currentComposerSignal().sessionID, "background job wakes treat the builder as the open session")
}

func TestASubagentSessionStaysReadOnly(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	view := f.u.drillStack[0].chat
	view.Focus()
	view.SetSelected(4)
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyRight})
	require.Len(t, f.u.drillStack, 2)

	require.False(t, f.u.composeTarget().shown)
	f.press(tea.KeyPressMsg{Code: tea.KeyTab})
	require.Equal(t, uiFocusMain, f.u.focus)
	require.Equal(t, "sess-1", f.u.currentComposerSignal().sessionID, "the user is still in their own session for job wakes")
}

func TestNothingIsSentToABuilderThatHasntLoaded(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.drillStack = append(f.u.drillStack, drillInEntry{sessionID: "sess-2", chat: NewChat(f.u.com), conversational: true})
	f.u.updateLayoutAndSize()

	infos := f.run(f.u.sendMessage("hello"))
	require.Empty(t, f.ws.runs)
	require.Len(t, infos, 1)
	require.Contains(t, infos[0].Msg, "still loading")
}

func TestDraftsStayWithTheSessionTheyWereTypedFor(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.textarea.SetValue("for my own session")
	f.drillIntoBuilder(t)
	require.Empty(t, f.u.textarea.Value(), "the root draft isn't shown to the builder")

	f.u.textarea.SetValue("for the builder")
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, "", f.u.textarea.Value(), "the run view has no editor")
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, "for my own session", f.u.textarea.Value())
}

func TestCommandsArentRunFromABuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.typeAndSend(t, "/demo again")

	require.Empty(t, f.ws.starts)
	require.Empty(t, f.ws.runs)
	require.Equal(t, "/demo again", f.u.textarea.Value())
}

func TestTextThatOnlyLooksLikeACommandIsSentToABuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.typeAndSend(t, "/src/app.ts is failing")

	require.Equal(t, []string{"/src/app.ts is failing"}, f.ws.runs)
	require.Equal(t, []string{"sess-2"}, f.ws.runSessions)
}

func TestWorkflowControlKeysActOnTheRunInView(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()

	// From the session that started it, without selecting anything.
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Equal(t, []string{"pause:run-1"}, f.ws.controls)

	paused := runningView()
	paused.Paused = true
	f.ws.views["run-1"] = paused
	f.event(workflowhost.Event{Kind: workflowhost.EventState, RunID: "run-1", Command: "demo", SessionID: "sess-1", State: workflowhost.StatePaused})
	require.Contains(t, ansi.Strip(f.node(t).Render(120)), "round 1/5 · paused")

	// From the run view, the same key resumes it.
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Equal(t, []string{"pause:run-1", "resume:run-1", "abort:run-1"}, f.ws.controls)

	target, ok := uiWorkflowViews{f.u}.TargetRun()
	require.True(t, ok, "the palette's controls act on the run in view")
	require.Equal(t, "run-1", target.RunID)
	require.True(t, target.Paused)
}

func TestWorkflowControlKeysIgnoreAFinishedRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	done := runningView()
	done.Status = workflowhost.StatusCompleted
	f.ws.views = map[string]workflowhost.RunView{"run-1": done}
	f.markerArrives()

	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	require.Empty(t, f.ws.controls)
	_, ok := uiWorkflowViews{f.u}.TargetRun()
	require.False(t, ok, "the palette offers no controls either")
}

func TestADrilledInBuilderMakesRoomForTheEditor(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.width, f.u.height = 100, 30
	f.u.state = uiChat
	f.drillIntoBuilder(t)
	f.u.updateLayoutAndSize()
	require.Positive(t, f.u.layout.editor.Dy(), "the editor has room under a builder")

	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	f.u.updateLayoutAndSize()
	require.Zero(t, f.u.layout.editor.Dy(), "the run view has no editor")
}

func TestAttachmentsStayWithTheSessionTheyWereAddedFor(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	skill := attachments.SkillAttachment{Name: "go-style", Instructions: "x"}
	f.u.attachments.Update(skill)
	f.drillIntoBuilder(t)
	require.False(t, f.u.attachments.HasContent(), "the root's attachments aren't sent to the builder")

	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, []attachments.SkillAttachment{skill}, f.u.attachments.SkillList())
}

func TestPromptHistoryFollowsTheEditorsTarget(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	require.Contains(t, f.ws.historyFor, "sess-2", "a builder's own prompts are its history")

	f.ws.historyFor = nil
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.Equal(t, []string{"sess-1"}, f.ws.historyFor, "leaving the builder brings back the root's history")
}

func TestASendWhileTheBuilderLoadsKeepsTheDraft(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.drillStack = append(f.u.drillStack, drillInEntry{sessionID: "sess-2", chat: NewChat(f.u.com), conversational: true})
	f.u.syncDraft()
	f.u.updateLayoutAndSize()
	f.u.focus = uiFocusEditor
	f.u.textarea.SetValue("hello")

	f.press(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Empty(t, f.ws.runs)
	require.Equal(t, "hello", f.u.textarea.Value())
}

func TestCommandsFromAutocompleteArentRunFromABuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	infos := f.run(f.u.tryExecuteSlashCommand("/new"))
	require.Len(t, infos, 1)
	require.Contains(t, infos[0].Msg, "go back to it to run /new")
	require.Equal(t, "sess-1", f.u.session.ID, "the root session is untouched")
}

func TestCancellingInABuilderCancelsTheBuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.ws.busy = map[string]bool{"sess-2": true}
	f.u.focus = uiFocusEditor
	f.press(tea.KeyPressMsg{Code: tea.KeyEscape})
	f.press(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Equal(t, []string{"sess-2"}, f.ws.cancelled)
}

func TestARunningNodeCountsItsTime(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	view := runningView()
	view.StartedAt = time.Now().Add(-90 * time.Second)
	f.ws.views = map[string]workflowhost.RunView{"run-1": view}
	f.markerArrives()
	require.True(t, f.u.elapsedTickRunning, "a running run keeps the elapsed tick going")
	require.Regexp(t, `running · 1m3[01]s`, ansi.Strip(f.node(t).Render(120)))

	view.StartedAt = view.StartedAt.Add(-time.Minute)
	f.u.workflowRuns.views["run-1"] = view
	f.node(t).SetView(view)
	running, _ := f.u.refreshRunningWorkflowRuns()
	require.True(t, running)
	require.Regexp(t, `running · 2m3[01]s`, ansi.Strip(f.node(t).Render(120)))
}

func TestARunViewSaysWhichBuildersAreWorking(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)
	row := func() string { return ansi.Strip(f.u.drillStack[0].chat.MessageItems()[2].Render(100)) }
	require.Contains(t, row(), "/demo approach 1 · session")

	f.ws.busy = map[string]bool{"sess-2": true}
	_, _ = f.u.refreshRunningWorkflowRuns()
	require.Contains(t, row(), "/demo approach 1 · working")
}

func TestControlsNeverReachARunTheUserIsntLookingAt(t *testing.T) {
	t.Parallel()

	finished := runningView()
	finished.Status = workflowhost.StatusCompleted
	other := runningView()
	other.RunID = "run-2"

	tests := map[string]func(f *workflowFixture){
		"a finished run's node is selected": func(f *workflowFixture) {
			f.u.chat.Focus()
			f.u.chat.SetSelected(0)
		},
		"a finished run's view is open": func(f *workflowFixture) {
			_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
			f.run(cmd)
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			f.ws.views = map[string]workflowhost.RunView{"run-1": finished, "run-2": other}
			f.markerArrives()
			f.u.workflowRuns.views["run-2"] = other
			setup(f)
			f.u.focus = uiFocusMain

			f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
			require.Empty(t, f.ws.controls, "aborting must not reach run-2")
		})
	}
}

func TestControlsFromABuilderActOnItsRun(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.u.workflowRuns.views["run-2"] = func() workflowhost.RunView {
		v := runningView()
		v.RunID = "run-2"
		return v
	}()
	f.u.focus = uiFocusMain
	f.press(tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt})
	require.Equal(t, []string{"abort:run-1"}, f.ws.controls)
}

func TestAHistoryLoadForAnotherSessionIsIgnored(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	user := message.Message{ID: "u", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "from the builder"}}}
	f.u.Update(promptHistoryLoadedMsg{messages: []message.Message{user}, sessionID: "sess-2"})
	require.Empty(t, f.u.promptHistory.messages, "the editor sends to sess-1, so sess-2's history doesn't apply")

	f.u.Update(promptHistoryLoadedMsg{messages: []message.Message{user}, sessionID: "sess-1"})
	require.Len(t, f.u.promptHistory.messages, 1)
}

func TestPaletteCommandsArentRunFromABuilder(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	actions := []dialog.Action{
		dialog.ActionRunCustomCommand{Name: "review", Content: "Review it"},
		dialog.ActionRunCustomCommand{Name: "demo", Command: commands.CustomCommand{ID: "project:demo", Name: "demo", Workflow: "/proj/demo/workflow.js"}},
		dialog.ActionRunMCPPrompt{PromptID: "summarise"},
	}
	for _, action := range actions {
		f.u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID, action: action})
		infos := f.run(f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
		require.Len(t, infos, 1, "%#v", action)
		require.Equal(t, util.InfoTypeWarn, infos[0].Type, "%#v", action)
		require.False(t, f.u.dialog.HasDialogs())
	}
	_, cmd := f.u.Update(sendMessageMsg{Content: "from /init"})
	infos := f.run(cmd)
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Empty(t, f.ws.runs)
	require.Empty(t, f.ws.starts)
}

func TestPaletteActionsOnTheUsersSessionArentRunFromABuilder(t *testing.T) {
	t.Parallel()

	for _, action := range []dialog.Action{
		dialog.ActionSummarize{SessionID: "sess-1"},
		dialog.ActionRenameSession{},
		dialog.ActionRegenerateTitle{},
	} {
		f := newWorkflowFixture(t)
		f.drillIntoBuilder(t)
		f.u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID, action: action})
		infos := f.run(f.u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter}))
		require.Len(t, infos, 1, "%#v", action)
		require.Contains(t, infos[0].Msg, "acts on your own session", "%#v", action)
		require.False(t, f.u.dialog.HasDialogs())
	}
}

func TestEscCancelsWhatsRunningTheViewOnScreen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		open func(f *workflowFixture)
		want []string
	}{
		"a run view: nothing, the run is aborted with alt+x instead": {
			open: func(f *workflowFixture) {
				_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
				f.run(cmd)
			},
		},
		"a run's subagent: nothing": {
			open: func(f *workflowFixture) {
				_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
				f.run(cmd)
				_, cmd = f.u.Update(util.AgentDrillInMsg{SessionID: "da-1", Label: "da"})
				f.run(cmd)
			},
		},
		"a subagent a builder started: the builder": {
			open: func(f *workflowFixture) {
				f.drillIntoBuilder(t)
				_, cmd := f.u.Update(util.AgentDrillInMsg{SessionID: "builder-msg$$agent1", Label: "explorer"})
				f.run(cmd)
			},
			want: []string{"sess-2"},
		},
		"a subagent the user's own turn started: the user's session": {
			open: func(f *workflowFixture) {
				_, cmd := f.u.Update(util.AgentDrillInMsg{SessionID: "msg$$agent1", Label: "explorer"})
				f.run(cmd)
			},
			want: []string{"sess-1"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newWorkflowFixture(t)
			f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
			f.markerArrives()
			tc.open(f)
			f.ws.busy = map[string]bool{"sess-1": true, "sess-2": true}
			f.u.focus = uiFocusMain
			f.press(tea.KeyPressMsg{Code: tea.KeyEscape})
			f.press(tea.KeyPressMsg{Code: tea.KeyEscape})
			require.Equal(t, tc.want, f.ws.cancelled)
		})
	}
}

func TestABusyBuilderDoesntMakeTheUsersSessionBusy(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.busy = map[string]bool{"sess-2": true}
	f.ws.anyBusy = true
	require.False(t, f.u.isRootBusy())
	f.u.focus = uiFocusEditor
	f.press(tea.KeyPressMsg{Code: 'a'})
	require.Equal(t, f.u.readyPlaceholder, f.u.textarea.Placeholder, "the user's own session isn't working")

	f.drillIntoBuilder(t)
	f.u.focus = uiFocusEditor
	f.press(tea.KeyPressMsg{Code: 'b'})
	require.Equal(t, f.u.workingPlaceholder, f.u.textarea.Placeholder, "the builder is")
}

func TestABuildersQueueShowsWhileTypingToIt(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.drillIntoBuilder(t)
	f.ws.anyBusy = true
	f.ws.queued = map[string]int{"sess-2": 2, "sess-1": 0}
	f.press(tea.KeyPressMsg{Code: 'x'})
	require.Equal(t, 2, f.u.promptQueue)
	require.Positive(t, f.u.pillsAreaHeight(), "the builder's queued prompts show")
}

func TestBrowsingARunViewKeepsTheUsersSessionOpenWithItsDraft(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	f.u.textarea.SetValue("half-written")
	_, cmd := f.u.Update(util.WorkflowRunDrillInMsg{RunID: "run-1", Label: "/demo"})
	f.run(cmd)

	signal := f.u.currentComposerSignal()
	require.Equal(t, "sess-1", signal.sessionID)
	require.True(t, signal.hasDraft, "job wakes still wait for the user's draft")
}

// A draft written in one session and stashed while drilled into a builder
// stays with that session: switching to another session from the builder
// doesn't bring it along.
func TestAStashedDraftDoesntFollowTheUserToAnotherSession(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.u.textarea.SetValue("for session one")
	f.drillIntoBuilder(t)
	f.press(tea.KeyPressMsg{Code: 'x'})
	require.Empty(t, f.u.textarea.Value(), "the builder has its own draft")

	other := session.Session{ID: "sess-other", Title: "other"}
	f.u.clearDrillStack()
	f.u.session = &other
	f.press(tea.KeyPressMsg{Code: tea.KeyEnd})
	require.NotContains(t, f.u.textarea.Value(), "for session one")
}

// A change announcement can be dropped, and a run another Anvil drives
// announces nothing here, so a running run on screen is reloaded now and
// then regardless.
func TestARunningRunOnScreenIsReloadedWithoutAnnouncements(t *testing.T) {
	t.Parallel()

	f := newWorkflowFixture(t)
	f.ws.views = map[string]workflowhost.RunView{"run-1": runningView()}
	f.markerArrives()
	next := runningView()
	next.Phases = append(next.Phases, workflowhost.RunPhase{Step: "audit", Detail: "pass 2"})
	f.ws.views["run-1"] = next

	for range workflowRunReloadTicks {
		_, cmd := f.u.refreshRunningWorkflowRuns()
		f.run(cmd)
	}
	require.Contains(t, ansi.Strip(f.node(t).Render(100)), "audit · pass 2")
}
