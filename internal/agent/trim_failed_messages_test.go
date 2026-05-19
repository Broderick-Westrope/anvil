package agent

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestTrimFailedAttemptMessages(t *testing.T) {
	t.Parallel()

	userMsg := func(text string) message.Message {
		return message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		}
	}
	assistantMsg := func(text string) message.Message {
		return message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		}
	}
	emptyAssistant := func(id string) message.Message {
		return message.Message{
			ID:    id,
			Role:  message.Assistant,
			Parts: []message.ContentPart{},
		}
	}
	errorFinishAssistant := func(id string) message.Message {
		return message.Message{
			ID:   id,
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.Finish{Reason: message.FinishReasonError, Message: "Unauthorized"},
			},
		}
	}

	tests := []struct {
		name    string
		msgs    []message.Message
		want    []message.Message
		wantIDs []string
	}{
		{
			name: "trims trailing user message",
			msgs: []message.Message{
				assistantMsg("hello"),
				userMsg("hi"),
			},
			want: []message.Message{
				assistantMsg("hello"),
			},
		},
		{
			name: "trims trailing empty assistant then user message",
			msgs: []message.Message{
				assistantMsg("hello"),
				userMsg("hi"),
				emptyAssistant("ast-1"),
			},
			want: []message.Message{
				assistantMsg("hello"),
			},
			wantIDs: []string{"ast-1"},
		},
		{
			name: "trims trailing error-finish assistant then user message",
			msgs: []message.Message{
				assistantMsg("hello"),
				userMsg("hi"),
				errorFinishAssistant("ast-2"),
			},
			want: []message.Message{
				assistantMsg("hello"),
			},
			wantIDs: []string{"ast-2"},
		},
		{
			name: "stops at non-empty assistant",
			msgs: []message.Message{
				userMsg("first"),
				assistantMsg("response"),
			},
			want: []message.Message{
				userMsg("first"),
				assistantMsg("response"),
			},
		},
		{
			name: "empty input",
			msgs: nil,
			want: nil,
		},
		{
			name: "only user message",
			msgs: []message.Message{
				userMsg("hi"),
			},
			want: []message.Message{},
		},
		{
			name: "preserves earlier conversation",
			msgs: []message.Message{
				userMsg("first"),
				assistantMsg("response"),
				userMsg("second"),
				emptyAssistant("ast-3"),
			},
			want: []message.Message{
				userMsg("first"),
				assistantMsg("response"),
			},
			wantIDs: []string{"ast-3"},
		},
		{
			name: "empty assistant only with no preceding user message",
			msgs: []message.Message{
				emptyAssistant("ast-4"),
			},
			want:    []message.Message{},
			wantIDs: []string{"ast-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, gotIDs := trimFailedAttemptMessages(tt.msgs)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantIDs, gotIDs)
		})
	}
}
