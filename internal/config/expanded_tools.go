package config

import "path/filepath"

// IsToolExpanded reports whether toolName matches any of the given
// glob patterns.
func IsToolExpanded(patterns []string, toolName string) bool {
	for _, p := range patterns {
		if matched, err := filepath.Match(p, toolName); err == nil && matched {
			return true
		}
	}
	return false
}
