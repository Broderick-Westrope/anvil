package anthropic

import (
	"strings"
)

// defaultBetas is the set of Anthropic beta flags sent with every OAuth
// request.
var defaultBetas = []string{
	"claude-code-20250219",
	"oauth-2025-04-20",
	"interleaved-thinking-2025-05-14",
	"prompt-caching-scope-2026-01-05",
	"context-management-2025-06-27",
	"advisor-tool-2026-03-01",
	"cache-diagnosis-2026-04-07",
	"extended-cache-ttl-2025-04-11",
}

// BetasForModel returns the beta flags appropriate for the given model ID.
// Haiku models exclude the interleaved-thinking beta. Models in the 4-6 or
// 4-7 family additionally include the effort beta. A fresh slice is
// returned on every call to prevent callers from mutating the defaults.
func BetasForModel(modelID string) []string {
	result := make([]string, 0, len(defaultBetas)+1)
	for _, b := range defaultBetas {
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
