package agent

import (
	"os"
	"strings"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	anthropicoauth "github.com/charmbracelet/crush/internal/oauth/anthropic"
)

const (
	// AnthropicIdentityPrefix is the identity declaration injected as a
	// system message for Anthropic OAuth sessions.
	AnthropicIdentityPrefix = "You are Claude Code, Anthropic's official CLI for Claude."

	// SystemModeEnvVar is the environment variable that controls how the
	// system prompt is delivered when using Anthropic OAuth.
	SystemModeEnvVar = "CRUSH_ANTHROPIC_SYSTEM_MODE"

	// SystemModeA keeps the system prompt in the system[] array (default).
	SystemModeA = "system"

	// SystemModeB moves the system prompt into the first user message.
	SystemModeB = "user"
)

// isAnthropicOAuth returns true when providerCfg describes an Anthropic
// provider authenticated via OAuth.
func isAnthropicOAuth(providerCfg config.ProviderConfig) bool {
	return providerCfg.Type == catwalk.TypeAnthropic && providerCfg.OAuthToken != nil
}

// anthropicSystemMode returns the active system-prompt delivery mode,
// reading SystemModeEnvVar and defaulting to SystemModeA.
func anthropicSystemMode() string {
	if mode := os.Getenv(SystemModeEnvVar); mode != "" {
		return mode
	}
	return SystemModeA
}

// transformForAnthropicOAuth rewrites messages for Anthropic OAuth billing
// compliance. It prepends a billing header and identity prefix, using the
// mode selected by anthropicSystemMode.
func transformForAnthropicOAuth(
	messages []fantasy.Message,
	_ string,
	_ string,
) []fantasy.Message {
	switch anthropicSystemMode() {
	case SystemModeB:
		return transformModeB(messages)
	default:
		return transformModeA(messages)
	}
}

// transformModeA prepends billing and identity system messages before the
// existing messages. The billing header is computed from the original system
// text before any prepending to avoid a self-referential hash.
func transformModeA(messages []fantasy.Message) []fantasy.Message {
	systemText := extractSystemText(messages)

	// Compute billing header BEFORE prepending to avoid a self-referential
	// hash.
	billingHeader := anthropicoauth.BuildBillingHeader(systemText)

	result := make([]fantasy.Message, 0, len(messages)+2)
	result = append(result,
		fantasy.NewSystemMessage(billingHeader),
		fantasy.NewSystemMessage(AnthropicIdentityPrefix),
	)
	result = append(result, messages...)
	return result
}

// transformModeB removes system messages from the slice, prepends their
// text content to the first user message, and places billing + identity as
// the only system messages at the head.
func transformModeB(messages []fantasy.Message) []fantasy.Message {
	var systemTexts []string
	nonSystem := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == fantasy.MessageRoleSystem {
			for _, part := range msg.Content {
				if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && tp.Text != "" {
					systemTexts = append(systemTexts, tp.Text)
				}
			}
		} else {
			nonSystem = append(nonSystem, msg)
		}
	}

	systemText := strings.Join(systemTexts, "\n")

	// Prepend collected system text to the first user message.
	for i, msg := range nonSystem {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		newContent := make([]fantasy.MessagePart, 0, len(msg.Content)+1)
		if systemText != "" {
			newContent = append(newContent, fantasy.TextPart{Text: systemText})
		}
		newContent = append(newContent, msg.Content...)
		nonSystem[i].Content = newContent
		break
	}

	// Compute billing header from the first user message, which now contains
	// the former system content.
	var firstUserText strings.Builder
	for _, msg := range nonSystem {
		if msg.Role != fantasy.MessageRoleUser {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				firstUserText.WriteString(tp.Text)
			}
		}
		break
	}
	billingHeader := anthropicoauth.BuildBillingHeader(firstUserText.String())

	result := make([]fantasy.Message, 0, len(nonSystem)+2)
	result = append(result,
		fantasy.NewSystemMessage(billingHeader),
		fantasy.NewSystemMessage(AnthropicIdentityPrefix),
	)
	result = append(result, nonSystem...)
	return result
}

// extractSystemText joins the text content of all system-role messages with
// newlines.
func extractSystemText(messages []fantasy.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleSystem {
			continue
		}
		for _, part := range msg.Content {
			if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && tp.Text != "" {
				parts = append(parts, tp.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}
