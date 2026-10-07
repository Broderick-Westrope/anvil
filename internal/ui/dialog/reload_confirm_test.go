package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Broderick-Westrope/anvil/internal/ui/common"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func newTestReloadConfirm(jobs []string, attachments bool) *ReloadConfirm {
	s := styles.TokyoNight()
	return NewReloadConfirm(&common.Common{Styles: &s}, "v2.0.0", jobs, attachments)
}

func TestReloadConfirm_Lines(t *testing.T) {
	t.Parallel()

	r := newTestReloadConfirm([]string{"sleep 300", "npm run dev", "go test ./...", "tail -f log", "watch"}, true)
	require.Equal(t, []string{
		"Reload onto anvil v2.0.0?",
		"",
		"These background jobs will be stopped:",
		"• sleep 300",
		"• npm run dev",
		"• go test ./...",
		"+2 more",
		"",
		"Attachments will be dropped.",
	}, r.lines())

	r = newTestReloadConfirm(nil, true)
	require.Equal(t, []string{"Reload onto anvil v2.0.0?", "", "Attachments will be dropped."}, r.lines())

	r = newTestReloadConfirm([]string{"one"}, false)
	require.Equal(t, []string{"Reload onto anvil v2.0.0?", "", "These background jobs will be stopped:", "• one"}, r.lines())
}

func TestReloadConfirm_Keys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want Action
	}{
		{"enter defaults to cancel", []tea.KeyPressMsg{enterKey()}, ActionReloadInstanceCancel{}},
		{"tab then enter confirms", []tea.KeyPressMsg{tabKey(), enterKey()}, ActionReloadInstanceConfirm{}},
		{"y confirms", []tea.KeyPressMsg{{Code: 'y', Text: "y"}}, ActionReloadInstanceConfirm{}},
		{"n cancels", []tea.KeyPressMsg{{Code: 'n', Text: "n"}}, ActionReloadInstanceCancel{}},
		{"esc cancels", []tea.KeyPressMsg{escKey()}, ActionReloadInstanceCancel{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newTestReloadConfirm([]string{"sleep 300"}, false)
			var got Action
			for _, k := range tt.keys {
				got = r.HandleMsg(k)
			}
			require.Equal(t, tt.want, got)
		})
	}
}
