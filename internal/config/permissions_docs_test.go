package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDocumentedPermissionExamples parses the permission examples shown in
// README.md and the anvil-config skill so documentation cannot drift away
// from what the parser accepts.
func TestDocumentedPermissionExamples(t *testing.T) {
	t.Parallel()

	// The README example.
	readme := `{
		"view": "allow",
		"ls": "allow",
		"grep": "allow",
		"{edit,write}": "allow",
		"mcp_context7_*": "allow",
		"bash": {
			"*": "ask",
			"git status *": "allow",
			"git diff *": "allow",
			"go test *": "allow",
			"rm *": "deny"
		}
	}`

	// The anvil-config skill example.
	skill := `{
		"view": "allow",
		"{edit,write}": "allow",
		"mcp_linear_*": "allow",
		"bash": {
			"*": "ask",
			"git status *": "allow",
			"go test *": "allow",
			"rm *": "deny"
		}
	}`

	for name, doc := range map[string]string{"README": readme, "skill": skill} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var perms Permissions
			require.NoError(t, json.Unmarshal([]byte(doc), &perms))
			require.NotEmpty(t, perms.Rules)
		})
	}
}

// TestDocumentedTrailingStarBehavior pins the "git status *" matches bare
// "git status" claim made in both documents.
func TestDocumentedTrailingStarBehavior(t *testing.T) {
	t.Parallel()

	var perms Permissions
	require.NoError(t, json.Unmarshal([]byte(`{"bash":{"git status *":"allow"}}`), &perms))
	require.Len(t, perms.Rules, 1)
	require.Equal(t, "git status *", perms.Rules[0].SubRules[0].InputPattern)
}

// TestDocumentedLegacyRejectsMixing pins the documented claim that
// allowed_tools cannot be combined with the rule format.
func TestDocumentedLegacyRejectsMixing(t *testing.T) {
	t.Parallel()

	var perms Permissions
	err := json.Unmarshal([]byte(`{"allowed_tools":["bash"],"edit":"allow"}`), &perms)
	require.Error(t, err)
}

// TestDocumentedRedirectRuleParses pins the README example showing how to
// allow a file write by adding a rule for the redirection segment itself.
func TestDocumentedRedirectRuleParses(t *testing.T) {
	t.Parallel()

	var perms Permissions
	require.NoError(t, json.Unmarshal([]byte(`{
		"bash": {
			"*": "ask",
			"echo *": "allow",
			"> /tmp/*": "allow",
			">> /tmp/*": "allow"
		}
	}`), &perms))
	require.Len(t, perms.Rules, 1)
	require.Len(t, perms.Rules[0].SubRules, 4)
}
