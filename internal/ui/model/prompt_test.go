package model

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	uistyles "github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func newPromptTestUI(t *testing.T, ws *testWorkspace) *UI {
	t.Helper()
	st := uistyles.TokyoNight()
	return &UI{com: &common.Common{Workspace: ws, Styles: &st}, textarea: textarea.New()}
}

// gutter renders the first three prompt lines as plain text.
func gutter(m *UI, focused bool) []string {
	fn := m.modePromptFunc
	if !m.promptModes.active() {
		fn = m.normalPromptFunc
	}
	lines := make([]string, 3)
	for i := range lines {
		lines[i] = ansi.Strip(fn(textarea.PromptInfo{LineNumber: i, Focused: focused}))
	}
	return lines
}

func TestEditorPromptBadges(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		configured bool
		bouncer    permission.BouncerMode
		yolo       config.YoloLevel
		want       []string
	}{
		{"nothing on", false, "", config.YoloOff, []string{"  > ", "::: ", "::: "}},
		{"yolo", false, "", config.YoloStandard, []string{" !  ", "::: ", "::: "}},
		{"full yolo", false, "", config.YoloFull, []string{"!!! ", "::: ", "::: "}},
		{"bouncer off", true, permission.BouncerOff, config.YoloOff, []string{"  > ", "::: ", "::: "}},
		{"shadow", true, permission.BouncerShadow, config.YoloOff, []string{" ◇  ", "::: ", "::: "}},
		{"enforce", true, permission.BouncerEnforce, config.YoloOff, []string{" ◆  ", "::: ", "::: "}},
		{"enforce and yolo", true, permission.BouncerEnforce, config.YoloStandard, []string{" ◆  ", " !  ", "::: "}},
		{"shadow and full yolo", true, permission.BouncerShadow, config.YoloFull, []string{" ◇  ", "!!! ", "::: "}},
		{"unconfigured bouncer is ignored", false, permission.BouncerEnforce, config.YoloOff, []string{"  > ", "::: ", "::: "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newPromptTestUI(t, &testWorkspace{bouncerConfigured: tt.configured, bouncerMode: tt.bouncer, yolo: tt.yolo})
			m.refreshEditorPrompt()
			for _, focused := range []bool{true, false} {
				got := gutter(m, focused)
				if !focused && tt.want[0] == "  > " {
					continue // The unfocused normal prompt differs and is unchanged.
				}
				for i, line := range got {
					require.Equal(t, tt.want[i], line, "focused=%v line %d", focused, i)
					require.Equal(t, promptWidth, ansi.StringWidth(line), "gutter width, line %d", i)
				}
			}
		})
	}
}

func TestEditorPromptStyles(t *testing.T) {
	t.Parallel()
	ed := uistyles.TokyoNight().Editor

	render := func(ws *testWorkspace, line int, focused bool) string {
		m := newPromptTestUI(t, ws)
		m.refreshEditorPrompt()
		return m.modePromptFunc(textarea.PromptInfo{LineNumber: line, Focused: focused})
	}

	require.Equal(t, ed.PromptEnforceFocused.Render(), render(&testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerEnforce}, 0, true))
	require.Equal(t, ed.PromptEnforceInactive.Render(), render(&testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerEnforce, yolo: config.YoloFull}, 0, true),
		"full yolo bypasses the bouncer, so its badge is dimmed")
	require.Equal(t, ed.PromptFullYoloFocused.Render(), render(&testWorkspace{yolo: config.YoloFull}, 0, true))
	require.Equal(t, ed.PromptYoloFocused.Render(), render(&testWorkspace{yolo: config.YoloStandard}, 0, true))

	// Dots take the colour of the riskiest active mode.
	require.Equal(t, ed.PromptBouncerDotsFocused.Render(), render(&testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerShadow}, 2, true))
	require.Equal(t, ed.PromptYoloDotsFocused.Render(), render(&testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerShadow, yolo: config.YoloStandard}, 2, true))
	require.Equal(t, ed.PromptFullYoloDotsFocused.Render(), render(&testWorkspace{yolo: config.YoloFull}, 2, true))
	require.Equal(t, ed.PromptModeDotsBlurred.Render(), render(&testWorkspace{yolo: config.YoloFull}, 2, false))

	// Every badge is visually distinct when focused.
	seen := map[string]string{}
	for name, s := range map[string]string{
		"shadow":   ed.PromptShadowFocused.Render(),
		"enforce":  ed.PromptEnforceFocused.Render(),
		"inactive": ed.PromptEnforceInactive.Render(),
		"yolo":     ed.PromptYoloFocused.Render(),
		"fullyolo": ed.PromptFullYoloFocused.Render(),
	} {
		require.NotContains(t, seen, s, "%s looks the same as %s", name, seen[s])
		seen[s] = name
	}
}

func TestEditorPromptRefreshesOnCycle(t *testing.T) {
	t.Parallel()
	ws := &testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerOff}
	m := newPromptTestUI(t, ws)
	m.refreshEditorPrompt()
	require.Equal(t, "  > ", gutter(m, true)[0])

	m.cycleBouncerMode()
	require.Equal(t, " ◇  ", gutter(m, true)[0])
	m.cycleYoloLevel()
	require.Equal(t, []string{" ◇  ", " !  "}, gutter(m, true)[:2])
	m.cycleYoloLevel()
	require.Equal(t, []string{" ◇  ", "!!! "}, gutter(m, true)[:2])
	m.cycleBouncerMode()
	m.cycleBouncerMode()
	m.cycleYoloLevel()
	require.Equal(t, "  > ", gutter(m, true)[0])
}

func TestModePlaceholder(t *testing.T) {
	t.Parallel()
	require.Empty(t, promptModes{}.modePlaceholder())
	require.Empty(t, promptModes{bouncer: permission.BouncerEnforce}.modePlaceholder())
	require.Equal(t, "Yolo mode!", promptModes{yolo: config.YoloStandard}.modePlaceholder())
	require.Equal(t, "Full yolo: no permission checks", promptModes{yolo: config.YoloFull}.modePlaceholder())
}

func TestCycleBouncerKey(t *testing.T) {
	t.Parallel()

	unconfigured := &testWorkspace{}
	m := newPromptTestUI(t, unconfigured)
	info, ok := findInfoMsg(collectMsgs(m.handleCycleBouncerKey()))
	require.True(t, ok)
	require.Equal(t, "No bouncer configured", info.Msg)
	require.Empty(t, unconfigured.bouncerSetCalls)

	ws := &testWorkspace{bouncerConfigured: true, bouncerMode: permission.BouncerOff}
	m = newPromptTestUI(t, ws)
	for _, want := range []permission.BouncerMode{permission.BouncerShadow, permission.BouncerEnforce, permission.BouncerOff} {
		info, ok := findInfoMsg(collectMsgs(m.handleCycleBouncerKey()))
		require.True(t, ok)
		require.Equal(t, "Bouncer: "+string(want), info.Msg)
	}
	require.Equal(t, []permission.BouncerMode{permission.BouncerShadow, permission.BouncerEnforce, permission.BouncerOff}, ws.bouncerSetCalls)
	require.Equal(t, []string{"ctrl+q"}, DefaultKeyMap().CycleBouncer.Keys())
}
