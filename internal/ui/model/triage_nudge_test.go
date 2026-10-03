package model

import (
	"errors"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCheckTriageNudge(t *testing.T) {
	t.Parallel()

	t.Run("below threshold", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{cfg: &config.Config{}, unresolvedCount: 49}
		require.Nil(t, u.checkTriageNudge()())
	})

	t.Run("at threshold with no prior triage", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{cfg: &config.Config{}, unresolvedCount: 50}
		msg := u.checkTriageNudge()()
		nudge, ok := msg.(triageNudgeMsg)
		require.True(t, ok)
		require.Equal(t, 50, nudge.count)
	})

	t.Run("at threshold but triaged recently", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{
			cfg:             &config.Config{},
			unresolvedCount: 50,
			lastTriage:      time.Now().Add(-24 * time.Hour),
		}
		require.Nil(t, u.checkTriageNudge()())
	})

	t.Run("error counting", func(t *testing.T) {
		t.Parallel()
		u := newTestUI()
		u.com.Workspace = &testWorkspace{cfg: &config.Config{}, unresolvedCount: 50, unresolvedErr: errors.New("boom")}
		require.Nil(t, u.checkTriageNudge()())
	})
}

func TestUpdate_TriageNudgeMsgShowsOnce(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &testWorkspace{cfg: &config.Config{}}

	_, cmd := u.Update(triageNudgeMsg{count: 50})
	msgs := collectMsgs(cmd)
	_, ok := findInfoMsg(msgs)
	require.True(t, ok, "first delivery should show the nudge")
	require.True(t, u.triageNudgeShown)

	_, cmd2 := u.Update(triageNudgeMsg{count: 50})
	msgs2 := collectMsgs(cmd2)
	_, ok = findInfoMsg(msgs2)
	require.False(t, ok, "second delivery should not show the nudge again")
}
