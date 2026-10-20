package model

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/ui/anim"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/autocomplete"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// fakeFeatureMsg, fakeFeatureAction and fakeObservedMsg belong to
// fakeFeature; no upstream case matches them.
type (
	fakeFeatureMsg    struct{}
	fakeFeatureAction struct{}
	fakeObservedMsg   struct{ title string }
)

// fakeFeature records what the routing offers it. It claims its own
// message and action types, the f9 key, and the custom command whose ID
// is "fake:cmd".
type fakeFeature struct {
	u *UI

	observed []tea.Msg
	msgs     []tea.Msg
	keys     []tea.KeyPressMsg
	actions  []dialog.Action
	commands []string
	panel    panelView
	entries  []*dialog.CommandItem
}

var fakeFeatureKey = key.NewBinding(key.WithKeys("f9"))

func (f *fakeFeature) Observe(msg tea.Msg) tea.Cmd {
	f.observed = append(f.observed, msg)
	switch msg.(type) {
	case pubsub.Event[session.Session]:
		// The title UI shows before update applies the event.
		title := f.u.session.Title
		return func() tea.Msg { return fakeObservedMsg{title: title} }
	case tea.TerminalVersionMsg:
		return func() tea.Msg { return fakeObservedMsg{title: "early return"} }
	}
	return nil
}

func (f *fakeFeature) HandleMsg(msg tea.Msg) (tea.Cmd, bool) {
	f.msgs = append(f.msgs, msg)
	_, ok := msg.(fakeFeatureMsg)
	return nil, ok
}

func (f *fakeFeature) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	f.keys = append(f.keys, msg)
	return nil, key.Matches(msg, fakeFeatureKey)
}

func (f *fakeFeature) HandleAction(action dialog.Action) (tea.Cmd, bool) {
	f.actions = append(f.actions, action)
	_, ok := action.(fakeFeatureAction)
	return nil, ok
}

func (f *fakeFeature) RunCommand(cmd commands.CustomCommand, rawArgs string) (tea.Cmd, bool) {
	if cmd.ID != "fake:cmd" {
		return nil, false
	}
	f.commands = append(f.commands, cmd.SlashName()+"|"+rawArgs)
	return nil, true
}

func (f *fakeFeature) Panel() panelView { return f.panel }

func (f *fakeFeature) PaletteEntries() []*dialog.CommandItem { return f.entries }

// staticPanel is a panel of fixed lines.
type staticPanel []string

func (p staticPanel) Lines() int { return len(p) }

func (p staticPanel) Render(width int) string {
	lines := make([]string, len(p))
	for i, line := range p {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

func newFeatureTestUI(t *testing.T) (*UI, *fakeFeature) {
	t.Helper()
	ws := newWorkflowTestWorkspace()
	u := newTestUI()
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	sty := u.com.Styles.Attachments
	renderer := attachments.NewRenderer(sty.Normal, sty.Deleting, sty.Image, sty.Text, sty.Skill, sty.Remove)
	u.attachments = attachments.New(renderer, attachments.Keymap{})
	u.slashAC = autocomplete.New(nil, 10)
	sess := ws.sessions["sess-1"]
	u.session = &sess
	f := &fakeFeature{u: u}
	u.features.register(f)
	u.updateLayoutAndSize()
	return u, f
}

func TestFeaturesObserveSharedMessagesBeforeUpdate(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	renamed := *u.session
	renamed.Title = "Renamed"
	_, cmd := u.Update(pubsub.Event[session.Session]{Type: pubsub.UpdatedEvent, Payload: renamed})

	require.Equal(t, "Renamed", u.session.Title, "update still applies the event")
	require.Contains(t, cmdMsgs(cmd), tea.Msg(fakeObservedMsg{title: "Invoker"}),
		"the feature sees the state before update and its command survives")
	require.Empty(t, f.msgs, "a message an upstream case handles is never offered to claim")
}

func TestFeaturesNeverSeeInputAsMessages(t *testing.T) {
	t.Parallel()

	tests := map[string]tea.Msg{
		"key":         tea.KeyPressMsg{Code: 'a', Text: "a"},
		"paste":       tea.PasteMsg{Content: "pasted"},
		"click":       tea.MouseClickMsg{X: 1, Y: 1},
		"key release": tea.KeyReleaseMsg{Code: 'a'},
		"paste start": tea.PasteStartMsg{},
		"wheel":       tea.MouseWheelMsg{},
	}
	for name, msg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, f := newFeatureTestUI(t)
			u.Update(msg)
			require.Empty(t, f.observed)
			require.Empty(t, f.msgs)
		})
	}
}

func TestFeatureClaimsItsOwnMessageBeforeTheDialog(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.dialog.OpenDialog(&stubDialog{id: "stub", action: dialog.ActionClose{}})
	u.Update(fakeFeatureMsg{})

	require.Equal(t, []tea.Msg{fakeFeatureMsg{}}, f.msgs)
	require.True(t, u.dialog.ContainsDialog("stub"), "a claimed message never reaches the dialog")
}

func TestFeatureKeyIsOfferedWhenNoGlobalKeyMatches(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.Update(tea.KeyPressMsg{Code: tea.KeyF9})

	require.Len(t, f.keys, 1)
	require.Empty(t, u.textarea.Value())
}

func TestFeatureKeyIsBlockedByAMatchingGlobalKeyWhoseConditionFails(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.keyMap.Chat.TogglePills = key.NewBinding(key.WithKeys("f9"))
	u.session = nil
	u.Update(tea.KeyPressMsg{Code: tea.KeyF9})

	require.Empty(t, f.keys)
}

func TestFeatureClaimsItsDialogAction(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.dialog.OpenDialog(&stubDialog{id: "stub", action: fakeFeatureAction{}})
	cmd := u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Equal(t, []dialog.Action{fakeFeatureAction{}}, f.actions)
	require.Empty(t, cmdMsgs(cmd), "a claimed action isn't sent back as a message")
}

func TestPaletteCommandRunsThroughItsFeature(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID, action: dialog.ActionRunCustomCommand{
		Name: "fake", Content: "BODY", Command: commands.CustomCommand{ID: "fake:cmd", Name: "fake", Content: "BODY"},
	}})
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Equal(t, []string{"fake|"}, f.commands)
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID), "the palette closes")
	require.Empty(t, u.com.Workspace.(*workflowTestWorkspace).runs, "the body isn't sent")
}

func TestSlashCommandRunsThroughItsFeature(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.customCommands = []commands.CustomCommand{{ID: "fake:cmd", Name: "fake", Content: "BODY $ARGUMENTS"}}
	cmdMsgs(u.tryExecuteSlashCommand("/fake some args"))

	require.Equal(t, []string{"fake|some args"}, f.commands)
	require.Empty(t, u.com.Workspace.(*workflowTestWorkspace).runs, "the body isn't sent")
}

func TestFeaturePanelSharesThePillsArea(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	f.panel = staticPanel{"first panel line", "second panel line"}
	u.updateLayoutAndSize()

	require.Equal(t, 2, u.pillsAreaHeight())
	require.Contains(t, ansi.Strip(u.pillsView), "second panel line")

	u.drillStack = []drillInEntry{{sessionID: "child", chat: NewChat(u.com)}}
	u.updateLayoutAndSize()
	require.Zero(t, u.pillsAreaHeight())
	require.NotContains(t, ansi.Strip(u.pillsView), "panel line", "panels hide while drilled in")
}

func TestPaletteListsFeatureEntries(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	f.entries = []*dialog.CommandItem{
		dialog.NewCommandItem(u.com.Styles, "fake_entry", "Zebra Fake Entry", "", fakeFeatureAction{}),
	}
	u.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	require.True(t, u.dialog.ContainsDialog(dialog.CommandsID))
	for _, r := range "zebra fake" {
		u.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Contains(t, f.actions, dialog.Action(fakeFeatureAction{}))
}

// requirePanelContract checks that p renders exactly p.Lines() lines,
// each within width, at widths from nothing to wider than its content.
func requirePanelContract(t *testing.T, p panelView) {
	t.Helper()
	for _, width := range []int{0, 1, 2, 7, 30, 200} {
		out := p.Render(width)
		lines := strings.Split(out, "\n")
		require.Len(t, lines, p.Lines(), "width %d:\n%s", width, out)
		for _, line := range lines {
			require.LessOrEqual(t, ansi.StringWidth(line), width, "width %d: %q", width, line)
		}
	}
}

func TestPanelsKeepTheirRowsWhenNothingFits(t *testing.T) {
	t.Parallel()

	var fs features
	fs.register(&fakeFeature{panel: staticPanel{"first"}})
	fs.register(&fakeFeature{panel: staticPanel{"second"}})
	fs.register(&fakeFeature{})
	fs.register(&fakeFeature{panel: staticPanel{}})
	require.Equal(t, 2, fs.panelLines())
	for _, width := range []int{0, 1, 3} {
		require.Len(t, strings.Split(fs.renderPanels(width), "\n"), 2, "width %d", width)
	}
	require.Equal(t, "first\nsecond", fs.renderPanels(10))
}

func TestObservedCommandsSurviveUpdatesEarlyReturns(t *testing.T) {
	t.Parallel()

	u, _ := newFeatureTestUI(t)
	_, cmd := u.Update(tea.TerminalVersionMsg{Name: "xterm"})
	require.Contains(t, cmdMsgs(cmd), tea.Msg(fakeObservedMsg{title: "early return"}))
}

func TestTheFirstFeatureToClaimWins(t *testing.T) {
	t.Parallel()

	u, first := newFeatureTestUI(t)
	second := &fakeFeature{u: u}
	u.features.register(second)
	u.Update(fakeFeatureMsg{})
	u.Update(tea.KeyPressMsg{Code: tea.KeyF9})
	u.dialog.OpenDialog(&stubDialog{id: "stub", action: fakeFeatureAction{}})
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Len(t, first.msgs, 1)
	require.Len(t, first.keys, 1)
	require.Len(t, first.actions, 1)
	require.Empty(t, second.msgs)
	require.Empty(t, second.keys)
	require.Empty(t, second.actions)
	require.Len(t, second.observed, len(first.observed), "every feature observes")
}

func TestASlashCommandAFeatureRunsIsNotSentAsAPrompt(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.customCommands = []commands.CustomCommand{{ID: "fake:cmd", Name: "fake", Content: "BODY $ARGUMENTS"}}
	u.focus = uiFocusEditor
	u.textarea.SetValue("/fake some args")
	_, cmd := u.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmdMsgs(cmd)

	require.Equal(t, []string{"fake|some args"}, f.commands)
	require.Empty(t, u.com.Workspace.(*workflowTestWorkspace).runs, "a command run without a follow-up command is still handled")
}

func TestAPaletteCommandNoFeatureRunsLeavesThePaletteToUpstream(t *testing.T) {
	t.Parallel()

	u, f := newFeatureTestUI(t)
	u.dialog.OpenDialog(&stubDialog{id: dialog.CommandsID})
	u.dialog.OpenDialog(&stubDialog{id: "front", action: dialog.ActionRunCustomCommand{
		Name: "other", Content: "BODY", Command: commands.CustomCommand{ID: "other", Name: "other", Content: "BODY"},
	}})
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.Empty(t, f.commands)
	require.True(t, u.dialog.ContainsDialog(dialog.CommandsID), "only a feature that runs the command closes the palette")
	require.False(t, u.dialog.ContainsDialog("front"), "upstream handles it as a prompt command")
}

func TestFeaturesIgnoreHighFrequencyMessagesWithoutAllocating(t *testing.T) {
	f := newWorkflowUIFixture(t)
	f.event(started("demo", "sess-1"))
	msgs := []tea.Msg{spinner.TickMsg{}, anim.StepMsg{}, tea.MouseMotionMsg{}}
	allocs := testing.AllocsPerRun(100, func() {
		for _, msg := range msgs {
			f.u.features.observe(msg)
			f.u.features.handleMsg(msg)
		}
	})
	require.Zero(t, allocs)
}

func BenchmarkFeaturesIgnoreHighFrequencyMessages(b *testing.B) {
	ws := newWorkflowTestWorkspace()
	u := newTestUI()
	u.com.Workspace = ws
	u.keyMap = DefaultKeyMap()
	u.dialog = dialog.NewOverlay()
	u.features.register(newWorkflowFeature(uiHost{u}, u.com, ws))
	msgs := []tea.Msg{spinner.TickMsg{}, anim.StepMsg{}, tea.MouseMotionMsg{}}
	b.ReportAllocs()
	for b.Loop() {
		for _, msg := range msgs {
			u.features.observe(msg)
			u.features.handleMsg(msg)
		}
	}
}

// guardFeature claims cancel keys and refuses navigation while armed.
type guardFeature struct {
	fakeFeature
	armed     bool
	cancelled int
}

type guardCancelledMsg struct{}

func (g *guardFeature) Cancel() (tea.Cmd, bool) {
	if !g.armed {
		return nil, false
	}
	g.cancelled++
	return func() tea.Msg { return guardCancelledMsg{} }, true
}

func (g *guardFeature) BlockNavigation() tea.Cmd {
	if !g.armed {
		return nil
	}
	return util.ReportWarn("guarded")
}

func newGuardTestUI(t *testing.T) (*UI, *guardFeature) {
	t.Helper()
	u, _ := newFeatureTestUI(t)
	g := &guardFeature{fakeFeature: fakeFeature{u: u}, armed: true}
	u.features.register(g)
	return u, g
}

func TestCancelFeatureClaimsTheCancelKeyFirst(t *testing.T) {
	t.Parallel()
	u, g := newGuardTestUI(t)
	u.focus = uiFocusEditor

	_, cmd := u.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	require.Equal(t, 1, g.cancelled)
	require.Contains(t, cmdMsgs(cmd), tea.Msg(guardCancelledMsg{}))
}

func TestNavigationGuardRefusesARewind(t *testing.T) {
	t.Parallel()
	u, _ := newGuardTestUI(t)

	cmd := u.handleNavigateTree(dialog.ActionNavigateTree{MessageID: "m"})

	require.Nil(t, u.branchNavigation, "nothing is snapshotted for a refused rewind")
	msgs := cmdMsgs(cmd)
	require.Len(t, msgs, 1)
	require.Equal(t, "guarded", msgs[0].(util.InfoMsg).Msg)
}

func TestNavigationGuardRefusesAReturnToPreBranch(t *testing.T) {
	t.Parallel()
	u, _ := newGuardTestUI(t)
	u.pendingBranch = &branchReturnSnapshot{sessionID: u.session.ID, leafID: "old"}

	cmd := u.beginBranchReturn(true)

	require.False(t, u.branchRestoring)
	msgs := cmdMsgs(cmd)
	require.Len(t, msgs, 1)
	require.Equal(t, "guarded", msgs[0].(util.InfoMsg).Msg)
}

func TestNavigationGuardRefusesAnMCPToggle(t *testing.T) {
	t.Parallel()
	u, _ := newGuardTestUI(t)
	u.enabledLazyMCPs = map[string]bool{}

	u.dialog.OpenDialog(&stubDialog{id: "stub", action: dialog.ActionToggleLazyMCP{ServerName: "linear", Enabled: true}})

	cmd := u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})

	require.False(t, u.enabledLazyMCPs["linear"], "a toggle would move the leaf")
	require.Contains(t, cmdMsgs(cmd), tea.Msg(util.NewWarnMsg("guarded")))
}
