package config

import "strings"

// MigrateAllowedTools converts the deprecated allowed_tools list into
// equivalent permission rules. Each tool name becomes a rule with action
// "allow". Tool:action entries (e.g. "bash:execute") become tool-level
// allow rules (the action qualifier is dropped since the new system
// matches on input, not action).
func MigrateAllowedTools(allowedTools []string) []PermissionRule {
	if len(allowedTools) == 0 {
		return nil
	}

	rules := make([]PermissionRule, 0, len(allowedTools))
	for _, entry := range allowedTools {
		toolPattern := entry
		if idx := strings.IndexByte(entry, ':'); idx >= 0 {
			toolPattern = entry[:idx]
		}
		rules = append(rules, PermissionRule{
			ToolPattern: toolPattern,
			Action:      PermissionAllow,
		})
	}
	return rules
}
