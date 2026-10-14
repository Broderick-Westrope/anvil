package model

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/app/workflowhost"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

// TestWorkflowKeyPrecedence pins where the workflow keys sit among the
// UI's other key handling in each state, through UI.Update.
func TestWorkflowKeyPrecedence(t *testing.T) {
	t.Parallel()

	pauseKey := tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}
	tests := map[string]struct {
		setup      func(t *testing.T, f *workflowUIFixture)
		key        tea.KeyPressMsg
		wantPaused bool
	}{
		"editor with text": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.focus = uiFocusEditor
				f.u.textarea.SetValue("draft")
			},
			key: pauseKey, wantPaused: true,
		},
		"slash autocomplete open": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.focus = uiFocusEditor
				f.press(tea.KeyPressMsg{Code: '/', Text: "/"})
				require.True(t, f.u.slashACOpen)
			},
			// The open autocomplete takes keys before the global ones.
			key: pauseKey,
		},
		"main focus with a selected chat item": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.setSessionMessages([]message.Message{{
					ID: "m1", SessionID: "sess-1", Role: message.User,
					Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
				}})
				f.u.focus = uiFocusMain
				f.u.chat.SelectLast()
			},
			key: pauseKey, wantPaused: true,
		},
		"sidebar focus": {
			setup: func(t *testing.T, f *workflowUIFixture) { f.u.focus = uiFocusSidebar },
			key:   pauseKey, wantPaused: true,
		},
		"a dialog open": {
			setup: func(t *testing.T, f *workflowUIFixture) { f.u.dialog.OpenDialog(&stubDialog{id: "stub"}) },
			key:   pauseKey,
		},
		"an upstream binding that matches but doesn't apply": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.focus = uiFocusMain
				f.u.keyMap.WorkflowPause = key.NewBinding(key.WithKeys("h"))
			},
			key: tea.KeyPressMsg{Code: 'h', Text: "h"},
		},
		"a rebound key": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.keyMap.WorkflowPause = key.NewBinding(key.WithKeys("f1"))
			},
			key: tea.KeyPressMsg{Code: tea.KeyF1}, wantPaused: true,
		},
		"the old key once rebound": {
			setup: func(t *testing.T, f *workflowUIFixture) {
				f.u.keyMap.WorkflowPause = key.NewBinding(key.WithKeys("f1"))
			},
			key: pauseKey,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newWorkflowUIFixture(t)
			f.event(started("demo", "sess-1"))
			tc.setup(t, f)
			f.press(tc.key)
			if tc.wantPaused {
				require.Equal(t, []string{"pause:sess-1"}, f.ws.controls)
			} else {
				require.Empty(t, f.ws.controls)
			}
		})
	}
}

func TestWorkflowFollowKeyWorksFromLanding(t *testing.T) {
	t.Parallel()

	f := newWorkflowUIFixture(t)
	f.u.session = nil
	f.u.setState(uiLanding, uiFocusEditor)
	f.u.textarea.SetValue("draft")
	infos := f.event(workflowhost.Event{Kind: workflowhost.EventFocus, RunID: "run-1", Command: "demo", SessionID: "sess-1", Focus: "sess-2"})
	require.Len(t, infos, 1)
	require.Nil(t, f.u.session)

	f.press(tea.KeyPressMsg{Code: 'g', Mod: tea.ModAlt})
	require.NotNil(t, f.u.session)
	require.Equal(t, "sess-2", f.u.session.ID)
	require.Equal(t, "draft", f.u.textarea.Value())
}
