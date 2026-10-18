package model

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

type steerSent struct {
	child, text string
	interrupt   bool
}

type steerTestWorkspace struct {
	running      map[string]bool
	pending      map[string][]string
	sent         []steerSent
	cancels      []string
	steerErr     error
	returned     []agent.SubagentSteersReturned
	refuseCancel bool
}

func (w *steerTestWorkspace) AgentTakeReturnedSteers(rootSessionID string) []agent.SubagentSteersReturned {
	var taken, kept []agent.SubagentSteersReturned
	for _, r := range w.returned {
		if r.RootSessionID == rootSessionID {
			taken = append(taken, r)
		} else {
			kept = append(kept, r)
		}
	}
	w.returned = kept
	return taken
}

func (w *steerTestWorkspace) AgentSteerSubagent(child, text string, interrupt bool) error {
	if w.steerErr != nil {
		return w.steerErr
	}
	w.sent = append(w.sent, steerSent{child, text, interrupt})
	return nil
}

func (w *steerTestWorkspace) AgentCancelSubagent(child string) bool {
	if w.refuseCancel {
		return false
	}
	w.cancels = append(w.cancels, child)
	return w.running[child]
}

func (w *steerTestWorkspace) AgentSubagentRunning(child string) bool { return w.running[child] }

func (w *steerTestWorkspace) ParseAgentToolSessionID(id string) (string, string, bool) {
	msg, call, ok := strings.Cut(id, "$$")
	return msg, call, ok
}

func (w *steerTestWorkspace) AgentSubagentPendingSteers(child string) []string {
	return w.pending[child]
}

type steerFixture struct {
	f    *subagentSteer
	host *fakeHost
	ws   *steerTestWorkspace
}

func newSteerFixture(t *testing.T) *steerFixture {
	t.Helper()
	ws := &steerTestWorkspace{running: map[string]bool{"msg$$child": true}, pending: map[string][]string{}}
	host := &fakeHost{session: &session.Session{ID: "root"}, overlay: dialog.NewOverlay(), keyMap: DefaultKeyMap(), viewedChild: "msg$$child"}
	return &steerFixture{f: newSubagentSteer(host, common.DefaultCommon(nil), ws), host: host, ws: ws}
}

func (s *steerFixture) press(keys ...tea.KeyPressMsg) ([]util.InfoMsg, bool) {
	var infos []util.InfoMsg
	claimed := true
	for _, k := range keys {
		cmd, ok := s.f.PreemptInput(k)
		claimed = claimed && ok
		for _, msg := range cmdMsgs(cmd) {
			if info, ok := msg.(util.InfoMsg); ok {
				infos = append(infos, info)
			}
		}
	}
	return infos, claimed
}

var (
	tabKey       = tea.KeyPressMsg{Code: tea.KeyTab}
	steerEnter   = tea.KeyPressMsg{Code: tea.KeyEnter}
	escKey       = tea.KeyPressMsg{Code: tea.KeyEscape}
	interruptKey = tea.KeyPressMsg{Code: 'i', Mod: tea.ModAlt}
)

func TestSteerIgnoresKeysOutsideASubagentView(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.host.viewedChild = ""

	_, claimed := s.press(tabKey)
	require.False(t, claimed)
	_, claimed = s.press(escKey)
	require.False(t, claimed)
	require.Nil(t, s.f.DrillInFooter())
}

func TestSteerSendsTypedTextOnEnter(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	s.press(tabKey)
	s.press(typeKeys("use v2")...)
	infos, _ := s.press(steerEnter)

	require.Equal(t, []steerSent{{"msg$$child", "use v2", false}}, s.ws.sent)
	require.Empty(t, s.f.input.Value())
	require.NotEmpty(t, infos)
}

func TestInterruptSteerSendsWithInterrupt(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	s.press(tabKey)
	s.press(typeKeys("stop")...)
	s.press(interruptKey)

	require.Equal(t, []steerSent{{"msg$$child", "stop", true}}, s.ws.sent)
}

func TestRejectedSteerKeepsTheTextAndSaysWhy(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.steerErr = errors.New("subagent is no longer accepting messages")

	s.press(tabKey)
	s.press(typeKeys("late")...)
	infos, _ := s.press(steerEnter)

	require.Equal(t, "late", s.f.input.Value())
	require.Len(t, infos, 1)
	require.Equal(t, util.InfoTypeWarn, infos[0].Type)
	require.Contains(t, infos[0].Msg, "no longer accepting messages")
}

func TestEscCancelsTheViewedSubagent(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	_, claimed := s.press(escKey)
	require.True(t, claimed)
	require.Equal(t, []string{"msg$$child"}, s.ws.cancels)
}

func TestEscOnAFinishedSubagentDoesNotCancelTheRun(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.running["msg$$child"] = false

	_, claimed := s.press(escKey)
	require.True(t, claimed, "Esc is swallowed so it can't cancel the main run")
	require.Empty(t, s.ws.cancels)
}

func TestFinishedSubagentKeepsTheEditorDisabledWithAHint(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.running["msg$$child"] = false

	_, claimed := s.press(tabKey)
	require.False(t, claimed)
	footer := s.f.DrillInFooter()
	require.NotNil(t, footer)
	require.Contains(t, ansi.Strip(footer.Render(80)), "ask the orchestrator to resume it")
}

func TestFooterShowsQueuedSteers(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.pending["msg$$child"] = []string{"a", "b"}

	footer := s.f.DrillInFooter()
	require.Equal(t, subagentSteerFooterLines, footer.Lines())
	rendered := ansi.Strip(footer.Render(80))
	require.Contains(t, rendered, "2 queued")
	require.Len(t, strings.Split(rendered, "\n"), subagentSteerFooterLines)
}

func TestReturnedSteersComeBackToTheMainEditor(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	s.ws.returned = []agent.SubagentSteersReturned{
		{RootSessionID: "root", SessionID: "msg$$child", Steers: []string{"first", "second"}, Reason: "It failed."},
		{RootSessionID: "other-root", SessionID: "msg$$elsewhere", Steers: []string{"not here"}, Reason: "It failed."},
	}

	// Any message picks them up, so a dropped nudge can't lose them.
	msgs := cmdMsgs(s.f.Observe(spinnerTickMsgForTest{}))
	require.Len(t, msgs, 1)
	require.Contains(t, msgs[0].(util.InfoMsg).Msg, "It failed.")
	require.Equal(t, []string{"first\n\nsecond"}, s.host.restored)
	require.Nil(t, s.f.Observe(spinnerTickMsgForTest{}), "taken only once")

	_, handled := s.f.HandleMsg(pubsub.Event[agent.SubagentSteersReturned]{Type: pubsub.CreatedEvent})
	require.True(t, handled, "the nudge itself is claimed")
}

type spinnerTickMsgForTest struct{}

func TestPasteGoesToTheFocusedSteerInput(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	_, claimed := s.f.PreemptInput(tea.PasteMsg{Content: "pasted"})
	require.False(t, claimed, "an unfocused input leaves paste to the editor")

	s.press(tabKey)
	_, claimed = s.f.PreemptInput(tea.PasteMsg{Content: "pasted"})
	require.True(t, claimed)
	require.Equal(t, "pasted", s.f.input.Value())
}

func TestFooterHelpReadsCleanly(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	rendered := ansi.Strip(s.f.DrillInFooter().Render(200))
	require.Contains(t, rendered, "tab message subagent · esc cancel subagent")

	s.press(tabKey)
	rendered = ansi.Strip(s.f.DrillInFooter().Render(200))
	require.Contains(t, rendered, "enter send · alt+i interrupt and send · tab back to chat · esc cancel subagent")
}

func TestEscInAFocusedInputLeavesItBeforeCancelling(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	s.press(tabKey)
	s.press(typeKeys("half a thought")...)
	_, claimed := s.press(escKey)
	require.True(t, claimed)
	require.Empty(t, s.ws.cancels, "the first Esc only leaves the input")
	require.False(t, s.f.input.Focused())
	require.Equal(t, "half a thought", s.f.input.Value())

	s.press(escKey)
	require.Equal(t, []string{"msg$$child"}, s.ws.cancels)
}

func TestEscOnAFinishingSubagentSaysSo(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.refuseCancel = true

	infos, _ := s.press(escKey)
	require.Len(t, infos, 1)
	require.Contains(t, infos[0].Msg, "already returning its result")
}

func TestFooterRenderingChangesNoState(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.press(tabKey)
	s.press(typeKeys("draft")...)

	s.host.viewedChild = "msg$$other"
	s.f.DrillInFooter().Render(80)
	require.Equal(t, "msg$$child", s.f.child, "only input and messages switch subagents")
	require.True(t, s.f.input.Focused())
}

func TestWorkflowChildrenGetNoFooterAndKeepEsc(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.host.viewedChild = "3f2a0c1e-workflow-uuid"

	require.Nil(t, s.f.DrillInFooter())
	_, claimed := s.press(escKey)
	require.False(t, claimed)
}

func TestPasteIntoAStoppedSubagentsInputIsRefused(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.press(tabKey)
	s.ws.running["msg$$child"] = false

	_, claimed := s.f.PreemptInput(tea.PasteMsg{Content: "late"})
	require.False(t, claimed)
	require.Empty(t, s.f.input.Value())
}

func TestRenderingLeavesTheLiveInputsWidthAlone(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.f.Observe(tea.WindowSizeMsg{Width: 100, Height: 40})
	width := s.f.input.Width()

	s.f.DrillInFooter().Render(37)
	require.Equal(t, width, s.f.input.Width())
}
