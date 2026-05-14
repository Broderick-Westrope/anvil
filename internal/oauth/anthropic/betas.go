package anthropic

import (
	"strings"
)

// DefaultBetas is the set of Anthropic beta flags sent with every OAuth
// request.
var DefaultBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"prompt-caching-scope-2026-01-05",
}

// BetasForModel returns the beta flags appropriate for the given model ID.
// Haiku models exclude the interleaved-thinking beta. Models in the 4-6 or
// 4-7 family additionally include the effort beta.
func BetasForModel(modelID string) []string {
	result := make([]string, 0, len(DefaultBetas)+1)
	for _, b := range DefaultBetas {
		if strings.Contains(modelID, "haiku") && b == "interleaved-thinking-2025-05-14" {
			continue
		}
		result = append(result, b)
	}
	if strings.Contains(modelID, "4-6") || strings.Contains(modelID, "4-7") {
		result = append(result, "effort-2025-11-24")
	}
	return result
}

// MergeBetas merges an existing comma-separated beta string with modelBetas,
// deduplicates, and returns a comma-joined result.
func MergeBetas(existing string, modelBetas []string) string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(modelBetas)+4)

	if existing != "" {
		for _, b := range strings.Split(existing, ",") {
			b = strings.TrimSpace(b)
			if b != "" && !seen[b] {
				seen[b] = true
				result = append(result, b)
			}
		}
	}

	for _, b := range modelBetas {
		if b != "" && !seen[b] {
			seen[b] = true
			result = append(result, b)
		}
	}

	return strings.Join(result, ",")
}
