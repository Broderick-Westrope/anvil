package model

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/Broderick-Westrope/anvil/internal/ui/util"
	"github.com/stretchr/testify/require"
)

// configFieldWorkspace records SetConfigField calls and can fail them.
type configFieldWorkspace struct {
	*testWorkspace
	writes []configWrite
	err    error
}

type configWrite struct {
	scope config.Scope
	key   string
	value any
}

func (w *configFieldWorkspace) SetConfigField(scope config.Scope, key string, value any) error {
	if w.err != nil {
		return w.err
	}
	w.writes = append(w.writes, configWrite{scope, key, value})
	return nil
}

func newMouseToggleUI(t *testing.T, enabled bool, writeErr error) (*UI, *configFieldWorkspace) {
	t.Helper()
	u := newTestUI()
	ws := &configFieldWorkspace{testWorkspace: &testWorkspace{cfg: visionConfig(true)}, err: writeErr}
	u.com.Workspace = ws
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(attachments.NewRenderer(u.com.Styles.Attachments), attachments.Keymap{})
	u.keyMap = DefaultKeyMap()
	u.status = NewStatus(u.com, u)
	u.mouseEnabled = enabled
	return u, ws
}

func TestMouseMode(t *testing.T) {
	t.Parallel()

	require.Equal(t, tea.MouseModeCellMotion, mouseMode(true))
	require.Equal(t, tea.MouseModeNone, mouseMode(false))
}

func TestMouseEnabledByConfig(t *testing.T) {
	t.Parallel()

	on, off := true, false
	require.True(t, mouseEnabledByConfig(nil))
	require.True(t, mouseEnabledByConfig(&config.Options{}), "no tui block defaults on")
	require.True(t, mouseEnabledByConfig(&config.Options{TUI: &config.TUIOptions{}}), "unset defaults on")
	require.True(t, mouseEnabledByConfig(&config.Options{TUI: &config.TUIOptions{Mouse: &on}}))
	require.False(t, mouseEnabledByConfig(&config.Options{TUI: &config.TUIOptions{Mouse: &off}}))
}

func TestToggleMouseSupport(t *testing.T) {
	t.Parallel()

	u, ws := newMouseToggleUI(t, true, nil)
	u.textareaMouseSelecting = true

	msg := u.toggleMouseSupport()()

	require.False(t, u.mouseEnabled)
	require.False(t, u.textareaMouseSelecting, "a drag can't finish once capture is off")
	require.Equal(t, tea.MouseModeNone, u.View().MouseMode)
	require.Equal(t, []configWrite{{config.ScopeGlobal, "options.tui.mouse", false}}, ws.writes)
	require.Equal(t, "Mouse support disabled", msg.(util.InfoMsg).Msg)

	msg = u.toggleMouseSupport()()

	require.True(t, u.mouseEnabled)
	require.Equal(t, tea.MouseModeCellMotion, u.View().MouseMode)
	require.Equal(t, configWrite{config.ScopeGlobal, "options.tui.mouse", true}, ws.writes[1])
	require.Equal(t, "Mouse support enabled", msg.(util.InfoMsg).Msg)
}

func TestToggleMouseSupportKeepsStateWhenSaveFails(t *testing.T) {
	t.Parallel()

	u, _ := newMouseToggleUI(t, true, errors.New("read-only config"))

	msg := u.toggleMouseSupport()()

	require.True(t, u.mouseEnabled, "the UI must match the saved setting")
	info, ok := msg.(util.InfoMsg)
	require.True(t, ok)
	require.Equal(t, util.InfoTypeError, info.Type)
}
