package message_test

import (
	"strings"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestFilterBranchPathForContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []message.Message
		verify func(t *testing.T, result []message.Message)
	}{
		{
			name:  "no_messages",
			input: []message.Message{},
			verify: func(t *testing.T, result []message.Message) {
				require.Empty(t, result)
			},
		},
		{
			name: "no_compaction",
			input: []message.Message{
				{
					ID:          "msg-1",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "hello"}},
				},
				{
					ID:          "msg-2",
					Role:        message.Assistant,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "hi there"}},
				},
				{
					ID:          "msg-3",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "bye"}},
				},
			},
			verify: func(t *testing.T, result []message.Message) {
				require.Len(t, result, 3)
				require.Equal(t, "hello", result[0].Parts[0].(message.TextContent).Text)
				require.Equal(t, "hi there", result[1].Parts[0].(message.TextContent).Text)
				require.Equal(t, "bye", result[2].Parts[0].(message.TextContent).Text)
			},
		},
		{
			name: "metadata_filtered",
			input: []message.Message{
				{
					ID:          "msg-1",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "hello"}},
				},
				{
					ID:          "msg-label",
					Role:        message.Assistant,
					MessageType: message.MessageTypeLabel,
					Parts:       []message.ContentPart{message.LabelContent{Label: "test"}},
				},
				{
					ID:          "msg-model",
					Role:        message.Assistant,
					MessageType: message.MessageTypeModelChange,
					Parts:       []message.ContentPart{message.ModelChangeContent{ModelID: "gpt-4"}},
				},
				{
					ID:          "msg-thinking",
					Role:        message.Assistant,
					MessageType: message.MessageTypeThinkingLevelChange,
					Parts:       []message.ContentPart{message.ThinkingLevelChangeContent{ThinkingLevel: "high"}},
				},
				{
					ID:          "msg-2",
					Role:        message.Assistant,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "response"}},
				},
			},
			verify: func(t *testing.T, result []message.Message) {
				// Only the user message and the assistant message should remain.
				require.Len(t, result, 2)
				require.Equal(t, "hello", result[0].Parts[0].(message.TextContent).Text)
				require.Equal(t, "response", result[1].Parts[0].(message.TextContent).Text)
			},
		},
		{
			name: "single_compaction",
			input: []message.Message{
				{
					ID:          "msg-1",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "old message"}},
				},
				{
					ID:          "msg-2",
					Role:        message.Assistant,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "old reply"}},
				},
				{
					ID:          "msg-kept",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "kept message"}},
				},
				{
					ID:          "msg-compact",
					Role:        message.Assistant,
					MessageType: message.MessageTypeCompaction,
					Parts: []message.ContentPart{message.CompactionContent{
						Summary:          "This is a summary",
						FirstKeptEntryID: "msg-kept",
						TokensBefore:     1000,
					}},
				},
				{
					ID:          "msg-after",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "after compaction"}},
				},
			},
			verify: func(t *testing.T, result []message.Message) {
				// Should have: synthetic summary, kept message, after-compaction message.
				require.Len(t, result, 3)

				// First is the synthetic summary.
				summaryText := result[0].Parts[0].(message.TextContent).Text
				require.Contains(t, summaryText, "This is a summary")
				require.Contains(t, summaryText, "<summary>")
				require.Equal(t, message.User, result[0].Role)

				// Second is the kept message.
				require.Equal(t, "kept message", result[1].Parts[0].(message.TextContent).Text)

				// Third is the post-compaction message.
				require.Equal(t, "after compaction", result[2].Parts[0].(message.TextContent).Text)
			},
		},
		{
			name: "branch_summary_converted",
			input: []message.Message{
				{
					ID:          "msg-1",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "hello"}},
				},
				{
					ID:          "msg-bs",
					Role:        message.Assistant,
					MessageType: message.MessageTypeBranchSummary,
					Parts:       []message.ContentPart{message.BranchSummaryContent{Summary: "branch summary text"}},
				},
				{
					ID:          "msg-2",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "continue"}},
				},
			},
			verify: func(t *testing.T, result []message.Message) {
				require.Len(t, result, 3)

				// The branch summary should be converted to a synthetic user message.
				require.Equal(t, message.User, result[1].Role)
				summaryText := result[1].Parts[0].(message.TextContent).Text
				require.Contains(t, summaryText, "branch summary text")
				require.Contains(t, summaryText, "<summary>")
			},
		},
		{
			name: "multiple_compactions",
			input: []message.Message{
				{
					ID:          "msg-1",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "very old"}},
				},
				{
					ID:          "msg-compact-1",
					Role:        message.Assistant,
					MessageType: message.MessageTypeCompaction,
					Parts: []message.ContentPart{message.CompactionContent{
						Summary:          "old summary",
						FirstKeptEntryID: "msg-1",
						TokensBefore:     500,
					}},
				},
				{
					ID:          "msg-mid",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "middle"}},
				},
				{
					ID:          "msg-compact-2",
					Role:        message.Assistant,
					MessageType: message.MessageTypeCompaction,
					Parts: []message.ContentPart{message.CompactionContent{
						Summary:          "recent summary",
						FirstKeptEntryID: "msg-mid",
						TokensBefore:     800,
					}},
				},
				{
					ID:          "msg-last",
					Role:        message.User,
					MessageType: message.MessageTypeMessage,
					Parts:       []message.ContentPart{message.TextContent{Text: "latest"}},
				},
			},
			verify: func(t *testing.T, result []message.Message) {
				// Only the most recent compaction should be processed.
				require.Len(t, result, 3)

				// First is the synthetic summary from the recent compaction.
				summaryText := result[0].Parts[0].(message.TextContent).Text
				require.Contains(t, summaryText, "recent summary")
				require.NotContains(t, summaryText, "old summary")

				// Second is the kept message (msg-mid).
				require.Equal(t, "middle", result[1].Parts[0].(message.TextContent).Text)

				// Third is the post-compaction message.
				require.Equal(t, "latest", result[2].Parts[0].(message.TextContent).Text)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := message.FilterBranchPathForContext(tt.input)
			tt.verify(t, result)
		})
	}
}

func TestComputeFirstKeptEntryID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		msgs       []message.Message
		keepTokens int
		want       string
	}{
		{
			name:       "empty_input",
			msgs:       []message.Message{},
			keepTokens: 100,
			want:       "",
		},
		{
			name: "under_threshold",
			msgs: []message.Message{
				{
					ID:   "msg-1",
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "short"},
					},
				},
				{
					ID:   "msg-2",
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: "reply"},
					},
				},
			},
			keepTokens: 10000,
			want:       "msg-1",
		},
		{
			name: "over_threshold_finds_user_boundary",
			msgs: []message.Message{
				{
					ID:   "msg-1",
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "first user message"},
					},
				},
				{
					ID:   "msg-2",
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: "first response"},
					},
				},
				{
					ID:   "msg-3",
					Role: message.User,
					Parts: []message.ContentPart{
						// Large text to push over threshold.
						message.TextContent{Text: strings.Repeat("x", 200)},
					},
				},
				{
					ID:   "msg-4",
					Role: message.Assistant,
					Parts: []message.ContentPart{
						message.TextContent{Text: strings.Repeat("y", 200)},
					},
				},
			},
			// Threshold set so that the last two messages exceed it
			// but the scan should find msg-3 (user) as the boundary.
			keepTokens: 80,
			want:       "msg-3",
		},
		{
			name: "single_message",
			msgs: []message.Message{
				{
					ID:   "only",
					Role: message.User,
					Parts: []message.ContentPart{
						message.TextContent{Text: "hello"},
					},
				},
			},
			keepTokens: 100,
			want:       "only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := message.ComputeFirstKeptEntryID(tt.msgs, tt.keepTokens)
			require.Equal(t, tt.want, got)
		})
	}
}
