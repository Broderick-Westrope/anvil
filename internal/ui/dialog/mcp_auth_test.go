package dialog

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/mcpauth"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestMCPAuth(t *testing.T) *MCPAuth {
	t.Helper()
	s := styles.TokyoNight()
	com := &common.Common{Styles: &s}
	d, _ := NewMCPAuth(com, "test-server")
	return d
}

func TestMCPAuth_ProgressMovesToAwaitingBrowser(t *testing.T) {
	t.Parallel()
	d := newTestMCPAuth(t)

	action := d.HandleMsg(MCPAuthProgressMsg{
		ServerName: "test-server",
		Stage:      mcpauth.StageAwaitingBrowser,
		Detail:     "https://example.com/auth",
	})
	require.Nil(t, action)
	require.Equal(t, mcpAuthStateAwaitingBrowser, d.state)
	require.Equal(t, "https://example.com/auth", d.authURL)
	require.Equal(t, "Complete sign-in in your browser", d.stageMsg)
}

func TestMCPAuth_ErrMovesToError_RetryReturnsAction(t *testing.T) {
	t.Parallel()
	d := newTestMCPAuth(t)

	testErr := errors.New("token exchange failed")
	action := d.HandleMsg(MCPAuthErrMsg{
		ServerName: "test-server",
		Err:        testErr,
	})
	require.Nil(t, action)
	require.Equal(t, mcpAuthStateError, d.state)
	require.Equal(t, testErr, d.err)

	// Press 'r' to retry.
	action = d.HandleMsg(keyMsg('r'))
	retry, ok := action.(ActionRetryMCPAuth)
	require.True(t, ok, "expected ActionRetryMCPAuth, got %T", action)
	require.Equal(t, "test-server", retry.ServerName)
}

func TestMCPAuth_EscReturnsCancelFromEveryState(t *testing.T) {
	t.Parallel()

	esc := tea.KeyPressMsg{Code: tea.KeyEscape}

	tests := []struct {
		name  string
		setup func(*MCPAuth)
	}{
		{"working", func(_ *MCPAuth) {}},
		{"awaiting_browser", func(d *MCPAuth) {
			d.HandleMsg(MCPAuthProgressMsg{
				ServerName: "test-server",
				Stage:      mcpauth.StageAwaitingBrowser,
				Detail:     "https://example.com/auth",
			})
		}},
		{"error", func(d *MCPAuth) {
			d.HandleMsg(MCPAuthErrMsg{
				ServerName: "test-server",
				Err:        errors.New("fail"),
			})
		}},
		{"success", func(d *MCPAuth) {
			d.state = mcpAuthStateSuccess
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newTestMCPAuth(t)
			tc.setup(d)
			action := d.HandleMsg(esc)
			cancel, ok := action.(ActionCancelMCPAuth)
			require.True(t, ok, "expected ActionCancelMCPAuth, got %T", action)
			require.Equal(t, "test-server", cancel.ServerName)
		})
	}
}

func TestMCPAuth_MismatchedServerNameIgnored(t *testing.T) {
	t.Parallel()
	d := newTestMCPAuth(t)

	// Progress with wrong server name should be ignored.
	action := d.HandleMsg(MCPAuthProgressMsg{
		ServerName: "other-server",
		Stage:      mcpauth.StageAwaitingBrowser,
		Detail:     "https://example.com/auth",
	})
	require.Nil(t, action)
	require.Equal(t, mcpAuthStateWorking, d.state,
		"state should remain working when ServerName does not match")

	// Done with wrong server name should be ignored.
	action = d.HandleMsg(MCPAuthDoneMsg{ServerName: "other-server"})
	require.Nil(t, action)
	require.Equal(t, mcpAuthStateWorking, d.state)

	// Error with wrong server name should be ignored.
	action = d.HandleMsg(MCPAuthErrMsg{
		ServerName: "other-server",
		Err:        errors.New("fail"),
	})
	require.Nil(t, action)
	require.Equal(t, mcpAuthStateWorking, d.state)
}

func TestMCPAuth_DoneReturnsActionClose(t *testing.T) {
	t.Parallel()
	d := newTestMCPAuth(t)

	action := d.HandleMsg(MCPAuthDoneMsg{ServerName: "test-server"})
	_, ok := action.(ActionClose)
	require.True(t, ok, "expected ActionClose, got %T", action)
	require.Equal(t, mcpAuthStateSuccess, d.state)
}

func TestMCPAuth_RetryIgnoredOutsideErrorState(t *testing.T) {
	t.Parallel()
	d := newTestMCPAuth(t)

	// 'r' in working state should do nothing.
	action := d.HandleMsg(keyMsg('r'))
	require.Nil(t, action, "retry should be ignored outside error state")
}
