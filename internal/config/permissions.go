package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/Broderick-Westrope/anvil/internal/permission/match"
	"github.com/invopop/jsonschema"
	orderedmap "github.com/pb33f/ordered-map/v2"
)

// PermissionAction represents what happens when a permission rule matches.
type PermissionAction string

const (
	// PermissionAllow allows the tool invocation without prompting.
	PermissionAllow PermissionAction = "allow"
	// PermissionAsk prompts the user before executing the tool.
	PermissionAsk PermissionAction = "ask"
	// PermissionDeny blocks the tool invocation entirely.
	PermissionDeny PermissionAction = "deny"
)

// validActions is the set of recognised permission actions.
var validActions = map[PermissionAction]struct{}{
	PermissionAllow: {},
	PermissionAsk:   {},
	PermissionDeny:  {},
}

// validateAction returns an error if a is not a recognised action.
func validateAction(a PermissionAction) error {
	if _, ok := validActions[a]; !ok {
		return fmt.Errorf("unknown permission action %q (valid: allow, ask, deny)", a)
	}
	return nil
}

// PermissionRule is a single rule in the ordered permission list.
// Either Action is set (string value, applies to all inputs) or
// SubRules is set (object value, per-input pattern matching).
type PermissionRule struct {
	ToolPattern string              // Glob pattern matching tool names.
	Action      PermissionAction    // Set when value is a string.
	SubRules    []PermissionSubRule // Set when value is an object.
}

// PermissionSubRule matches against tool input (command, path, URL, etc.).
type PermissionSubRule struct {
	InputPattern string           // Glob pattern matching tool input.
	Action       PermissionAction // Action to take when the pattern matches.
}

// Permissions holds the ordered list of permission rules parsed from config.
type Permissions struct {
	// AllowedTools is the deprecated flat list of tool names.
	// Kept for backward-compatible JSON parsing; migrated at load time.
	AllowedTools []string         `json:"allowed_tools,omitempty"`
	Rules        []PermissionRule `json:"-"`
}

// JSONSchema describes the permissions object for schema generation.
// The rules format uses arbitrary tool-name globs as keys, which the
// reflector cannot derive from the struct, so it is spelled out here.
func (Permissions) JSONSchema() *jsonschema.Schema {
	action := &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"allow", "ask", "deny"},
		Description: "Action to take: allow silently, ask the user, or deny outright",
	}

	subRules := &jsonschema.Schema{
		Type:                 "object",
		Description:          "Per-input rules keyed by a glob matching the tool input (bash command, file path, URL). Evaluated in order; last match wins.",
		AdditionalProperties: action,
	}

	ruleValue := &jsonschema.Schema{
		OneOf:       []*jsonschema.Schema{action, subRules},
		Description: "Either an action for every input, or an object of per-input glob rules",
	}

	return &jsonschema.Schema{
		Type: "object",
		Description: "Permission rules keyed by a glob matching tool names " +
			"(e.g. \"bash\", \"mcp_linear_*\", \"{edit,write}\"). Rules are " +
			"evaluated in order and the last match wins. The deprecated " +
			"allowed_tools list is still accepted but cannot be combined " +
			"with rules.",
		Properties: func() *orderedmap.OrderedMap[string, *jsonschema.Schema] {
			props := orderedmap.New[string, *jsonschema.Schema]()
			props.Set("allowed_tools", &jsonschema.Schema{
				Type:        "array",
				Items:       &jsonschema.Schema{Type: "string"},
				Description: "Deprecated: list of tools that don't require permission prompts. Use permission rules instead.",
			})
			return props
		}(),
		AdditionalProperties: ruleValue,
	}
}

// UnmarshalJSON implements [json.Unmarshaler]. It handles two formats:
//   - Legacy: {"allowed_tools": ["bash", "view"]}
//   - New ordered rules: {"bash": "allow", "edit": {"*.go": "allow", "*": "ask"}}
//
// Key ordering in the new format is preserved in the Rules slice.
func (p *Permissions) UnmarshalJSON(data []byte) error {
	// Reset fields.
	p.AllowedTools = nil
	p.Rules = nil

	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}

	// Attempt legacy format first.
	var legacy struct {
		AllowedTools []string `json:"allowed_tools"`
	}
	if err := json.Unmarshal(data, &legacy); err == nil && legacy.AllowedTools != nil {
		// Check for non-legacy keys that indicate a mixed format.
		var allKeys map[string]json.RawMessage
		if err := json.Unmarshal(data, &allKeys); err == nil {
			for k := range allKeys {
				if k != "allowed_tools" {
					return fmt.Errorf("permissions: cannot mix 'allowed_tools' with new permission rules; migrate to the new format")
				}
			}
		}
		p.AllowedTools = legacy.AllowedTools
		return nil
	}

	// New ordered-key format. Use token-based decoding to preserve order.
	dec := json.NewDecoder(bytes.NewReader(data))

	// Expect opening '{'.
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("permissions: expected '{', got %v", tok)
	}

	for dec.More() {
		// Read tool pattern key.
		keyTok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("permissions: reading key: %w", err)
		}
		toolPattern, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("permissions: expected string key, got %T", keyTok)
		}

		if err := match.Validate(toolPattern); err != nil {
			return fmt.Errorf("permissions: invalid tool pattern %q: %w", toolPattern, err)
		}

		rule, err := decodePermissionValue(dec, toolPattern)
		if err != nil {
			return err
		}
		p.Rules = append(p.Rules, rule)
	}

	// Consume closing '}'.
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}

	return nil
}

// decodePermissionValue reads the value for a tool pattern key and returns
// a PermissionRule. The value is either a string action or a nested object
// of sub-rules.
func decodePermissionValue(dec *json.Decoder, toolPattern string) (PermissionRule, error) {
	// Peek at the next token to determine the value type.
	tok, err := dec.Token()
	if err != nil {
		return PermissionRule{}, fmt.Errorf("permissions: reading value for %q: %w", toolPattern, err)
	}

	switch v := tok.(type) {
	case string:
		// Simple string action.
		action := PermissionAction(v)
		if err := validateAction(action); err != nil {
			return PermissionRule{}, fmt.Errorf("permissions: tool %q: %w", toolPattern, err)
		}
		return PermissionRule{
			ToolPattern: toolPattern,
			Action:      action,
		}, nil

	case json.Delim:
		if v != '{' {
			return PermissionRule{}, fmt.Errorf("permissions: tool %q: expected string or '{', got %v", toolPattern, v)
		}
		// Object value — parse sub-rules in order.
		var subRules []PermissionSubRule
		for dec.More() {
			subKeyTok, err := dec.Token()
			if err != nil {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: reading sub-rule key: %w", toolPattern, err)
			}
			inputPattern, ok := subKeyTok.(string)
			if !ok {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: expected string sub-rule key, got %T", toolPattern, subKeyTok)
			}

			if err := match.Validate(inputPattern); err != nil {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: invalid input pattern %q: %w", toolPattern, inputPattern, err)
			}

			subValTok, err := dec.Token()
			if err != nil {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: reading sub-rule value for %q: %w", toolPattern, inputPattern, err)
			}
			actionStr, ok := subValTok.(string)
			if !ok {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: sub-rule %q: expected string action, got %T", toolPattern, inputPattern, subValTok)
			}
			action := PermissionAction(actionStr)
			if err := validateAction(action); err != nil {
				return PermissionRule{}, fmt.Errorf("permissions: tool %q: sub-rule %q: %w", toolPattern, inputPattern, err)
			}

			subRules = append(subRules, PermissionSubRule{
				InputPattern: inputPattern,
				Action:       action,
			})
		}
		// Consume closing '}'.
		if _, err := dec.Token(); err != nil {
			return PermissionRule{}, fmt.Errorf("permissions: tool %q: %w", toolPattern, err)
		}
		return PermissionRule{
			ToolPattern: toolPattern,
			SubRules:    subRules,
		}, nil

	default:
		return PermissionRule{}, fmt.Errorf("permissions: tool %q: expected string or object, got %T", toolPattern, tok)
	}
}

// MarshalJSON implements [json.Marshaler]. It writes the legacy format when
// AllowedTools is populated and Rules is empty, otherwise writes the
// ordered rule format.
func (p Permissions) MarshalJSON() ([]byte, error) {
	if len(p.AllowedTools) > 0 && len(p.Rules) == 0 {
		return json.Marshal(struct {
			AllowedTools []string `json:"allowed_tools,omitempty"`
		}{
			AllowedTools: p.AllowedTools,
		})
	}

	var buf bytes.Buffer
	buf.WriteByte('{')

	for i, rule := range p.Rules {
		if i > 0 {
			buf.WriteByte(',')
		}

		// Write the key.
		keyBytes, err := json.Marshal(rule.ToolPattern)
		if err != nil {
			return nil, fmt.Errorf("permissions: marshaling key %q: %w", rule.ToolPattern, err)
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')

		if len(rule.SubRules) > 0 {
			// Write nested object.
			buf.WriteByte('{')
			for j, sub := range rule.SubRules {
				if j > 0 {
					buf.WriteByte(',')
				}
				subKey, err := json.Marshal(sub.InputPattern)
				if err != nil {
					return nil, fmt.Errorf("permissions: marshaling sub-key %q: %w", sub.InputPattern, err)
				}
				buf.Write(subKey)
				buf.WriteByte(':')
				subVal, err := json.Marshal(string(sub.Action))
				if err != nil {
					return nil, fmt.Errorf("permissions: marshaling sub-value: %w", err)
				}
				buf.Write(subVal)
			}
			buf.WriteByte('}')
		} else {
			// Write string action.
			valBytes, err := json.Marshal(string(rule.Action))
			if err != nil {
				return nil, fmt.Errorf("permissions: marshaling value: %w", err)
			}
			buf.Write(valBytes)
		}
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}
