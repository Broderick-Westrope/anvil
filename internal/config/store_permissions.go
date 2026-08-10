package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
)

// SetPermissionRule adds or updates a permission rule in the config file.
// Uses read→unmarshal→modify→marshal→write to avoid sjson dot-path issues.
// Serialized via s.mu to prevent concurrent write corruption.
//
// Unlike other config writers, ScopeGlobal targets the user-maintained
// config (~/.config/anvil/anvil.json) rather than the app data file, so
// permission rules stay in one version-controllable place.
func (s *ConfigStore) SetPermissionRule(scope Scope, toolPattern string, inputPattern string, action PermissionAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate patterns before writing. UnmarshalJSON validates on
	// load, so persisting an invalid pattern would break the next
	// startup.
	if err := match.Validate(toolPattern); err != nil {
		return fmt.Errorf("invalid tool pattern %q: %w", toolPattern, err)
	}
	if inputPattern != "" {
		if err := match.Validate(inputPattern); err != nil {
			return fmt.Errorf("invalid input pattern %q: %w", inputPattern, err)
		}
	}

	path, err := s.permissionConfigPath(scope)
	if err != nil {
		return fmt.Errorf("permission rule: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			data = []byte("{}")
		} else {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if cfg.Permissions == nil {
		cfg.Permissions = &Permissions{}
	}

	upsertPermissionRule(cfg.Permissions, toolPattern, inputPattern, action)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Rename-into-place so a concurrent reader (including our own
	// autoReload below) never observes a partially-written file.
	if err := atomicWriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	// Auto-reload to keep in-memory state fresh after config edits.
	// On failure the store's Config() is stale until the next reload;
	// the permission service stays consistent regardless because it
	// upserts its own rule snapshot after this call returns.
	if err := s.autoReload(context.Background()); err != nil {
		slog.Warn("Config file updated but failed to reload in-memory state", "error", err)
	}

	return nil
}

// permissionConfigPath returns the file path permission rules are
// written to for the given scope. ScopeGlobal prefers the
// user-maintained config file; it falls back to the data file when the
// user config path is unset (e.g. in tests).
func (s *ConfigStore) permissionConfigPath(scope Scope) (string, error) {
	if scope == ScopeGlobal && s.globalConfigPath != "" {
		return s.globalConfigPath, nil
	}
	return s.configPath(scope)
}

// upsertPermissionRule inserts or updates a permission rule in the
// Permissions struct.
func upsertPermissionRule(perms *Permissions, toolPattern, inputPattern string, action PermissionAction) {
	perms.Rules = UpsertPermissionRule(perms.Rules, toolPattern, inputPattern, action)
}

// UpsertPermissionRule inserts or updates a permission rule in the
// given slice, returning the updated slice. It handles the following
// cases:
//   - New tool pattern: append a new rule.
//   - Existing tool pattern with no inputPattern: update the action.
//   - Existing tool pattern with inputPattern: add/update sub-rule.
//   - Existing tool pattern has a string action but inputPattern is set:
//     convert to sub-rules.
func UpsertPermissionRule(rules []PermissionRule, toolPattern, inputPattern string, action PermissionAction) []PermissionRule {
	// Find existing rule with matching toolPattern.
	for i, rule := range rules {
		if rule.ToolPattern != toolPattern {
			continue
		}

		if inputPattern == "" {
			// Tool-level rule: replace everything with a simple action.
			rules[i] = PermissionRule{
				ToolPattern: toolPattern,
				Action:      action,
			}
			return rules
		}

		// Input-level rule requested.
		if len(rule.SubRules) > 0 {
			// Already has sub-rules: find and update, or append.
			for j, sub := range rule.SubRules {
				if sub.InputPattern == inputPattern {
					rules[i].SubRules[j].Action = action
					return rules
				}
			}
			rules[i].SubRules = append(rules[i].SubRules, PermissionSubRule{
				InputPattern: inputPattern,
				Action:       action,
			})
			return rules
		}

		// Currently a string action but caller wants a sub-rule;
		// preserve the existing action as a catch-all "*" sub-rule.
		rules[i] = PermissionRule{
			ToolPattern: toolPattern,
			SubRules: []PermissionSubRule{
				{InputPattern: "*", Action: rules[i].Action},
				{InputPattern: inputPattern, Action: action},
			},
		}
		return rules
	}

	// No existing rule found: append new one.
	if inputPattern == "" {
		return append(rules, PermissionRule{
			ToolPattern: toolPattern,
			Action:      action,
		})
	}
	return append(rules, PermissionRule{
		ToolPattern: toolPattern,
		SubRules: []PermissionSubRule{
			{InputPattern: inputPattern, Action: action},
		},
	})
}
