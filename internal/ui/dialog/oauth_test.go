package dialog

import (
	"runtime"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// fakeOAuthProvider stands in for a sign-in provider, recording the codes
// the dialog submits.
type fakeOAuthProvider struct {
	codeEntry bool
	submitted []string
}

func (p *fakeOAuthProvider) name() string                     { return "Fake" }
func (p *fakeOAuthProvider) initiateAuth() tea.Msg            { return nil }
func (p *fakeOAuthProvider) startPolling(string, int) tea.Cmd { return nil }
func (p *fakeOAuthProvider) stopPolling() tea.Msg             { return nil }
func (p *fakeOAuthProvider) supportsCodeEntry() bool          { return p.codeEntry }

func (p *fakeOAuthProvider) submitCode(input string) tea.Cmd {
	p.submitted = append(p.submitted, input)
	return func() tea.Msg { return nil }
}

// newDisplayedOAuth returns an OAuth dialog showing its sign-in screen.
// userCode set means a device flow; empty means a browser flow.
func newDisplayedOAuth(t *testing.T, codeEntry, onboarding bool, userCode string) (*OAuth, *fakeOAuthProvider) {
	t.Helper()
	sty := styles.TokyoNight()
	p := &fakeOAuthProvider{codeEntry: codeEntry}
	m, _ := newOAuth(&common.Common{Styles: &sty}, onboarding, catwalk.Provider{ID: "fake"},
		config.SelectedModel{}, config.SelectedModelType("large"), p)
	m.HandleMsg(ActionInitiateOAuth{UserCode: userCode, VerificationURL: "https://example.test/verify"})
	require.Equal(t, OAuthStateDisplay, m.State)
	return m, p
}

func helpKeys(bindings []key.Binding) []string {
	var keys []string
	for _, b := range bindings {
		keys = append(keys, b.Help().Desc)
	}
	return keys
}

func screenText(scr uv.ScreenBuffer) string {
	var b strings.Builder
	for y := range mouseScreenH {
		row, _ := screenRow(scr, y)
		b.WriteString(row + "\n")
	}
	return b.String()
}

func TestOAuth_DisplayTextFollowsTheFlow(t *testing.T) {
	t.Parallel()

	device, _ := newDisplayedOAuth(t, false, false, "ABCD-1234")
	text := screenText(drawMouseDialog(device))
	require.Contains(t, text, "copy the code below")
	require.Contains(t, text, "ABCD-1234")
	require.Contains(t, text, "Verifying...")
	require.NotContains(t, text, "Waiting for browser")
	require.Equal(t, []string{"copy code", "copy url", "copy & open", "exit"}, helpKeys(device.ShortHelp()))

	browser, _ := newDisplayedOAuth(t, false, false, "")
	text = screenText(drawMouseDialog(browser))
	require.Contains(t, text, "open the browser and authenticate")
	require.Contains(t, text, "Waiting for browser authentication...")
	require.NotContains(t, text, "Verifying")
	require.Equal(t, []string{"copy url", "open browser", "exit"}, helpKeys(browser.ShortHelp()))
}

func TestOAuth_LongPastedCodeScrollsWithinTheField(t *testing.T) {
	t.Parallel()

	m, _ := newDisplayedOAuth(t, true, false, "")
	drawMouseDialog(m)
	require.Nil(t, m.HandleMsg(tea.PasteMsg{Content: strings.Repeat("x", 70) + "END"}))
	require.Equal(t, []string{"submit code", "back"}, helpKeys(m.ShortHelp()), "a paste focuses the field")
	scr := drawMouseDialog(m)

	_, y := findOnScreen(t, scr, "> xxx")
	row, _ := screenRow(scr, y)
	require.Contains(t, row, "xEND", "the field scrolls to its end on one row")
	next, _ := screenRow(scr, y+1)
	require.NotContains(t, next, "x", "the field must not wrap")
}

func TestOAuth_TabFocusesThePasteFieldAndTheCaretSitsInIt(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		onboarding bool
		// padFrame pads the dialog frame, as a theme may.
		padFrame bool
	}{
		"dialog":        {},
		"padded dialog": {padFrame: true},
		"onboarding":    {onboarding: true},
	} {
		m, _ := newDisplayedOAuth(t, true, tc.onboarding, "")
		if tc.padFrame {
			m.com.Styles.Dialog.View = m.com.Styles.Dialog.View.Padding(1, 2)
		}
		require.Nil(t, m.Draw(uv.NewScreenBuffer(mouseScreenW, mouseScreenH), uv.Rect(0, 0, mouseScreenW, mouseScreenH)),
			"no caret until the field is focused")
		require.Equal(t, []string{"enter code", "copy url", "open browser", "exit"}, helpKeys(m.ShortHelp()))

		require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab}))
		require.Equal(t, []string{"submit code", "back"}, helpKeys(m.ShortHelp()))

		scr := uv.NewScreenBuffer(mouseScreenW, mouseScreenH)
		cur := m.Draw(scr, uv.Rect(0, 0, mouseScreenW, mouseScreenH))
		require.NotNil(t, cur, name)
		x, y := findOnScreen(t, scr, "> Paste the code")
		require.Equal(t, x+2, cur.X, name)
		require.Equal(t, y, cur.Y, name)
	}
}

func TestOAuth_KeysOutsideTheUnfocusedPasteFieldAreShortcuts(t *testing.T) {
	t.Parallel()

	m, _ := newDisplayedOAuth(t, true, false, "")
	require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: 'z', Text: "z"}))
	action, ok := m.HandleMsg(tea.KeyPressMsg{Code: 'u', Text: "u"}).(ActionCmd)
	require.True(t, ok)
	require.NotNil(t, action.Cmd, "u copies the URL")

	require.NotContains(t, screenText(drawMouseDialog(m)), "> z", "nothing was typed into the field")
	require.Equal(t, "enter code", m.ShortHelp()[0].Help().Desc, "the field stays unfocused")
}

func TestOAuth_PasteFieldSubmitsTypedCode(t *testing.T) {
	t.Parallel()

	m, p := newDisplayedOAuth(t, true, false, "")
	m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyTab})

	action, ok := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionCmd)
	require.True(t, ok)
	require.NotNil(t, action.Cmd, "an empty field opens the browser instead")
	require.Empty(t, p.submitted)

	for _, r := range "abc" {
		require.Nil(t, m.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)}))
	}
	require.Contains(t, screenText(drawMouseDialog(m)), "> abc")

	action, ok = m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionCmd)
	require.True(t, ok)
	require.NotNil(t, action.Cmd)
	require.Equal(t, []string{"abc"}, p.submitted)
	require.Equal(t, "enter code", m.ShortHelp()[0].Help().Desc, "submitting leaves the field")
}

// With no browser launcher on PATH, opening the sign-in page fails and the
// dialog must report it rather than wait for a callback that never comes.
func TestOAuth_BrowserThatWontOpenIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows launcher is not found through PATH")
	}
	t.Setenv("PATH", t.TempDir())

	m, _ := newDisplayedOAuth(t, false, false, "")
	action, ok := m.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEnter}).(ActionCmd)
	require.True(t, ok)

	errored, ok := action.Cmd().(ActionOAuthErrored)
	require.True(t, ok)
	require.ErrorContains(t, errored.Error, "failed to open browser")
}
