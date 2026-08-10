package permission

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestEvaluate(t *testing.T) {
	t.Parallel()

	t.Run("no rules returns default ask", func(t *testing.T) {
		t.Parallel()
		result := Evaluate("bash", "ls", nil, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
		require.Empty(t, result.MatchedRule)
	})

	t.Run("wildcard allow matches all tools", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "*", Action: config.PermissionAllow},
		}
		result := Evaluate("bash", "", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.False(t, result.IsDefault)
		require.Equal(t, "*", result.MatchedRule)
	})

	t.Run("tool-specific rule overrides wildcard (last match wins)", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "*", Action: config.PermissionAllow},
			{ToolPattern: "bash", Action: config.PermissionDeny},
		}
		result := Evaluate("bash", "", rules, nil)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "bash", result.MatchedRule)
	})

	t.Run("sub-rules match input patterns", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "*", Action: config.PermissionAsk},
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}

		// "git status" matches both "*" and "git *"; last match wins.
		result := Evaluate("bash", "git status", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "bash:git *", result.MatchedRule)
	})

	t.Run("sub-rules last match wins", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "*", Action: config.PermissionAsk},
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}

		// "git status" matches both "*" and "git *"; last match wins.
		result := Evaluate("bash", "git status", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "bash:git *", result.MatchedRule)

		// "echo hello" matches only "*".
		result = Evaluate("bash", "echo hello", rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.Equal(t, "bash:*", result.MatchedRule)
	})

	t.Run("brace expansion matches multiple tools", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "{edit,write}", Action: config.PermissionAllow},
		}

		result := Evaluate("edit", "", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)

		result = Evaluate("write", "", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)

		result = Evaluate("bash", "", rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
	})

	t.Run("session grant upgrades ask to allow", func(t *testing.T) {
		t.Parallel()
		configRules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAsk},
		}
		sessionRules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAllow},
		}

		result := Evaluate("bash", "", configRules, sessionRules)
		require.Equal(t, config.PermissionAllow, result.Action)
	})

	t.Run("session grant cannot override config deny", func(t *testing.T) {
		t.Parallel()
		configRules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionDeny},
		}
		sessionRules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAllow},
		}

		result := Evaluate("bash", "", configRules, sessionRules)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "bash", result.MatchedRule)
	})

	t.Run("session grant with glob pattern", func(t *testing.T) {
		t.Parallel()
		configRules := []config.PermissionRule{
			{ToolPattern: "*", Action: config.PermissionAsk},
		}
		sessionRules := []config.PermissionRule{
			{ToolPattern: "mcp__*", Action: config.PermissionAllow},
		}

		result := Evaluate("mcp__server", "", configRules, sessionRules)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "mcp__*", result.MatchedRule)

		// Non-matching tool still gets config result.
		result = Evaluate("bash", "", configRules, sessionRules)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.Equal(t, "*", result.MatchedRule)
	})

	t.Run("empty input for tool-name-only matching", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "view", Action: config.PermissionAllow},
		}

		result := Evaluate("view", "", rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "view", result.MatchedRule)
	})

	t.Run("multiple matching tool patterns last one wins", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "*", Action: config.PermissionAllow},
			{ToolPattern: "bash", Action: config.PermissionAsk},
			{ToolPattern: "b*", Action: config.PermissionDeny},
		}

		result := Evaluate("bash", "", rules, nil)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "b*", result.MatchedRule)
	})

	t.Run("unmatched tool returns default ask", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAllow},
		}

		result := Evaluate("view", "", rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
	})

	t.Run("session rules only no config rules", func(t *testing.T) {
		t.Parallel()
		sessionRules := []config.PermissionRule{
			{ToolPattern: "bash", Action: config.PermissionAllow},
		}

		result := Evaluate("bash", "", nil, sessionRules)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "bash", result.MatchedRule)
	})

	t.Run("sub-rule no input match falls through", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}

		// Tool matches but no sub-rule input matches.
		result := Evaluate("bash", "rm -rf /", rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
	})
}

func TestEvaluateAll(t *testing.T) {
	t.Parallel()

	t.Run("empty inputs behaves like single empty input", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{ToolPattern: "view", Action: config.PermissionAllow},
		}

		result := EvaluateAll("view", nil, rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "view", result.MatchedRule)

		result = EvaluateAll("bash", nil, rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
	})

	t.Run("single input allow", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}

		result := EvaluateAll("bash", []string{"git status"}, rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
		require.Equal(t, "bash:git *", result.MatchedRule)
	})

	t.Run("all inputs allow", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
					{InputPattern: "ls *", Action: config.PermissionAllow},
				},
			},
		}

		result := EvaluateAll("bash", []string{"git status", "ls -la"}, rules, nil)
		require.Equal(t, config.PermissionAllow, result.Action)
	})

	t.Run("one input unmatched returns ask", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}

		result := EvaluateAll("bash", []string{"git status", "rm -rf /"}, rules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)
		require.True(t, result.IsDefault)
	})

	t.Run("one input deny beats allows", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
					{InputPattern: "rm *", Action: config.PermissionDeny},
				},
			},
		}

		result := EvaluateAll("bash", []string{"git status", "rm -rf /"}, rules, nil)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "bash:rm *", result.MatchedRule)
	})

	t.Run("deny beats ask", func(t *testing.T) {
		t.Parallel()
		rules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "rm *", Action: config.PermissionDeny},
				},
			},
		}

		// "echo hello" is unmatched (ask), "rm -rf /" is denied.
		result := EvaluateAll("bash", []string{"echo hello", "rm -rf /"}, rules, nil)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "bash:rm *", result.MatchedRule)
	})

	t.Run("session grant upgrades ask segment to allow", func(t *testing.T) {
		t.Parallel()
		configRules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
				},
			},
		}
		sessionRules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "ls *", Action: config.PermissionAllow},
				},
			},
		}

		// Without the session grant, "ls -la" is unmatched → ask.
		result := EvaluateAll("bash", []string{"git status", "ls -la"}, configRules, nil)
		require.Equal(t, config.PermissionAsk, result.Action)

		// With the session grant, all segments allow.
		result = EvaluateAll("bash", []string{"git status", "ls -la"}, configRules, sessionRules)
		require.Equal(t, config.PermissionAllow, result.Action)
	})

	t.Run("session grant cannot override deny segment", func(t *testing.T) {
		t.Parallel()
		configRules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "git *", Action: config.PermissionAllow},
					{InputPattern: "rm *", Action: config.PermissionDeny},
				},
			},
		}
		sessionRules := []config.PermissionRule{
			{
				ToolPattern: "bash",
				SubRules: []config.PermissionSubRule{
					{InputPattern: "rm *", Action: config.PermissionAllow},
				},
			},
		}

		result := EvaluateAll("bash", []string{"git status", "rm -rf /"}, configRules, sessionRules)
		require.Equal(t, config.PermissionDeny, result.Action)
		require.Equal(t, "bash:rm *", result.MatchedRule)
	})
}
