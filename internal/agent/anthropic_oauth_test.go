package agent

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/oauth"
	anthropicoauth "github.com/charmbracelet/crush/internal/oauth/anthropic"
	"github.com/stretchr/testify/require"
)

// anthropicProviderCfg returns a ProviderConfig with Anthropic type and an
// optional OAuth token.
func anthropicProviderCfg(withOAuth bool) config.ProviderConfig {
	cfg := config.ProviderConfig{
		ID:   "anthropic",
		Type: catwalk.TypeAnthropic,
	}
	if withOAuth {
		cfg.OAuthToken = &oauth.Token{AccessToken: "test-token"}
	}
	return cfg
}

func TestIsAnthropicOAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      config.ProviderConfig
		expected bool
	}{
		{
			name:     "anthropic with OAuth token",
			cfg:      anthropicProviderCfg(true),
			expected: true,
		},
		{
			name:     "anthropic without OAuth token",
			cfg:      anthropicProviderCfg(false),
			expected: false,
		},
		{
			name: "openai with OAuth token",
			cfg: config.ProviderConfig{
				Type:       catwalk.TypeOpenAI,
				OAuthToken: &oauth.Token{AccessToken: "token"},
			},
			expected: false,
		},
		{
			name:     "empty provider config",
			cfg:      config.ProviderConfig{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, isAnthropicOAuth(tt.cfg))
		})
	}
}

func TestAnthropicSystemMode(t *testing.T) {
	// Note: subtests use t.Setenv, which is incompatible with t.Parallel.

	t.Run("default is system mode", func(t *testing.T) {
		t.Setenv(SystemModeEnvVar, "")
		require.Equal(t, SystemModeA, anthropicSystemMode())
	})

	t.Run("env var overrides to user mode", func(t *testing.T) {
		t.Setenv(SystemModeEnvVar, SystemModeB)
		require.Equal(t, SystemModeB, anthropicSystemMode())
	})

	t.Run("env var overrides to system mode explicitly", func(t *testing.T) {
		t.Setenv(SystemModeEnvVar, SystemModeA)
		require.Equal(t, SystemModeA, anthropicSystemMode())
	})
}

func systemMsg(text string) fantasy.Message {
	return fantasy.NewSystemMessage(text)
}

func userMsg(text string) fantasy.Message {
	return fantasy.NewUserMessage(text)
}

func TestTransformModeA(t *testing.T) {
	// Note: t.Setenv is incompatible with t.Parallel.
	t.Setenv(SystemModeEnvVar, SystemModeA)

	systemText := "Be helpful."
	messages := []fantasy.Message{
		systemMsg(systemText),
		userMsg("Hello"),
	}

	result := transformForAnthropicOAuth(messages)

	// Expect: billing header, identity prefix, then original messages.
	require.Len(t, result, 4)

	// First message: billing header (system role).
	require.Equal(t, fantasy.MessageRoleSystem, result[0].Role)
	require.Len(t, result[0].Content, 1)
	tp0, ok := fantasy.AsMessagePart[fantasy.TextPart](result[0].Content[0])
	require.True(t, ok)
	expectedBilling := anthropicoauth.BuildBillingValue(systemText)
	require.Equal(t, expectedBilling, tp0.Text)

	// Second message: identity prefix (system role).
	require.Equal(t, fantasy.MessageRoleSystem, result[1].Role)
	require.Len(t, result[1].Content, 1)
	tp1, ok := fantasy.AsMessagePart[fantasy.TextPart](result[1].Content[0])
	require.True(t, ok)
	require.Equal(t, AnthropicIdentityPrefix, tp1.Text)

	// Remaining messages are the originals unchanged.
	require.Equal(t, fantasy.MessageRoleSystem, result[2].Role)
	require.Equal(t, fantasy.MessageRoleUser, result[3].Role)
}

func TestTransformModeA_BillingHeader(t *testing.T) {
	// Note: t.Setenv is incompatible with t.Parallel.
	t.Setenv(SystemModeEnvVar, SystemModeA)

	systemContent := "You are a coding assistant."
	messages := []fantasy.Message{systemMsg(systemContent)}

	result := transformForAnthropicOAuth(messages)

	require.NotEmpty(t, result)
	require.Equal(t, fantasy.MessageRoleSystem, result[0].Role)
	tp, ok := fantasy.AsMessagePart[fantasy.TextPart](result[0].Content[0])
	require.True(t, ok)

	// The billing header must be computed from the original system text.
	expectedCCH := anthropicoauth.ComputeCCH(systemContent)
	require.Contains(t, tp.Text, expectedCCH,
		"billing header should contain the CCH of the original system text",
	)
}

func TestTransformModeB(t *testing.T) {
	// Note: t.Setenv is incompatible with t.Parallel.
	t.Setenv(SystemModeEnvVar, SystemModeB)

	systemContent := "Be helpful."
	userContent := "What is Go?"
	messages := []fantasy.Message{
		systemMsg(systemContent),
		userMsg(userContent),
	}

	result := transformForAnthropicOAuth(messages)

	// Expect: billing header, identity prefix, then user message (no system).
	require.Len(t, result, 3)

	require.Equal(t, fantasy.MessageRoleSystem, result[0].Role)
	require.Equal(t, fantasy.MessageRoleSystem, result[1].Role)
	require.Equal(t, fantasy.MessageRoleUser, result[2].Role)

	// Identity prefix is second system message.
	tp1, ok := fantasy.AsMessagePart[fantasy.TextPart](result[1].Content[0])
	require.True(t, ok)
	require.Equal(t, AnthropicIdentityPrefix, tp1.Text)

	// User message content should start with system text prepended.
	userParts := result[2].Content
	require.NotEmpty(t, userParts)
	firstPart, ok := fantasy.AsMessagePart[fantasy.TextPart](userParts[0])
	require.True(t, ok)
	require.Equal(t, systemContent, firstPart.Text)

	// The original user content follows.
	require.Len(t, userParts, 2)
	secondPart, ok := fantasy.AsMessagePart[fantasy.TextPart](userParts[1])
	require.True(t, ok)
	require.Equal(t, userContent, secondPart.Text)
}

func TestTransformNoOAuth(t *testing.T) {
	t.Parallel()

	cfg := anthropicProviderCfg(false) // no OAuth
	messages := []fantasy.Message{
		systemMsg("You are helpful."),
		userMsg("Hello"),
	}
	original := make([]fantasy.Message, len(messages))
	copy(original, messages)

	require.False(t, isAnthropicOAuth(cfg),
		"should not be Anthropic OAuth without token",
	)

	// When isAnthropicOAuth returns false the caller skips the transform.
	// Verify the messages are still the same length/content as a sanity check.
	require.Equal(t, original, messages)
}
