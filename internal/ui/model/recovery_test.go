package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
	"github.com/stretchr/testify/require"
)

type recoveryWorkspace struct {
	workspace.Workspace
}

func (*recoveryWorkspace) WorkingDir() string { return "/current" }

func (*recoveryWorkspace) AgentIsReady() bool { return false }

func (*recoveryWorkspace) PermissionYoloLevel() config.YoloLevel { return config.YoloOff }

func TestRecoveryTracksRootSessionNotDrilledInChild(t *testing.T) {
	t.Parallel()
	model := newTestUI()
	model.com.Workspace = &recoveryWorkspace{}
	var entries []recovery.Entry
	model.SetRecoveryHandler(func(entry recovery.Entry) { entries = append(entries, entry) })
	model.session = &session.Session{ID: "parent", Title: "Parent title", WorkingDir: "/original"}
	model.drillStack = []drillInEntry{{sessionID: "child", chat: model.chat}}
	_, _ = model.Update(tea.BlurMsg{})
	require.Len(t, entries, 1)
	require.Equal(t, "parent", entries[0].SessionID)
	require.Equal(t, model.com.Workspace.WorkingDir(), entries[0].WorkingDir)
	_, _ = model.Update(tea.BlurMsg{})
	require.Len(t, entries, 1)
	model.session.Title = "Renamed"
	_, _ = model.Update(tea.BlurMsg{})
	require.Len(t, entries, 2)
	require.Equal(t, "Renamed", entries[1].Title)
	model.session = &session.Session{ID: "other", Title: "Other"}
	_, _ = model.Update(tea.BlurMsg{})
	require.Len(t, entries, 3)
	require.Equal(t, "other", entries[2].SessionID)
	model.session = nil
	_, _ = model.Update(tea.BlurMsg{})
	require.Len(t, entries, 4)
	require.Empty(t, entries[3].SessionID)
}
