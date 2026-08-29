package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func newLazyMCPTestUI() *UI {
	return &UI{
		enabledLazyMCPs:   make(map[string]bool),
		pendingEnableMCPs: make(map[string]string),
	}
}

func TestDeriveEnabledLazyMCPs_EnableMCPToolCall(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.deriveEnabledLazyMCPs([]message.Message{
		{
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     "enable_mcp",
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "tc1",
					Content:    "Enabled datadog MCP (5 tools available)",
					IsError:    false,
				},
			},
		},
	})

	require.True(t, ui.enabledLazyMCPs["datadog"])
}

func TestDeriveEnabledLazyMCPs_ToggleContentLastWins(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.deriveEnabledLazyMCPs([]message.Message{
		{
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     "enable_mcp",
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "tc1",
					Content:    "Enabled datadog MCP",
					IsError:    false,
				},
			},
		},
		{
			MessageType: message.MessageTypeMCPToggle,
			Parts: []message.ContentPart{
				message.MCPToggleContent{ServerName: "datadog", Enabled: false},
			},
		},
	})

	require.False(t, ui.enabledLazyMCPs["datadog"])
}

func TestDeriveEnabledLazyMCPs_ErroredEnableNotCounted(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.deriveEnabledLazyMCPs([]message.Message{
		{
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       "tc1",
					Name:     "enable_mcp",
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
				},
			},
		},
		{
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "tc1",
					Content:    "Failed to connect MCP 'datadog': timeout",
					IsError:    true,
				},
			},
		},
	})

	require.Empty(t, ui.enabledLazyMCPs, "errored enable must not count")
}

func TestApplyLazyMCPMessageParts_UnfinishedToolCallIgnored(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{
				Name:     "enable_mcp",
				Input:    `{"server_name":"data`,
				Finished: false,
			},
		},
	})

	require.Empty(t, ui.enabledLazyMCPs)
}

func TestApplyLazyMCPMessageParts_BadJSONIgnored(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{
				Name:     "enable_mcp",
				Input:    `{not json`,
				Finished: true,
			},
		},
	})

	require.Empty(t, ui.enabledLazyMCPs)
}

func TestApplyLazyMCPMessageParts_OtherToolsIgnored(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{
				Name:     "bash",
				Input:    `{"server_name":"datadog"}`,
				Finished: true,
			},
		},
	})

	require.Empty(t, ui.enabledLazyMCPs)
}

// TestApplyLazyMCPMessageParts_ToolCallThenSuccessResult verifies the
// two-phase flow: ToolCall records pending, ToolResult resolves it.
func TestApplyLazyMCPMessageParts_ToolCallThenSuccessResult(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()

	// Phase 1: ToolCall arrives — should NOT yet mark enabled.
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "tc1",
				Name:     "enable_mcp",
				Input:    `{"server_name":"datadog"}`,
				Finished: true,
			},
		},
	})
	require.Empty(t, ui.enabledLazyMCPs, "ToolCall alone must not mark enabled")
	require.Contains(t, ui.pendingEnableMCPs, "tc1")

	// Phase 2: Successful ToolResult arrives — should mark enabled.
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "tc1",
				Content:    "Enabled datadog MCP (5 tools available)",
				IsError:    false,
			},
		},
	})
	require.True(t, ui.enabledLazyMCPs["datadog"])
	require.Empty(t, ui.pendingEnableMCPs)
}

// TestApplyLazyMCPMessageParts_ToolCallThenErrorResult verifies that a
// failed enable_mcp does not mark the server as enabled.
func TestApplyLazyMCPMessageParts_ToolCallThenErrorResult(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()

	// Phase 1: ToolCall arrives.
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "tc1",
				Name:     "enable_mcp",
				Input:    `{"server_name":"datadog"}`,
				Finished: true,
			},
		},
	})
	require.Empty(t, ui.enabledLazyMCPs)

	// Phase 2: Error ToolResult — must NOT mark enabled.
	ui.applyLazyMCPMessageParts(message.Message{
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "tc1",
				Content:    "Failed to connect MCP 'datadog': timeout",
				IsError:    true,
			},
		},
	})
	require.Empty(t, ui.enabledLazyMCPs, "errored result must not mark enabled")
	require.Empty(t, ui.pendingEnableMCPs)
}
