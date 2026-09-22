package chat

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/ui/attachments"
	"github.com/Broderick-Westrope/anvil/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

func TestUserMessageItemSourceMessage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	renderer := attachments.NewRenderer(sty.Attachments.Normal, sty.Attachments.Deleting, sty.Attachments.Image, sty.Attachments.Text, sty.Attachments.Skill, sty.Attachments.Remove)

	rawText := "<skill_content name=\"foo\">\ninstructions\n</skill_content>\n\n/review please"
	original := &message.Message{
		ID:   "user-1",
		Role: message.User,
		Parts: []message.ContentPart{
			message.TextContent{Text: rawText},
			message.BinaryContent{Path: "/tmp/a.png", MIMEType: "image/png", Data: []byte{1, 2, 3}},
		},
	}

	item := NewUserMessageItem(&sty, original, renderer)
	provider, ok := item.(SourceMessageProvider)
	require.True(t, ok, "UserMessageItem must implement SourceMessageProvider")

	clone := provider.SourceMessage()
	require.Equal(t, rawText, clone.Content().Text, "raw text including skill/command XML must be preserved verbatim")

	require.Len(t, clone.BinaryContent(), 1)
	require.Equal(t, []byte{1, 2, 3}, clone.BinaryContent()[0].Data)

	cloneBinary := clone.Parts[1].(message.BinaryContent)
	cloneBinary.Data[0] = 99
	require.Equal(t, byte(1), original.Parts[1].(message.BinaryContent).Data[0], "cloning must deep-copy BinaryContent.Data")

	original.Parts[1] = message.BinaryContent{Path: "/tmp/a.png", MIMEType: "image/png", Data: []byte{7, 7, 7}}
	require.Equal(t, byte(99), clone.Parts[1].(message.BinaryContent).Data[0])
}

func TestAssistantMessageItemSourceMessage(t *testing.T) {
	t.Parallel()

	sty := styles.TokyoNight()
	original := &message.Message{
		ID:   "asst-1",
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.TextContent{Text: "final answer"},
			message.Finish{Reason: message.FinishReasonEndTurn},
		},
	}

	item := NewAssistantMessageItem(&sty, original)
	provider, ok := item.(SourceMessageProvider)
	require.True(t, ok, "AssistantMessageItem must implement SourceMessageProvider")

	clone := provider.SourceMessage()
	require.Equal(t, "final answer", clone.Content().Text)
	require.Equal(t, message.FinishReasonEndTurn, clone.FinishReason())
	require.Empty(t, clone.ToolCalls())
}
