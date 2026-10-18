package message

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
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

func TestResetStreamedContent(t *testing.T) {
	t.Parallel()

	msg := &Message{}
	msg.AddImageURL("https://example.com/img.png", "high")
	msg.AppendContent("partial answer")
	msg.AppendReasoningContent("thinking...")
	msg.AddToolCall(ToolCall{ID: "1", Name: "bash"})
	msg.AddToolResult(ToolResult{ToolCallID: "1", Content: "output"})
	msg.AddFinish(FinishReasonError, "boom", "stream died")

	msg.ResetStreamedContent()

	// Streamed parts are gone.
	require.Empty(t, msg.Content().Text, "text should be cleared")
	require.Empty(t, msg.ReasoningContent().Thinking, "reasoning should be cleared")
	require.Empty(t, msg.ToolCalls(), "tool calls should be cleared")
	require.Nil(t, msg.FinishPart(), "finish should be cleared")

	// Non-streamed parts survive.
	require.Len(t, msg.ImageURLContent(), 1, "image should survive")
	require.Len(t, msg.ToolResults(), 1, "tool results should survive")
}

func TestToAIMessage_IgnoresToolTimestamps(t *testing.T) {
	t.Parallel()

	untimed := []Message{
		{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "1", Name: "bash", Input: "{}", Finished: true}}},
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "1", Name: "bash", Content: "out"}}},
	}
	timed := []Message{
		{Role: Assistant, Parts: []ContentPart{ToolCall{ID: "1", Name: "bash", Input: "{}", Finished: true, StartedAt: 1_000}}},
		{Role: Tool, Parts: []ContentPart{ToolResult{ToolCallID: "1", Name: "bash", Content: "out", FinishedAt: 12_000}}},
	}

	for i := range untimed {
		want, err := json.Marshal(untimed[i].ToAIMessage())
		require.NoError(t, err)
		got, err := json.Marshal(timed[i].ToAIMessage())
		require.NoError(t, err)
		require.Equal(t, string(want), string(got))
	}
}

func TestMarkToolCallStarted(t *testing.T) {
	t.Parallel()

	msg := &Message{}
	msg.AddToolCall(ToolCall{ID: "1", Name: "bash", Input: "{}", Finished: true})
	msg.AddToolCall(ToolCall{ID: "2", Name: "view", Input: "{}", Finished: true})

	require.True(t, msg.MarkToolCallStarted("2", time.UnixMilli(1_234)))
	require.False(t, msg.MarkToolCallStarted("missing", time.UnixMilli(5_000)))

	require.Equal(t, []ToolCall{
		{ID: "1", Name: "bash", Input: "{}", Finished: true},
		{ID: "2", Name: "view", Input: "{}", Finished: true, StartedAt: 1_234},
	}, msg.ToolCalls())
}

func TestToolCallEditsPreserveStartedAt(t *testing.T) {
	t.Parallel()

	msg := &Message{}
	msg.AddToolCall(ToolCall{ID: "1", Name: "bash", ProviderExecuted: true, StartedAt: 1_000})
	msg.AppendToolCallInput("1", "{}")
	msg.FinishToolCall("1")

	require.Equal(t, []ToolCall{
		{ID: "1", Name: "bash", Input: "{}", ProviderExecuted: true, Finished: true, StartedAt: 1_000},
	}, msg.ToolCalls())
}

func TestResetStreamedContentEmpty(t *testing.T) {
	t.Parallel()

	// Reset on an empty message is a no-op and must not panic.
	msg := &Message{}
	msg.ResetStreamedContent()
	require.Empty(t, msg.Parts)
}

func TestToAIMessage_ByRole(t *testing.T) {
	t.Parallel()

	itemID := "item_1"
	responses := &openai.ResponsesReasoningMetadata{ItemID: itemID, Summary: []string{"s"}}
	pngData := []byte{0x89, 0x50, 0x4E, 0x47}
	validBase64 := base64.StdEncoding.EncodeToString(pngData)

	tests := map[string]struct {
		msg  Message
		want []fantasy.Message
	}{
		"user text with text and image attachments": {
			msg: Message{
				Role: User,
				Parts: []ContentPart{
					TextContent{Text: "  hello  "},
					BinaryContent{Path: "notes.txt", MIMEType: "text/plain", Data: []byte("note body")},
					BinaryContent{Path: "shot.png", MIMEType: "image/png", Data: pngData},
				},
			},
			want: []fantasy.Message{{
				Role: fantasy.MessageRoleUser,
				Content: []fantasy.MessagePart{
					fantasy.TextPart{Text: "hello\n<system_info>The files below have been attached by the user, consider them in your response</system_info>\n<file path='notes.txt'>\n\nnote body\n</file>\n"},
					fantasy.FilePart{Filename: "shot.png", Data: pngData, MediaType: "image/png"},
				},
			}},
		},
		"user with only an image omits the text part": {
			msg: Message{
				Role:  User,
				Parts: []ContentPart{BinaryContent{Path: "shot.png", MIMEType: "image/png", Data: pngData}},
			},
			want: []fantasy.Message{{
				Role:    fantasy.MessageRoleUser,
				Content: []fantasy.MessagePart{fantasy.FilePart{Filename: "shot.png", Data: pngData, MediaType: "image/png"}},
			}},
		},
		"user with no content has no parts": {
			msg:  Message{Role: User, Parts: []ContentPart{TextContent{Text: "   "}}},
			want: []fantasy.Message{{Role: fantasy.MessageRoleUser}},
		},
		"assistant reasoning carries every provider signature": {
			msg: Message{
				Role: Assistant,
				Parts: []ContentPart{
					ReasoningContent{
						Thinking:         "thinking",
						Signature:        "anthropic-sig",
						ThoughtSignature: "google-sig",
						ToolID:           "tool-1",
						ResponsesData:    responses,
					},
					TextContent{Text: " answer "},
					ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"ls"}`, ProviderExecuted: true},
				},
			},
			want: []fantasy.Message{{
				Role: fantasy.MessageRoleAssistant,
				Content: []fantasy.MessagePart{
					fantasy.ReasoningPart{Text: "thinking", ProviderOptions: fantasy.ProviderOptions{
						anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: "anthropic-sig"},
						openai.Name:    responses,
						google.Name:    &google.ReasoningMetadata{Signature: "google-sig", ToolID: "tool-1"},
					}},
					fantasy.TextPart{Text: "answer"},
					fantasy.ToolCallPart{ToolCallID: "call_1", ToolName: "bash", Input: `{"command":"ls"}`, ProviderExecuted: true},
				},
			}},
		},
		"assistant reasoning without signatures has empty provider options": {
			msg: Message{
				Role:  Assistant,
				Parts: []ContentPart{ReasoningContent{Thinking: "thinking"}},
			},
			want: []fantasy.Message{{
				Role:    fantasy.MessageRoleAssistant,
				Content: []fantasy.MessagePart{fantasy.ReasoningPart{Text: "thinking", ProviderOptions: fantasy.ProviderOptions{}}},
			}},
		},
		"assistant with empty reasoning and text has no parts": {
			msg: Message{
				Role:  Assistant,
				Parts: []ContentPart{ReasoningContent{Signature: "sig"}, TextContent{Text: " "}},
			},
			want: []fantasy.Message{{Role: fantasy.MessageRoleAssistant}},
		},
		"tool results by kind": {
			msg: Message{
				Role: Tool,
				Parts: []ContentPart{
					ToolResult{ToolCallID: "err", Content: "boom", Data: validBase64, IsError: true},
					ToolResult{ToolCallID: "text", Content: "plain output"},
					ToolResult{ToolCallID: "media", Content: "ignored", Data: validBase64, MIMEType: "image/png"},
					ToolResult{ToolCallID: "bad-media", Content: "ignored", Data: "not base64!", MIMEType: "image/png"},
				},
			},
			want: []fantasy.Message{{
				Role: fantasy.MessageRoleTool,
				Content: []fantasy.MessagePart{
					fantasy.ToolResultPart{ToolCallID: "err", Output: fantasy.ToolResultOutputContentError{Error: errors.New("boom")}},
					fantasy.ToolResultPart{ToolCallID: "text", Output: fantasy.ToolResultOutputContentText{Text: "plain output"}},
					fantasy.ToolResultPart{ToolCallID: "media", Output: fantasy.ToolResultOutputContentMedia{Data: validBase64, MediaType: "image/png"}},
					fantasy.ToolResultPart{ToolCallID: "bad-media", Output: fantasy.ToolResultOutputContentText{Text: mediaLoadFailedPlaceholder}},
				},
			}},
		},
		"system role produces nothing": {
			msg:  Message{Role: System, Parts: []ContentPart{TextContent{Text: "sys"}}},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.msg.ToAIMessage())
		})
	}
}
