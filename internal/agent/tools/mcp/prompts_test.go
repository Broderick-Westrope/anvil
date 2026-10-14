package mcp

import (
	"slices"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// This test cannot be parallel: it registers prompts in the package-level
// prompt registry.
func TestPromptCommands(t *testing.T) {
	const server = "test-prompt-commands"
	updatePrompts(server, []*Prompt{
		{
			Name:        "review",
			Title:       "Review",
			Description: "Reviews a file.",
			Arguments: []*mcp.PromptArgument{
				{Name: "file", Title: "File path", Description: "The file to review.", Required: true},
				{Name: "depth", Description: "How deep to go."},
			},
		},
		{Name: "summarize"},
	})
	t.Cleanup(func() { updatePrompts(server, nil) })

	got := slices.DeleteFunc(PromptCommands(), func(c commands.MCPPrompt) bool { return c.ClientID != server })

	require.Equal(t, []commands.MCPPrompt{
		{
			ID:          "test-prompt-commands:review",
			Title:       "Review",
			Description: "Reviews a file.",
			PromptID:    "review",
			ClientID:    server,
			Arguments: []commands.Argument{
				{ID: "file", Title: "File path", Description: "The file to review.", Required: true},
				{ID: "depth", Title: "depth", Description: "How deep to go."},
			},
		},
		{
			ID:       "test-prompt-commands:summarize",
			PromptID: "summarize",
			ClientID: server,
		},
	}, got)
}

func TestPromptCommandsNeverNil(t *testing.T) {
	require.NotNil(t, PromptCommands())
}
