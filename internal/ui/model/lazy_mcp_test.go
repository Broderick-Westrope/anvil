package model

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func newLazyMCPTestUI() *UI {
	return &UI{enabledLazyMCPs: make(map[string]bool)}
}

func TestDeriveEnabledLazyMCPs_EnableMCPToolCall(t *testing.T) {
	t.Parallel()

	ui := newLazyMCPTestUI()
	ui.deriveEnabledLazyMCPs([]message.Message{
		{
			Parts: []message.ContentPart{
				message.ToolCall{
					Name:     "enable_mcp",
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
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
					Name:     "enable_mcp",
					Input:    `{"server_name":"datadog"}`,
					Finished: true,
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
