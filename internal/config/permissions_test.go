package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissions_UnmarshalJSON_LegacyFormat(t *testing.T) {
	t.Parallel()

	input := `{"allowed_tools": ["bash", "view", "edit"]}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Equal(t, []string{"bash", "view", "edit"}, p.AllowedTools)
	require.Empty(t, p.Rules)
}

func TestPermissions_UnmarshalJSON_StringActions(t *testing.T) {
	t.Parallel()

	input := `{"bash": "allow", "edit": "ask", "rm": "deny"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Empty(t, p.AllowedTools)
	require.Len(t, p.Rules, 3)

	require.Equal(t, "bash", p.Rules[0].ToolPattern)
	require.Equal(t, PermissionAllow, p.Rules[0].Action)

	require.Equal(t, "edit", p.Rules[1].ToolPattern)
	require.Equal(t, PermissionAsk, p.Rules[1].Action)

	require.Equal(t, "rm", p.Rules[2].ToolPattern)
	require.Equal(t, PermissionDeny, p.Rules[2].Action)
}

func TestPermissions_UnmarshalJSON_ObjectSubRules(t *testing.T) {
	t.Parallel()

	input := `{"bash": {"*.sh": "allow", "*": "ask"}}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Len(t, p.Rules, 1)
	require.Equal(t, "bash", p.Rules[0].ToolPattern)
	require.Empty(t, p.Rules[0].Action)
	require.Len(t, p.Rules[0].SubRules, 2)

	require.Equal(t, "*.sh", p.Rules[0].SubRules[0].InputPattern)
	require.Equal(t, PermissionAllow, p.Rules[0].SubRules[0].Action)

	require.Equal(t, "*", p.Rules[0].SubRules[1].InputPattern)
	require.Equal(t, PermissionAsk, p.Rules[0].SubRules[1].Action)
}

func TestPermissions_UnmarshalJSON_MixedStringAndObject(t *testing.T) {
	t.Parallel()

	input := `{"view": "allow", "bash": {"*.sh": "allow", "*": "deny"}, "edit": "ask"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Len(t, p.Rules, 3)

	require.Equal(t, "view", p.Rules[0].ToolPattern)
	require.Equal(t, PermissionAllow, p.Rules[0].Action)
	require.Empty(t, p.Rules[0].SubRules)

	require.Equal(t, "bash", p.Rules[1].ToolPattern)
	require.Empty(t, p.Rules[1].Action)
	require.Len(t, p.Rules[1].SubRules, 2)

	require.Equal(t, "edit", p.Rules[2].ToolPattern)
	require.Equal(t, PermissionAsk, p.Rules[2].Action)
}

func TestPermissions_UnmarshalJSON_OrderPreserved(t *testing.T) {
	t.Parallel()

	// Use enough keys to make ordering non-trivial.
	input := `{"zeta": "allow", "alpha": "deny", "mu": "ask", "beta": "allow"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Len(t, p.Rules, 4)
	require.Equal(t, "zeta", p.Rules[0].ToolPattern)
	require.Equal(t, "alpha", p.Rules[1].ToolPattern)
	require.Equal(t, "mu", p.Rules[2].ToolPattern)
	require.Equal(t, "beta", p.Rules[3].ToolPattern)
}

func TestPermissions_UnmarshalJSON_InvalidAction(t *testing.T) {
	t.Parallel()

	input := `{"bash": "reject"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown permission action")
}

func TestPermissions_UnmarshalJSON_InvalidSubRuleAction(t *testing.T) {
	t.Parallel()

	input := `{"bash": {"*.sh": "nope"}}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown permission action")
}

func TestPermissions_UnmarshalJSON_InvalidGlobPattern(t *testing.T) {
	t.Parallel()

	input := `{"ba{sh": "allow"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid tool pattern")
}

func TestPermissions_UnmarshalJSON_InvalidSubRuleGlobPattern(t *testing.T) {
	t.Parallel()

	input := `{"bash": {"[invalid": "allow"}}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid input pattern")
}

func TestPermissions_UnmarshalJSON_NullAndEmpty(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"null", "{}", ""} {
		var p Permissions
		if input == "" {
			// Empty byte slice should not panic.
			err := p.UnmarshalJSON([]byte(input))
			require.NoError(t, err)
		} else {
			err := json.Unmarshal([]byte(input), &p)
			require.NoError(t, err)
		}
		require.Empty(t, p.AllowedTools)
		require.Empty(t, p.Rules)
	}
}

func TestPermissions_MarshalJSON_Legacy(t *testing.T) {
	t.Parallel()

	p := Permissions{
		AllowedTools: []string{"bash", "view"},
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"allowed_tools": ["bash", "view"]}`, string(data))
}

func TestPermissions_MarshalJSON_Rules(t *testing.T) {
	t.Parallel()

	p := Permissions{
		Rules: []PermissionRule{
			{ToolPattern: "bash", Action: PermissionAllow},
			{ToolPattern: "edit", SubRules: []PermissionSubRule{
				{InputPattern: "*.go", Action: PermissionAllow},
				{InputPattern: "*", Action: PermissionAsk},
			}},
		},
	}
	data, err := json.Marshal(p)
	require.NoError(t, err)

	// Verify it's valid JSON.
	require.True(t, json.Valid(data))

	// Verify content by unmarshaling back.
	var got Permissions
	err = json.Unmarshal(data, &got)
	require.NoError(t, err)
	require.Equal(t, p.Rules, got.Rules)
}

func TestPermissions_MarshalJSON_EmptyRules(t *testing.T) {
	t.Parallel()

	p := Permissions{}
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.Equal(t, `{}`, string(data))
}

func TestPermissions_RoundTrip(t *testing.T) {
	t.Parallel()

	original := `{"view":"allow","bash":{"*.sh":"allow","*":"deny"},"edit":"ask"}`
	var p Permissions
	err := json.Unmarshal([]byte(original), &p)
	require.NoError(t, err)

	data, err := json.Marshal(p)
	require.NoError(t, err)

	// Marshal back again to verify stability.
	var p2 Permissions
	err = json.Unmarshal(data, &p2)
	require.NoError(t, err)

	data2, err := json.Marshal(p2)
	require.NoError(t, err)

	require.Equal(t, string(data), string(data2))
}

func TestPermissions_RoundTrip_Legacy(t *testing.T) {
	t.Parallel()

	original := `{"allowed_tools":["bash","view"]}`
	var p Permissions
	err := json.Unmarshal([]byte(original), &p)
	require.NoError(t, err)

	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, original, string(data))
}

func TestPermissions_UnmarshalJSON_GlobPatterns(t *testing.T) {
	t.Parallel()

	// Brace expansion and wildcards should be accepted.
	input := `{"{bash,edit}": "allow", "mcp__*": "deny"}`
	var p Permissions
	err := json.Unmarshal([]byte(input), &p)
	require.NoError(t, err)
	require.Len(t, p.Rules, 2)
	require.Equal(t, "{bash,edit}", p.Rules[0].ToolPattern)
	require.Equal(t, "mcp__*", p.Rules[1].ToolPattern)
}
