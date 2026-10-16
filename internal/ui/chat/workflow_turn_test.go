package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/commands"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestWorkflowTurnRendersAsWorkflowMessage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	prompt := skills.FormatContentXML("go-style", "secret skill body") + "\n\nBuild the **thing**."
	msg := &message.Message{
		ID:    "turn-1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: commands.FormatWorkflowTurnXML("build", prompt)}},
	}

	items := ExtractMessageItems(&sty, msg, nil, ToolItemOptions{})
	require.Len(t, items, 1)
	_, ok := items[0].(*WorkflowTurnMessageItem)
	require.True(t, ok, "workflow turns must not render as user messages")

	out := ansi.Strip(items[0].Render(80))
	require.Contains(t, out, "Workflow /build")
	require.Contains(t, out, "Build the thing.")
	require.Contains(t, out, "Using skill: go-style")
	require.NotContains(t, out, "secret skill body")
	require.NotContains(t, out, "workflow_turn")
}

func TestUserMessageStaysUserMessage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	msg := &message.Message{
		ID:    "user-1",
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "plain"}},
	}
	items := ExtractMessageItems(&sty, msg, nil, ToolItemOptions{})
	_, ok := items[0].(*UserMessageItem)
	require.True(t, ok)
}
