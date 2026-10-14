package agent

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/openrouter"
	"github.com/Broderick-Westrope/anvil/internal/config"
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

func TestGenerateTitle(t *testing.T) {
	t.Parallel()
	conversation := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "Help me name this"}},
	}}
	usage := fantasy.Usage{InputTokens: 1_000_000, CacheCreationTokens: 1_000_000, CacheReadTokens: 1_000_000, OutputTokens: 1_000_000}
	tests := map[string]struct {
		canReason      bool
		fail           bool
		openrouterCost *float64
		flatRate       bool
		wantTitle      string
		wantMaxTokens  int64
		wantCost       float64
	}{
		"catwalk prices": {wantTitle: "Named", wantMaxTokens: 40, wantCost: 1 + 2 + 3 + 4},
		"reasoning model": {
			canReason: true, wantTitle: "Named", wantMaxTokens: 777, wantCost: 1 + 2 + 3 + 4,
		},
		"openrouter cost": {openrouterCost: new(0.5), wantTitle: "Named", wantMaxTokens: 40, wantCost: 0.5},
		"flat rate":       {openrouterCost: new(0.5), flatRate: true, wantTitle: "Named", wantMaxTokens: 40},
		"every model fails": {
			fail: true, wantTitle: DefaultSessionName, wantMaxTokens: 40,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := testEnv(t)
			model := &recordingModel{respond: func(int) ([]fantasy.StreamPart, error) {
				if tt.fail {
					return nil, errors.New("no title for you")
				}
				parts := textReply("Named\n", fantasy.FinishReasonStop, usage)
				if tt.openrouterCost != nil {
					parts[len(parts)-1].ProviderMetadata = fantasy.ProviderMetadata{
						openrouter.Name: &openrouter.ProviderMetadata{Usage: openrouter.UsageAccounting{Cost: *tt.openrouterCost}},
					}
				}
				return parts, nil
			}}
			m := Model{
				Model: model,
				CatwalkCfg: catwalk.Model{
					ContextWindow: 200000, DefaultMaxTokens: 777, CanReason: tt.canReason,
					CostPer1MInCached: 1, CostPer1MOutCached: 2, CostPer1MIn: 3, CostPer1MOut: 4,
				},
				FlatRate: tt.flatRate,
			}
			a := sideCallAgent(env, m, m, "", config.ProviderConfig{})
			sess := newTestSession(t, env)

			a.generateTitle(t.Context(), sess.ID, conversation)

			got, err := env.sessions.Get(t.Context(), sess.ID)
			require.NoError(t, err)
			require.Equal(t, tt.wantTitle, got.Title)
			recorded := model.recorded()
			require.NotEmpty(t, recorded)
			require.NotNil(t, recorded[0].MaxOutputTokens)
			require.Equal(t, tt.wantMaxTokens, *recorded[0].MaxOutputTokens)
			if tt.fail {
				require.Len(t, recorded, 2, "the large model is tried after the small one")
				return
			}
			require.InDelta(t, tt.wantCost, got.Cost, 1e-9)
			require.Equal(t, usage.InputTokens+usage.CacheCreationTokens, got.PromptTokens)
			require.Equal(t, usage.OutputTokens, got.CompletionTokens)
		})
	}
}
