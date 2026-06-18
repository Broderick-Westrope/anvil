package message

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func makeTestAttachments(n int, contentSize int) []Attachment {
	attachments := make([]Attachment, n)
	content := []byte(strings.Repeat("x", contentSize))
	for i := range n {
		attachments[i] = Attachment{
			FilePath: fmt.Sprintf("/path/to/file%d.txt", i),
			MimeType: "text/plain",
			Content:  content,
		}
	}
	return attachments
}

func TestToAIMessage_CorruptedMediaData(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_123",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "abc\x80def",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_123", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "corrupted media should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func TestToAIMessage_ValidMediaData(t *testing.T) {
	t.Parallel()

	validBase64 := base64.StdEncoding.EncodeToString([]byte{0x89, 0x50, 0x4E, 0x47})

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_456",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       validBase64,
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_456", part.ToolCallID)

	mediaContent, ok := part.Output.(fantasy.ToolResultOutputContentMedia)
	require.True(t, ok, "valid media should remain as media")
	require.Equal(t, validBase64, mediaContent.Data)
	require.Equal(t, "image/png", mediaContent.MediaType)
}

func TestToAIMessage_ASCIIButInvalidBase64(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Tool,
		Parts: []ContentPart{
			ToolResult{
				ToolCallID: "call_789",
				Name:       "screenshot",
				Content:    "Loaded image/png content",
				Data:       "not-valid-base64!!!",
				MIMEType:   "image/png",
			},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 1)

	part, ok := messages[0].Content[0].(fantasy.ToolResultPart)
	require.True(t, ok)

	require.Equal(t, "call_789", part.ToolCallID)

	textContent, ok := part.Output.(fantasy.ToolResultOutputContentText)
	require.True(t, ok, "ASCII but invalid base64 should be downgraded to text")
	require.Equal(t, mediaLoadFailedPlaceholder, textContent.Text)
}

func TestToAIMessage_AssistantReasoningBeforeText(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Assistant,
		Parts: []ContentPart{
			ReasoningContent{
				Thinking:  "Let me think about this...",
				Signature: "sig123",
			},
			TextContent{Text: "Here is my response"},
			ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"ls"}`},
		},
	}

	messages := msg.ToAIMessage()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].Content, 3)

	_, ok := messages[0].Content[0].(fantasy.ReasoningPart)
	require.True(t, ok, "first part should be ReasoningPart")
	_, ok = messages[0].Content[1].(fantasy.TextPart)
	require.True(t, ok, "second part should be TextPart")
	_, ok = messages[0].Content[2].(fantasy.ToolCallPart)
	require.True(t, ok, "third part should be ToolCallPart")
}

func TestAppendReasoningContent_PreservesAllFields(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Assistant,
		Parts: []ContentPart{
			ReasoningContent{
				Thinking:         "initial",
				Signature:        "sig",
				ThoughtSignature: "tsig",
				ToolID:           "tid",
				StartedAt:        100,
				FinishedAt:       200,
			},
		},
	}

	msg.AppendReasoningContent(" more")
	rc := msg.ReasoningContent()
	require.Equal(t, "initial more", rc.Thinking)
	require.Equal(t, "sig", rc.Signature)
	require.Equal(t, "tsig", rc.ThoughtSignature)
	require.Equal(t, "tid", rc.ToolID)
	require.Equal(t, int64(100), rc.StartedAt)
	require.Equal(t, int64(200), rc.FinishedAt)
}

func TestAppendReasoningSignature_PreservesAllFields(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Assistant,
		Parts: []ContentPart{
			ReasoningContent{
				Thinking:         "thought",
				Signature:        "sig",
				ThoughtSignature: "tsig",
				ToolID:           "tid",
				StartedAt:        100,
			},
		},
	}

	msg.AppendReasoningSignature("more")
	rc := msg.ReasoningContent()
	require.Equal(t, "thought", rc.Thinking)
	require.Equal(t, "sigmore", rc.Signature)
	require.Equal(t, "tsig", rc.ThoughtSignature)
	require.Equal(t, "tid", rc.ToolID)
}

func TestFinishThinking_PreservesAllFields(t *testing.T) {
	t.Parallel()

	msg := &Message{
		Role: Assistant,
		Parts: []ContentPart{
			ReasoningContent{
				Thinking:         "thought",
				Signature:        "sig",
				ThoughtSignature: "tsig",
				ToolID:           "tid",
				StartedAt:        100,
			},
		},
	}

	msg.FinishThinking()
	rc := msg.ReasoningContent()
	require.Equal(t, "thought", rc.Thinking)
	require.Equal(t, "sig", rc.Signature)
	require.Equal(t, "tsig", rc.ThoughtSignature)
	require.Equal(t, "tid", rc.ToolID)
	require.NotZero(t, rc.FinishedAt)
}

func BenchmarkPromptWithTextAttachments(b *testing.B) {
	cases := []struct {
		name        string
		numFiles    int
		contentSize int
	}{
		{"1file_100bytes", 1, 100},
		{"5files_1KB", 5, 1024},
		{"10files_10KB", 10, 10 * 1024},
		{"20files_50KB", 20, 50 * 1024},
	}

	for _, tc := range cases {
		attachments := makeTestAttachments(tc.numFiles, tc.contentSize)
		prompt := "Process these files"

		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				_ = PromptWithTextAttachments(prompt, attachments)
			}
		})
	}
}
