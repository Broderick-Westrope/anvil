package anthropic

import (
	"strings"
)

// Headers returns the base OAuth header map for Anthropic API requests.
// The Authorization header (Bearer token) is handled separately by the
// provider configuration layer.
func Headers(modelID string) map[string]string {
	return map[string]string{
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    strings.Join(BetasForModel(modelID), ","),
		"user-agent":        "claude-cli/" + CLIVersion + " (external, cli)",
		"x-app":             "cli",
	}
}
