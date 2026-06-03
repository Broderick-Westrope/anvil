package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatConversationForTitle(t *testing.T) {
	t.Parallel()

	t.Run("normal messages", func(t *testing.T) {
		t.Parallel()
		msgs := []message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Hello"}}},
			{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "Hi there"}}},
		}
		result := formatConversationForTitle(msgs)
		assert.Equal(t, "user: Hello\nassistant: Hi there\n", result)
	})

	t.Run("all tool messages returns empty", func(t *testing.T) {
		t.Parallel()
		msgs := []message.Message{
			{Role: message.Tool, Parts: []message.ContentPart{message.TextContent{Text: "tool output"}}},
			{Role: message.Tool, Parts: []message.ContentPart{message.TextContent{Text: "more output"}}},
		}
		result := formatConversationForTitle(msgs)
		assert.Empty(t, result)
	})

	t.Run("exceeds maxTitleConversationChars", func(t *testing.T) {
		t.Parallel()
		longText := strings.Repeat("a", maxTitleConversationChars+500)
		msgs := []message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: longText}}},
		}
		result := formatConversationForTitle(msgs)
		require.LessOrEqual(t, len(result), maxTitleConversationChars)
		require.True(t, utf8.ValidString(result))
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		result := formatConversationForTitle(nil)
		assert.Empty(t, result)
	})

	t.Run("multi-byte truncation preserves valid UTF-8", func(t *testing.T) {
		t.Parallel()
		// Each '日' is 3 bytes. Build a string that, with the "user: " prefix
		// and trailing newline, exceeds maxTitleConversationChars.
		runeText := strings.Repeat("日", maxTitleConversationChars)
		msgs := []message.Message{
			{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: runeText}}},
		}
		result := formatConversationForTitle(msgs)
		require.LessOrEqual(t, len(result), maxTitleConversationChars)
		require.True(t, utf8.ValidString(result))
	})
}
