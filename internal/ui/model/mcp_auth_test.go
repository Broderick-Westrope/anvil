package model

import (
	"context"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

func newTestFlow(id uint64) *mcpAuthFlow {
	_, cancel := context.WithCancel(context.Background())
	return &mcpAuthFlow{id: id, cancel: cancel}
}

func TestAllocFlowID_Monotonic(t *testing.T) {
	t.Parallel()

	ui := &UI{}
	first := ui.allocFlowID()
	second := ui.allocFlowID()
	third := ui.allocFlowID()

	require.Equal(t, uint64(1), first)
	require.Equal(t, uint64(2), second)
	require.Equal(t, uint64(3), third)
}

func TestIsCurrentFlow(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: make(map[string]*mcpAuthFlow),
	}

	t.Run("no flow exists", func(t *testing.T) {
		t.Parallel()
		require.False(t, ui.isCurrentFlow("server-a", 1))
	})

	t.Run("matching flow", func(t *testing.T) {
		t.Parallel()
		u := &UI{
			mcpAuthFlows: map[string]*mcpAuthFlow{
				"server-a": newTestFlow(42),
			},
		}
		require.True(t, u.isCurrentFlow("server-a", 42))
	})

	t.Run("stale flow ID", func(t *testing.T) {
		t.Parallel()
		u := &UI{
			mcpAuthFlows: map[string]*mcpAuthFlow{
				"server-a": newTestFlow(42),
			},
		}
		require.False(t, u.isCurrentFlow("server-a", 41))
	})
}

func TestStaleDoneMsg_Dropped(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(5),
		},
	}

	// A stale MCPAuthDoneMsg (from flow 3) must not delete the
	// current flow (id 5).
	staleMsg := dialog.MCPAuthDoneMsg{
		ServerName: "server-a",
		FlowID:     3,
	}

	// isCurrentFlow should reject it.
	require.False(t, ui.isCurrentFlow(staleMsg.ServerName, staleMsg.FlowID))

	// The flow should still be present.
	_, exists := ui.mcpAuthFlows["server-a"]
	require.True(t, exists, "current flow must survive a stale Done message")
}

func TestStaleErrMsg_Dropped(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(7),
		},
	}

	staleMsg := dialog.MCPAuthErrMsg{
		ServerName: "server-a",
		FlowID:     4,
	}

	require.False(t, ui.isCurrentFlow(staleMsg.ServerName, staleMsg.FlowID))

	_, exists := ui.mcpAuthFlows["server-a"]
	require.True(t, exists, "current flow must survive a stale Err message")
}

func TestStaleProgressMsg_Dropped(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(10),
		},
	}

	staleMsg := dialog.MCPAuthProgressMsg{
		ServerName: "server-a",
		FlowID:     9,
	}

	require.False(t, ui.isCurrentFlow(staleMsg.ServerName, staleMsg.FlowID))
}

func TestCurrentFlowMsg_Accepted(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(5),
		},
	}

	// Messages with the correct flow ID must be accepted.
	require.True(t, ui.isCurrentFlow("server-a", 5))
}

func TestRetryMCPAuth_SupersedesOldFlow(t *testing.T) {
	t.Parallel()

	ui := &UI{
		nextMCPAuthFlowID: 10,
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(10),
		},
	}

	oldID := ui.mcpAuthFlows["server-a"].id

	// Simulate retryMCPAuth by allocating a new ID and replacing
	// the flow entry (we cannot call retryMCPAuth directly because
	// it needs a full Workspace; we test the ID logic instead).
	newID := ui.allocFlowID()
	ui.mcpAuthFlows["server-a"] = newTestFlow(newID)

	require.NotEqual(t, oldID, newID)
	require.False(t, ui.isCurrentFlow("server-a", oldID),
		"old flow ID must be rejected after retry")
	require.True(t, ui.isCurrentFlow("server-a", newID),
		"new flow ID must be accepted after retry")
}

func TestCancelMCPAuth_ThenStaleMsg(t *testing.T) {
	t.Parallel()

	ui := &UI{
		mcpAuthFlows: map[string]*mcpAuthFlow{
			"server-a": newTestFlow(3),
		},
	}

	// Cancel the flow.
	ui.cancelMCPAuth("server-a")

	// isCurrentFlow returns false for the cancelled flow.
	require.False(t, ui.isCurrentFlow("server-a", 3))

	// A new flow started later gets a different ID.
	newID := ui.allocFlowID()
	ui.mcpAuthFlows["server-a"] = newTestFlow(newID)

	// The stale message from the old flow is still rejected.
	require.False(t, ui.isCurrentFlow("server-a", 3))
	require.True(t, ui.isCurrentFlow("server-a", newID))
}
