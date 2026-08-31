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
			Name:             "oracle",
			DelegatesTo:      nil,
			Role:             "Strategic advisor.",
			DelegateWhen:     "Deep reasoning needed.",
			DontDelegateWhen: "Routine decisions.",
			Body:             "Detailed specialist instructions for oracle.",
		},
		{
			Name:             "fixer",
			DelegatesTo:      nil,
			Role:             "Implementation specialist.",
			DelegateWhen:     "Well-defined implementation work.",
			DontDelegateWhen: "Task still needs discovery.",
			Body:             "Detailed specialist instructions for fixer.",
		},
		{
			Name:             "explorer",
			DelegatesTo:      nil,
			Role:             "Search specialist.",
			DelegateWhen:     "Broad codebase discovery.",
			DontDelegateWhen: "Exact file path is known.",
			Body:             "Detailed specialist instructions for explorer.",
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

	t.Run("contains routing field content not body", func(t *testing.T) {
		t.Parallel()
		result := BuildAgentsBlock(agents)
		require.Contains(t, result, "- Role: Strategic advisor.")
		require.Contains(t, result, "- Role: Implementation specialist.")
		require.Contains(t, result, "- Role: Search specialist.")
		require.Contains(t, result, "- Delegate when: Deep reasoning needed.")
		require.Contains(t, result, "- Don't delegate when: Routine decisions.")
		// Body must NOT appear in the agents block.
		require.NotContains(t, result, "Detailed specialist instructions")
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
		{Name: "oracle", RoutingHint: "Route deep reasoning, high-stakes architecture decisions, or persistent bugs to @oracle."},
		{Name: "explorer", RoutingHint: "Route broad codebase discovery and parallel search tasks to @explorer."},
		{Name: "librarian", RoutingHint: "Route external documentation lookup and unfamiliar library research to @librarian."},
		{Name: "designer", RoutingHint: "Route UI/UX work and user-facing polish to @designer."},
		{Name: "fixer", RoutingHint: "Route well-defined, bounded implementation work and test writing to @fixer."},
		{Name: "planner", RoutingHint: "Route feature planning, requirement interviews, and spec writing to @planner."},
		{Name: "tester", RoutingHint: "Route comprehensive test strategy, coverage analysis, and flaky-test diagnosis to @tester."},
		{Name: "reviewer", RoutingHint: "Route code review, diff analysis, and PR quality checks to @reviewer."},
		{Name: "devils-advocate", RoutingHint: "Route adversarial review of specs and plans to @devils-advocate."},
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
			{Name: "oracle", RoutingHint: "Route deep reasoning, high-stakes architecture decisions, or persistent bugs to @oracle."},
			{Name: "fixer", RoutingHint: "Route well-defined, bounded implementation work and test writing to @fixer."},
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
		// routing fields that match typical frontmatter content.
		realisticRoster := []AgentMD{
			{Name: "oracle", Role: "Strategic advisor for deep reasoning, architecture decisions, and complex debugging.", DelegateWhen: "Genuinely uncertain about a high-stakes decision.", DontDelegateWhen: "Routine implementation decisions can be made confidently.", RoutingHint: "Route deep reasoning, high-stakes architecture decisions, or persistent bugs to @oracle."},
			{Name: "explorer", Role: "Fast codebase search and pattern-matching specialist.", DelegateWhen: "Discovering what exists before planning work.", DontDelegateWhen: "You already know the exact file path.", RoutingHint: "Route broad codebase discovery and parallel search tasks to @explorer."},
			{Name: "librarian", Role: "External documentation and library research specialist.", DelegateWhen: "Working with libraries that have frequent API changes.", DontDelegateWhen: "The API is stable and well-known.", RoutingHint: "Route external documentation lookup and unfamiliar library research to @librarian."},
			{Name: "designer", Role: "UI/UX specialist for intentional, polished user-facing experiences.", DelegateWhen: "The output is user-facing and polish matters.", DontDelegateWhen: "The work is purely backend or logic.", RoutingHint: "Route UI/UX work and user-facing polish to @designer."},
			{Name: "fixer", Role: "Fast, bounded implementation specialist.", DelegateWhen: "The implementation work is well-defined with a clear spec.", DontDelegateWhen: "The task still needs discovery or research.", RoutingHint: "Route well-defined, bounded implementation work and test writing to @fixer."},
			{Name: "planner", Role: "Feature planning and specification specialist.", DelegateWhen: "Starting a new feature that needs structured planning.", DontDelegateWhen: "The change is small enough that a plan would cost more time.", RoutingHint: "Route feature planning, requirement interviews, and spec writing to @planner."},
			{Name: "tester", Role: "Test analysis and planning specialist.", DelegateWhen: "Writing a comprehensive test suite for a module or feature.", DontDelegateWhen: "Adding a single test case to an already well-covered area.", RoutingHint: "Route comprehensive test strategy, coverage analysis, and flaky-test diagnosis to @tester."},
			{Name: "reviewer", Role: "Code and PR reviewer focused on implementation quality.", DelegateWhen: "Reviewing a set of code changes or a pull request.", DontDelegateWhen: "Doing a quick self-check on a small, obviously correct change.", RoutingHint: "Route code review, diff analysis, and PR quality checks to @reviewer."},
			{Name: "devils-advocate", Role: "Rigorous critic that finds weaknesses in specs and plans.", DelegateWhen: "A spec or plan needs adversarial review before implementation.", DontDelegateWhen: "The work is implementation.", RoutingHint: "Route adversarial review of specs and plans to @devils-advocate."},
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

func TestParseAgentMD_FullFrontmatter(t *testing.T) {
	t.Parallel()

	content := []byte(
		"---\n" +
			"delegates_to: [fixer, explorer]\n" +
			"role: Deep reasoning specialist.\n" +
			"delegate_when: High-stakes decisions needed.\n" +
			"dont_delegate_when: Routine tasks are sufficient.\n" +
			"---\n" +
			"# Oracle\n" +
			"\n" +
			"You are the oracle specialist. Apply deep reasoning to every problem.\n",
	)

	got, err := ParseAgentMD("oracle", content)
	require.NoError(t, err)
	require.Equal(t, "oracle", got.Name)
	require.Equal(t, []string{"fixer", "explorer"}, got.DelegatesTo)
	require.Equal(t, "Deep reasoning specialist.", got.Role)
	require.Equal(t, "High-stakes decisions needed.", got.DelegateWhen)
	require.Equal(t, "Routine tasks are sufficient.", got.DontDelegateWhen)
	// Body should be only the markdown after the closing ---.
	require.Equal(t, "# Oracle\n\nYou are the oracle specialist. Apply deep reasoning to every problem.\n", got.Body)
	// Body must not contain any frontmatter keys.
	require.NotContains(t, got.Body, "delegates_to")
	require.NotContains(t, got.Body, "delegate_when")
	require.NotContains(t, got.Body, "dont_delegate_when")
}

func TestBuildAgentsBlock_UsesRoutingNotBody(t *testing.T) {
	t.Parallel()

	agents := []AgentMD{
		{
			Name:             "oracle",
			Role:             "Strategic routing role.",
			DelegateWhen:     "When oracle is best.",
			DontDelegateWhen: "When oracle is not needed.",
			Body:             "SECRET BODY CONTENT that must not appear in the agents block.",
		},
		{
			Name: "fixer",
			Role: "Implementation routing role.",
			// No DelegateWhen or DontDelegateWhen — optional fields.
			Body: "ANOTHER SECRET BODY that must not appear.",
		},
	}

	result := BuildAgentsBlock(agents)

	// Routing fields must be present.
	require.Contains(t, result, "- Role: Strategic routing role.")
	require.Contains(t, result, "- Delegate when: When oracle is best.")
	require.Contains(t, result, "- Don't delegate when: When oracle is not needed.")
	require.Contains(t, result, "- Role: Implementation routing role.")

	// Body text must NOT appear in the agents block.
	require.NotContains(t, result, "SECRET BODY CONTENT")
	require.NotContains(t, result, "ANOTHER SECRET BODY")

	// Optional fields absent on fixer should not produce empty lines with labels.
	require.NotContains(t, result, "- Delegate when: \n")
	require.NotContains(t, result, "- Don't delegate when: \n")
}

func TestParseAgentMD_CapabilityFields(t *testing.T) {
	t.Parallel()

	t.Run("absent tools field means nil (all tools)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nrole: Agent.\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.Tools)
	})

	t.Run("tools null means nil (all tools)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ntools: null\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.Tools)
	})

	t.Run("tools empty slice means no tools", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ntools: []\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.NotNil(t, got.Tools)
		require.Empty(t, got.Tools)
	})

	t.Run("tools list is parsed correctly", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\ntools: [glob, grep]\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, []string{"glob", "grep"}, got.Tools)
	})

	t.Run("absent skills field means nil (all skills)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nrole: Agent.\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.Skills)
	})

	t.Run("skills null means nil (all skills)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nskills: null\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.Skills)
	})

	t.Run("skills empty slice means no skills", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nskills: []\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.NotNil(t, got.Skills)
		require.Empty(t, got.Skills)
	})

	t.Run("skills list is parsed correctly", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nskills: [search, summarise]\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, []string{"search", "summarise"}, got.Skills)
	})

	t.Run("absent mcps field means nil (all MCPs)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nrole: Agent.\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.MCPs)
	})

	t.Run("mcps null means nil (all MCPs)", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmcps: null\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Nil(t, got.MCPs)
	})

	t.Run("mcps empty map means no MCPs", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmcps: {}\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.NotNil(t, got.MCPs)
		require.Empty(t, got.MCPs)
	})

	t.Run("mcps map with nil value means all tools from that server", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmcps:\n  linear: null\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.NotNil(t, got.MCPs)
		val, ok := got.MCPs["linear"]
		require.True(t, ok)
		require.Nil(t, val)
	})

	t.Run("mcps map with specific tools restricts to those tools", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmcps:\n  linear: [search, create]\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, []string{"search", "create"}, got.MCPs["linear"])
	})

	t.Run("model field is parsed correctly", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmodel: anthropic/claude-opus-4-6\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, "anthropic/claude-opus-4-6", got.Model)
	})

	t.Run("absent model field means empty string", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nrole: Agent.\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Empty(t, got.Model)
	})

	t.Run("routing_hint is parsed correctly", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nrouting_hint: Route X to @agent.\n---\nBody.\n")
		got, err := ParseAgentMD("agent", content)
		require.NoError(t, err)
		require.Equal(t, "Route X to @agent.", got.RoutingHint)
	})

	t.Run("full frontmatter with all capability fields parses correctly", func(t *testing.T) {
		t.Parallel()
		content := []byte(
			"---\n" +
				"delegates_to: [fixer]\n" +
				"role: Full agent.\n" +
				"delegate_when: Always.\n" +
				"dont_delegate_when: Never.\n" +
				"model: anthropic/claude-opus-4-6\n" +
				"tools: [glob, grep, bash]\n" +
				"skills: [search]\n" +
				"mcps:\n" +
				"  linear: [create, search]\n" +
				"  datadog: null\n" +
				"---\n" +
				"Body content.\n",
		)
		got, err := ParseAgentMD("full", content)
		require.NoError(t, err)
		require.Equal(t, "full", got.Name)
		require.Equal(t, []string{"fixer"}, got.DelegatesTo)
		require.Equal(t, "Full agent.", got.Role)
		require.Equal(t, "Always.", got.DelegateWhen)
		require.Equal(t, "Never.", got.DontDelegateWhen)
		require.Equal(t, "anthropic/claude-opus-4-6", got.Model)
		require.Equal(t, []string{"glob", "grep", "bash"}, got.Tools)
		require.Equal(t, []string{"search"}, got.Skills)
		require.Equal(t, []string{"create", "search"}, got.MCPs["linear"])
		require.Nil(t, got.MCPs["datadog"])
		require.Equal(t, "Body content.\n", got.Body)
	})
}

func TestValidateDelegatesTo_CycleDetection(t *testing.T) {
	t.Parallel()

	t.Run("direct cycle A to B to A produces error", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "a", DelegatesTo: []string{"b"}},
			{Name: "b", DelegatesTo: []string{"a"}},
		}
		errs, _ := ValidateDelegatesTo(agents, nil)
		require.NotEmpty(t, errs)
		require.Contains(t, errs[0].Error(), "delegation cycle detected")
	})

	t.Run("indirect cycle A to B to C to A produces error", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "a", DelegatesTo: []string{"b"}},
			{Name: "b", DelegatesTo: []string{"c"}},
			{Name: "c", DelegatesTo: []string{"a"}},
		}
		errs, _ := ValidateDelegatesTo(agents, nil)
		require.NotEmpty(t, errs)
		require.Contains(t, errs[0].Error(), "delegation cycle detected")
	})

	t.Run("self reference A to A produces error", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "a", DelegatesTo: []string{"a"}},
		}
		errs, _ := ValidateDelegatesTo(agents, nil)
		require.NotEmpty(t, errs)
		require.Contains(t, errs[0].Error(), "delegation cycle detected")
	})

	t.Run("no cycle produces no cycle warnings", func(t *testing.T) {
		t.Parallel()
		agents := []AgentMD{
			{Name: "orchestrator", DelegatesTo: []string{"fixer", "oracle"}},
			{Name: "fixer", DelegatesTo: nil},
			{Name: "oracle", DelegatesTo: nil},
		}
		errs, warnings := ValidateDelegatesTo(agents, nil)
		require.Empty(t, errs)
		require.Empty(t, warnings)
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

func TestParseAgentMD_ModelTuningFields(t *testing.T) {
	t.Parallel()

	t.Run("all fields present", func(t *testing.T) {
		t.Parallel()
		content := []byte(
			"---\n" +
				"role: Search specialist.\n" +
				"model: anthropic/claude-sonnet-5\n" +
				"reasoning_effort: low\n" +
				"think: false\n" +
				"---\n" +
				"Body.\n",
		)
		got, err := ParseAgentMD("explorer", content)
		require.NoError(t, err)
		require.Equal(t, "anthropic/claude-sonnet-5", got.Model)
		require.Equal(t, "low", got.ReasoningEffort)
		require.NotNil(t, got.Think)
		require.False(t, *got.Think)
	})

	t.Run("absent think is nil so it inherits", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nmodel: anthropic/claude-opus-5\n---\nBody.\n")
		got, err := ParseAgentMD("planner", content)
		require.NoError(t, err)
		require.Equal(t, "anthropic/claude-opus-5", got.Model)
		require.Empty(t, got.ReasoningEffort)
		require.Nil(t, got.Think)
	})

	t.Run("think true is distinguishable from absent", func(t *testing.T) {
		t.Parallel()
		content := []byte("---\nthink: true\n---\nBody.\n")
		got, err := ParseAgentMD("oracle", content)
		require.NoError(t, err)
		require.NotNil(t, got.Think)
		require.True(t, *got.Think)
	})
}
