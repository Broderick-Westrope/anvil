package prompt

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAgentMD(t *testing.T) {
	t.Parallel()

	t.Run("valid frontmatter with delegates_to", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ndelegates_to: [fixer, oracle]\n---\n- Role: Test agent.\n- Capabilities: Testing.\n")
		got, err := ParseAgentMD("tester", content)
		require.NoError(t, err)
		require.Equal(t, "tester", got.Name)
		require.Equal(t, []string{"fixer", "oracle"}, got.DelegatesTo)
		require.Contains(t, got.Body, "Role: Test agent.")
	})

	t.Run("empty delegates_to list", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ndelegates_to: []\n---\n- Role: Leaf agent.\n")
		got, err := ParseAgentMD("oracle", content)
		require.NoError(t, err)
		require.Equal(t, "oracle", got.Name)
		require.Empty(t, got.DelegatesTo)
		require.Contains(t, got.Body, "Role: Leaf agent.")
	})

	t.Run("no frontmatter treats whole content as body", func(t *testing.T) {
		t.Parallel()
		content := []byte("- Role: Simple agent.\n- Capabilities: Stuff.\n")
		got, err := ParseAgentMD("simple", content)
		require.NoError(t, err)
		require.Equal(t, "simple", got.Name)
		require.Nil(t, got.DelegatesTo)
		require.Equal(t, string(content), got.Body)
	})

	t.Run("body is correctly extracted after frontmatter", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ndelegates_to: []\n---\nFirst line.\n\nSecond paragraph.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, "First line.\n\nSecond paragraph.\n", got.Body)
	})

	t.Run("frontmatter with no delegates_to key", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nsome_other: value\n---\n- Role: Agent without delegation field.\n")
		got, err := ParseAgentMD("misc", content)
		require.NoError(t, err)
		require.Equal(t, "misc", got.Name)
		require.Nil(t, got.DelegatesTo)
		require.Contains(t, got.Body, "Role: Agent without delegation field.")
	})

	t.Run("invalid YAML in frontmatter returns error", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ndelegates_to: [unclosed\n---\n- Role: Agent.\n")
		_, err := ParseAgentMD("bad", content)
		require.Error(t, err)
	})
}

func TestBuildAgentsBlock(t *testing.T) {
	t.Parallel()

	agents := []AgentMD{
		{
			Name:        "oracle",
			DelegatesTo: nil,
			Body:        "- Role: Strategic advisor.\n- Capabilities: Deep reasoning.\n",
		},
		{
			Name:        "fixer",
			DelegatesTo: nil,
			Body:        "- Role: Implementation specialist.\n- Capabilities: Code changes.\n",
		},
		{
			Name:        "explorer",
			DelegatesTo: nil,
			Body:        "- Role: Search specialist.\n- Capabilities: Glob and grep.\n",
		},
	}

	t.Run("wraps output in Agents tags", func(t *testing.T) {
		t.Parallel()
		result := BuildAgentsBlock(agents)
		require.True(t, strings.HasPrefix(result, "<Agents>"), "should start with <Agents>")
		require.True(t, strings.HasSuffix(result, "</Agents>"), "should end with </Agents>")
	})

	t.Run("contains all agent names prefixed with @", func(t *testing.T) {
		t.Parallel()
		result := BuildAgentsBlock(agents)
		require.Contains(t, result, "@oracle")
		require.Contains(t, result, "@fixer")
		require.Contains(t, result, "@explorer")
	})

	t.Run("contains agent body content", func(t *testing.T) {
		t.Parallel()
		result := BuildAgentsBlock(agents)
		require.Contains(t, result, "Strategic advisor")
		require.Contains(t, result, "Implementation specialist")
		require.Contains(t, result, "Search specialist")
	})

	t.Run("empty agents list produces minimal block", func(t *testing.T) {
		t.Parallel()
		result := BuildAgentsBlock(nil)
		require.Contains(t, result, "<Agents>")
		require.Contains(t, result, "</Agents>")
	})
}

func TestBuildDelegationWorkflow(t *testing.T) {
	t.Parallel()

	// Full roster of 9 agents matching the project spec.
	fullRoster := []AgentMD{
		{Name: "oracle"},
		{Name: "explorer"},
		{Name: "librarian"},
		{Name: "designer"},
		{Name: "fixer"},
		{Name: "planner"},
		{Name: "tester"},
		{Name: "reviewer"},
		{Name: "devils-advocate"},
	}

	t.Run("contains all 6 workflow steps", func(t *testing.T) {
		t.Parallel()
		result := BuildDelegationWorkflow(fullRoster)
		require.Contains(t, result, "1. **Understand**")
		require.Contains(t, result, "2. **Path Selection**")
		require.Contains(t, result, "3. **Delegation Check**")
		require.Contains(t, result, "4. **Split and Parallelize**")
		require.Contains(t, result, "5. **Execute**")
		require.Contains(t, result, "6. **Verify**")
	})

	t.Run("wrapped in Workflow tags", func(t *testing.T) {
		t.Parallel()
		result := BuildDelegationWorkflow(fullRoster)
		require.True(t, strings.HasPrefix(result, "<Workflow>"), "should start with <Workflow>")
		require.True(t, strings.HasSuffix(result, "</Workflow>"), "should end with </Workflow>")
	})

	t.Run("contains all enabled agent names", func(t *testing.T) {
		t.Parallel()
		result := BuildDelegationWorkflow(fullRoster)
		for _, a := range fullRoster {
			require.Contains(t, result, "@"+a.Name, "should mention @%s", a.Name)
		}
	})

	t.Run("subset roster omits disabled agent routing rules", func(t *testing.T) {
		t.Parallel()
		subset := []AgentMD{
			{Name: "oracle"},
			{Name: "fixer"},
		}
		result := BuildDelegationWorkflow(subset)
		// Designer is not in the subset, so its routing rule should be absent.
		require.NotContains(t, result, "@designer")
		// Enabled agents should still appear.
		require.Contains(t, result, "@oracle")
		require.Contains(t, result, "@fixer")
	})

	t.Run("approximate token count under 4k for full roster", func(t *testing.T) {
		t.Parallel()
		// Build a realistic agents block using the actual agent .md files is not
		// possible without embedding here, so we approximate with representative
		// bodies that match typical agent description length.
		realisticRoster := []AgentMD{
			{Name: "oracle", Body: "- Role: Strategic advisor for deep reasoning, architecture decisions, and complex debugging.\n- Capabilities: Thorough analysis of ambiguous or high-stakes problems.\n- Delegate when: Genuinely uncertain about a high-stakes decision.\n- Don't delegate when: Routine implementation decisions can be made confidently.\n"},
			{Name: "explorer", Body: "- Role: Fast codebase search and pattern-matching specialist.\n- Capabilities: Glob file discovery, grep content search, AST-aware queries.\n- Delegate when: Discovering what exists before planning work.\n- Don't delegate when: You already know the exact file path.\n"},
			{Name: "librarian", Body: "- Role: External documentation and library research specialist.\n- Capabilities: Fetches latest official docs, API signatures, usage examples.\n- Delegate when: Working with libraries that have frequent API changes.\n- Don't delegate when: The API is stable and well-known.\n"},
			{Name: "designer", Body: "- Role: UI/UX specialist for intentional, polished user-facing experiences.\n- Capabilities: Visual direction, responsive layouts, design systems.\n- Delegate when: The output is user-facing and polish matters.\n- Don't delegate when: The work is purely backend or logic.\n"},
			{Name: "fixer", Body: "- Role: Fast, bounded implementation specialist.\n- Capabilities: Receives complete context and a clear task specification, then executes.\n- Delegate when: The implementation work is well-defined with a clear spec.\n- Don't delegate when: The task still needs discovery or research.\n"},
			{Name: "planner", Body: "- Role: Feature planning and specification specialist.\n- Capabilities: Grilling users to surface requirements, brainstorming approaches.\n- Delegate when: Starting a new feature that needs structured planning.\n- Don't delegate when: The change is small enough that a plan would cost more time.\n"},
			{Name: "tester", Body: "- Role: Test analysis and planning specialist.\n- Capabilities: Analyses the codebase to identify coverage gaps.\n- Delegate when: Writing a comprehensive test suite for a module or feature.\n- Don't delegate when: Adding a single test case to an already well-covered area.\n"},
			{Name: "reviewer", Body: "- Role: Code and PR reviewer focused on implementation quality.\n- Capabilities: Reviews diffs and implementations for correctness.\n- Delegate when: Reviewing a set of code changes or a pull request.\n- Don't delegate when: Doing a quick self-check on a small, obviously correct change.\n"},
			{Name: "devils-advocate", Body: "- Role: Rigorous critic that finds weaknesses in specs and plans.\n- Capabilities: Identifies unstated assumptions, edge cases, hidden complexity.\n- Delegate when: A spec or plan needs adversarial review before implementation.\n- Don't delegate when: The work is implementation.\n"},
		}

		agentsBlock := BuildAgentsBlock(realisticRoster)
		workflow := BuildDelegationWorkflow(realisticRoster)
		combined := agentsBlock + workflow

		// Approximate token count: 1 token ≈ 4 characters.
		approxTokens := len(combined) / 4
		require.Less(t, approxTokens, 4000,
			"combined agents block and workflow should be under 4k tokens, got ~%d", approxTokens)
	})
}

func TestValidateDelegatesTo(t *testing.T) {
	t.Parallel()

	allAgents := []AgentMD{
		{Name: "orchestrator", DelegatesTo: []string{"oracle", "fixer", "planner"}},
		{Name: "oracle", DelegatesTo: nil},
		{Name: "fixer", DelegatesTo: nil},
		{Name: "planner", DelegatesTo: []string{"devils-advocate"}},
		{Name: "devils-advocate", DelegatesTo: nil},
	}

	t.Run("all valid refs produce no errors or warnings", func(t *testing.T) {
		t.Parallel()
		errs, warnings := ValidateDelegatesTo(allAgents, nil)
		require.Empty(t, errs)
		require.Empty(t, warnings)
	})

	t.Run("reference to unknown agent produces error", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "alpha", DelegatesTo: []string{"nonexistent"}},
		}
		errs, warnings := ValidateDelegatesTo(agents, nil)
		require.Len(t, errs, 1)
		require.Empty(t, warnings)
		require.Contains(t, errs[0].Error(), "unknown agent")
		require.Contains(t, errs[0].Error(), "nonexistent")
	})

	t.Run("reference to disabled agent produces warning not error", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "planner", DelegatesTo: []string{"devils-advocate"}},
			{Name: "devils-advocate", DelegatesTo: nil},
		}
		disabled := []string{"devils-advocate"}
		errs, warnings := ValidateDelegatesTo(agents, disabled)
		require.Empty(t, errs)
		require.Len(t, warnings, 1)
		require.Contains(t, warnings[0].Error(), "disabled agent")
		require.Contains(t, warnings[0].Error(), "devils-advocate")
	})

	t.Run("mixed valid and invalid refs returns only errors for invalid", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "orchestrator", DelegatesTo: []string{"oracle", "missing-agent"}},
			{Name: "oracle", DelegatesTo: nil},
		}
		errs, warnings := ValidateDelegatesTo(agents, nil)
		require.Len(t, errs, 1)
		require.Empty(t, warnings)
		require.Contains(t, errs[0].Error(), "missing-agent")
	})

	t.Run("multiple missing refs produces multiple errors", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "alpha", DelegatesTo: []string{"ghost1", "ghost2"}},
		}
		errs, warnings := ValidateDelegatesTo(agents, nil)
		require.Len(t, errs, 2)
		require.Empty(t, warnings)
	})

	t.Run("disabled ref takes priority over missing check", func(t *testing.T) {
		t.Parallel()
		// Agent "beta" is disabled (not in the agents slice but in disabledAgents).
		// It should produce a warning, not an error.
		agents := []AgentMD{
			{Name: "alpha", DelegatesTo: []string{"beta"}},
		}
		disabled := []string{"beta"}
		errs, warnings := ValidateDelegatesTo(agents, disabled)
		require.Empty(t, errs)
		require.Len(t, warnings, 1)
	})

	t.Run("empty agents list produces no errors", func(t *testing.T) {
		t.Parallel()
		errs, warnings := ValidateDelegatesTo(nil, nil)
		require.Empty(t, errs)
		require.Empty(t, warnings)
	})
}
