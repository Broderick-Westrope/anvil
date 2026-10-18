package model

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
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
	running  map[string]bool
	pending  map[string][]string
	sent     []steerSent
	cancels  []string
	steerErr error
}

func (w *steerTestWorkspace) AgentSteerSubagent(child, text string, interrupt bool) error {
	if w.steerErr != nil {
		return w.steerErr
	}
	w.sent = append(w.sent, steerSent{child, text, interrupt})
	return nil
}

func (w *steerTestWorkspace) AgentCancelSubagent(child string) bool {
	w.cancels = append(w.cancels, child)
	return w.running[child]
}

func (w *steerTestWorkspace) AgentSubagentRunning(child string) bool { return w.running[child] }

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
	ws := &steerTestWorkspace{running: map[string]bool{"child": true}, pending: map[string][]string{}}
	host := &fakeHost{overlay: dialog.NewOverlay(), keyMap: DefaultKeyMap(), viewedSubagent: "child"}
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
	s.host.viewedSubagent = ""

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

	require.Equal(t, []steerSent{{"child", "use v2", false}}, s.ws.sent)
	require.Empty(t, s.f.input.Value())
	require.NotEmpty(t, infos)
}

func TestInterruptSteerSendsWithInterrupt(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	s.press(tabKey)
	s.press(typeKeys("stop")...)
	s.press(interruptKey)

	require.Equal(t, []steerSent{{"child", "stop", true}}, s.ws.sent)
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
	require.Equal(t, []string{"child"}, s.ws.cancels)
}

func TestEscOnAFinishedSubagentDoesNotCancelTheRun(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.running["child"] = false

	_, claimed := s.press(escKey)
	require.True(t, claimed, "Esc is swallowed so it can't cancel the main run")
	require.Empty(t, s.ws.cancels)
}

func TestFinishedSubagentKeepsTheEditorDisabledWithAHint(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.running["child"] = false

	_, claimed := s.press(tabKey)
	require.False(t, claimed)
	footer := s.f.DrillInFooter()
	require.NotNil(t, footer)
	require.Contains(t, ansi.Strip(footer.Render(80)), "ask the orchestrator to resume it")
}

func TestFooterShowsQueuedSteers(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)
	s.ws.pending["child"] = []string{"a", "b"}

	footer := s.f.DrillInFooter()
	require.Equal(t, subagentSteerFooterLines, footer.Lines())
	rendered := ansi.Strip(footer.Render(80))
	require.Contains(t, rendered, "2 queued")
	require.Len(t, strings.Split(rendered, "\n"), subagentSteerFooterLines)
}

func TestReturnedSteersComeBackToTheMainEditor(t *testing.T) {
	t.Parallel()
	s := newSteerFixture(t)

	cmd, handled := s.f.HandleMsg(pubsub.Event[agent.SubagentSteersReturned]{
		Type:    pubsub.CreatedEvent,
		Payload: agent.SubagentSteersReturned{SessionID: "child", Steers: []string{"first", "second"}, Reason: "It failed."},
	})
	require.True(t, handled)
	msgs := cmdMsgs(cmd)
	require.Len(t, msgs, 1)
	require.Contains(t, msgs[0].(util.InfoMsg).Msg, "It failed.")
	require.Equal(t, []string{"first\n\nsecond"}, s.host.restored)
}

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
