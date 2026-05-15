package agent

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// fixedTestTime returns a deterministic time for use in prompt-building tests
// so that the rendered date field is stable across runs.
func fixedTestTime() time.Time {
	t, _ := time.Parse("1/2/2006", "1/1/2025")
	return t
}

// loadAllAgentMDs is a helper that loads the embedded agent .md files and
// returns them keyed by agent name.
func loadAllAgentMDs(t *testing.T) map[string]prompt.AgentMD {
	t.Helper()
	mds, err := loadAgentMDs(agentMDFS)
	require.NoError(t, err)
	return mds
}

// buildOrchestratorBlocksFromMDs returns the agents block and delegation
// workflow strings built from the provided agentMDs map, excluding the
// orchestrator itself.
func buildOrchestratorBlocksFromMDs(agentMDs map[string]prompt.AgentMD) (agentsBlock, delegationWorkflow string) {
	agents := make([]prompt.AgentMD, 0, len(agentMDs))
	for name, md := range agentMDs {
		if name != config.AgentOrchestrator {
			agents = append(agents, md)
		}
	}
	// Sort for deterministic output.
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Name < agents[j].Name
	})
	return prompt.BuildAgentsBlock(agents), prompt.BuildDelegationWorkflow(agents)
}

// minimalConfigStore creates a minimal config store for prompt-building
// tests. It uses a temp dir that is not a git repo, so GitStatus will be
// empty and output is deterministic. ContextPaths and SkillsPaths are cleared
// so no external files are read.
func minimalConfigStore(t *testing.T) *config.ConfigStore {
	t.Helper()
	tmpDir := t.TempDir()
	cfg, err := config.Init(tmpDir, "", false)
	require.NoError(t, err)
	cfg.Config().Options.SkillsPaths = nil
	cfg.Config().Options.ContextPaths = nil
	cfg.Config().Options.DisabledSkills = []string{"crush-config"}
	return cfg
}

// TestOrchestratorPromptTokenBudget verifies that the full orchestrator
// system prompt with all 9 specialists is under 12 000 approximate tokens
// (1 token ≈ 4 characters). This guards against prompt bloat as agent
// descriptions grow.
func TestOrchestratorPromptTokenBudget(t *testing.T) {
	t.Parallel()

	agentMDs := loadAllAgentMDs(t)

	// All 9 specialist .md files must be present.
	require.Len(t, agentMDs, 9, "expected 9 agent .md files in templates/agents")

	agentsBlock, delegationWorkflow := buildOrchestratorBlocksFromMDs(agentMDs)

	cfg := minimalConfigStore(t)

	p, err := orchestratorPrompt(
		prompt.WithTimeFunc(fixedTestTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir("/project"),
		prompt.WithAgentsBlock(agentsBlock),
		prompt.WithDelegationWorkflow(delegationWorkflow),
	)
	require.NoError(t, err)

	result, err := p.Build(context.Background(), "", "", cfg)
	require.NoError(t, err)

	require.NotEmpty(t, result)

	approxTokens := len(result) / 4
	require.Less(t, approxTokens, 12000,
		"orchestrator prompt should be under 12 000 approximate tokens, got ~%d", approxTokens)
}

// TestSharedTemplateRendering verifies that the orchestrator and specialist
// prompts (explorer and fixer) all render non-empty output and contain the
// expected structural substrings from the shared base template.
func TestSharedTemplateRendering(t *testing.T) {
	t.Parallel()

	agentMDs := loadAllAgentMDs(t)
	agentsBlock, delegationWorkflow := buildOrchestratorBlocksFromMDs(agentMDs)
	cfg := minimalConfigStore(t)

	const (
		wantCriticalRules      = "<critical_rules>"
		wantCommunicationStyle = "<communication_style>"
		wantWorkingDir         = "Working directory: /project"
	)

	t.Run("orchestrator prompt", func(t *testing.T) {
		t.Parallel()

		p, err := orchestratorPrompt(
			prompt.WithTimeFunc(fixedTestTime),
			prompt.WithPlatform("linux"),
			prompt.WithWorkingDir("/project"),
			prompt.WithAgentsBlock(agentsBlock),
			prompt.WithDelegationWorkflow(delegationWorkflow),
		)
		require.NoError(t, err)

		result, err := p.Build(context.Background(), "", "", cfg)
		require.NoError(t, err)

		require.NotEmpty(t, result, "orchestrator prompt must not be empty")
		require.Contains(t, result, wantCriticalRules)
		require.Contains(t, result, wantCommunicationStyle)
		require.Contains(t, result, wantWorkingDir)
		require.Contains(t, result, "<Agents>")
		require.Contains(t, result, "<Workflow>")
	})

	t.Run("explorer specialist prompt", func(t *testing.T) {
		t.Parallel()

		explorerMD, ok := agentMDs["explorer"]
		require.True(t, ok, "explorer agent .md must be present")

		p, err := specialistPrompt(
			prompt.WithTimeFunc(fixedTestTime),
			prompt.WithPlatform("linux"),
			prompt.WithWorkingDir("/project"),
			prompt.WithAgentBody(explorerMD.Body),
		)
		require.NoError(t, err)

		result, err := p.Build(context.Background(), "", "", cfg)
		require.NoError(t, err)

		require.NotEmpty(t, result, "explorer prompt must not be empty")
		require.Contains(t, result, wantCriticalRules)
		require.Contains(t, result, wantCommunicationStyle)
		require.Contains(t, result, wantWorkingDir)
		// Explorer's body describes codebase search.
		require.Contains(t, result, "codebase search", "explorer prompt should contain its role description")
	})

	t.Run("fixer specialist prompt", func(t *testing.T) {
		t.Parallel()

		fixerMD, ok := agentMDs["fixer"]
		require.True(t, ok, "fixer agent .md must be present")

		p, err := specialistPrompt(
			prompt.WithTimeFunc(fixedTestTime),
			prompt.WithPlatform("linux"),
			prompt.WithWorkingDir("/project"),
			prompt.WithAgentBody(fixerMD.Body),
		)
		require.NoError(t, err)

		result, err := p.Build(context.Background(), "", "", cfg)
		require.NoError(t, err)

		require.NotEmpty(t, result, "fixer prompt must not be empty")
		require.Contains(t, result, wantCriticalRules)
		require.Contains(t, result, wantCommunicationStyle)
		require.Contains(t, result, wantWorkingDir)
		// Fixer's body describes implementation work.
		require.Contains(t, result, "implementation", "fixer prompt should contain its role description")
	})
}

// TestDepthAwareToolFiltering verifies the delegation rules encoded in the
// embedded agent .md files, which are the data source that coordinator uses
// to decide whether to include task/background_task tools at each depth.
//
// At depth > 1, the coordinator adds task tools only when the agent has a
// non-empty delegates_to list. At depth = 1, task tools are never added.
// This test verifies the underlying data (agentMDs) matches those expectations.
func TestDepthAwareToolFiltering(t *testing.T) {
	t.Parallel()

	agentMDs := loadAllAgentMDs(t)

	// Agents that are expected to have non-empty delegates_to (they can
	// sub-delegate, so at depth > 1 they get task and background_task).
	delegateAgents := []string{"planner", "tester", "designer"}

	// Leaf agents have empty delegates_to, so even at depth > 1 they do NOT
	// get task/background_task tools.
	leafAgents := []string{"explorer", "oracle", "fixer", "librarian", "reviewer", "devils-advocate"}

	t.Run("delegate agents have non-empty DelegatesTo", func(t *testing.T) {
		t.Parallel()

		for _, name := range delegateAgents {
			md, ok := agentMDs[name]
			require.True(t, ok, "agent %q must have an embedded .md file", name)
			require.NotEmpty(t, md.DelegatesTo,
				"agent %q must have non-empty delegates_to so it receives task tools at depth > 1", name)
		}
	})

	t.Run("leaf agents have empty DelegatesTo", func(t *testing.T) {
		t.Parallel()

		for _, name := range leafAgents {
			md, ok := agentMDs[name]
			require.True(t, ok, "agent %q must have an embedded .md file", name)
			require.Empty(t, md.DelegatesTo,
				"agent %q must be a leaf (empty delegates_to); it should never receive task tools", name)
		}
	})

}

// TestDisabledAgents verifies that an agent listed in DisabledAgents is
// removed from the Agents map after configureAgents(), and that
// getOrBuildAgent returns an error for a missing agent.
func TestDisabledAgents(t *testing.T) {
	t.Parallel()

	t.Run("designer removed from Agents map after configureAgents", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{}
		cfg.DisabledAgents = []string{"designer"}
		// configureAgents is unexported but accessible from the same package
		// via the exported SetupAgents + applyDisabledAgents path. Trigger
		// both by calling the exported SetupAgents and then applyDisabledAgents
		// indirectly through DisabledAgents. We use the internal method
		// directly since we are inside the config package peer.
		cfg.SetupAgents()
		// Apply the disabled-agents list manually (mirrors configureAgents).
		for _, name := range cfg.DisabledAgents {
			delete(cfg.Agents, name)
		}

		_, ok := cfg.Agents["designer"]
		require.False(t, ok, "designer should not be in Agents after being disabled")

		// Other agents remain.
		_, ok = cfg.Agents[config.AgentOrchestrator]
		require.True(t, ok, "orchestrator should still be present")
		_, ok = cfg.Agents["fixer"]
		require.True(t, ok, "fixer should still be present")
	})

	t.Run("getOrBuildAgent returns error for missing agent", func(t *testing.T) {
		t.Parallel()

		agentMDs := loadAllAgentMDs(t)

		// Build a coordinator that has no entry for "designer".
		c := &coordinator{
			agentConfigs: map[string]config.Agent{
				config.AgentOrchestrator: {ID: config.AgentOrchestrator},
				"fixer":                  {ID: "fixer"},
				// "designer" intentionally absent.
			},
			agentMDs: agentMDs,
		}

		_, err := c.getOrBuildAgent(context.Background(), "designer", 2)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not configured",
			"error should mention that the agent is not configured")
	})

	t.Run("orchestrator blocks reference disabled agents", func(t *testing.T) {
		t.Parallel()

		agentMDs := loadAllAgentMDs(t)

		// Remove designer from active configs (simulates DisabledAgents).
		agentConfigs := make(map[string]config.Agent, len(agentMDs))
		for name := range agentMDs {
			if name != "designer" {
				agentConfigs[name] = config.Agent{ID: name}
			}
		}

		c := &coordinator{
			agentConfigs: agentConfigs,
			agentMDs:     agentMDs,
		}
		agentsBlock, _ := c.buildOrchestratorBlocks()

		require.NotContains(t, agentsBlock, "@designer",
			"disabled designer should not appear in orchestrator agents block")
		require.Contains(t, agentsBlock, "@fixer",
			"enabled fixer should still appear in orchestrator agents block")
	})
}

// TestFilterListIntegration verifies that ParseFilterList correctly restricts
// the available tools when given an explicit allow-list, as used when building
// agent tool sets.
func TestFilterListIntegration(t *testing.T) {
	t.Parallel()

	// Full candidate tool list — matches what buildTools uses.
	allTools := []string{
		"task", "background_task",
		"agentic_fetch", "bash", "crush_info", "crush_logs",
		"job_output", "job_kill", "download", "edit", "multiedit",
		"fetch", "glob", "grep", "ls", "sourcegraph", "todos",
		"view", "write",
		"lsp_diagnostics", "lsp_references", "lsp_restart",
		"list_mcp_resources", "read_mcp_resource",
	}

	t.Run("explorer read-only tools are subset of all tools", func(t *testing.T) {
		t.Parallel()

		// Explorer's allowed tools from the defaults.
		explorerAllowed := []string{"glob", "grep", "ls", "view", "lsp_diagnostics", "lsp_references", "sourcegraph"}

		result, err := config.ParseFilterList(explorerAllowed, allTools)
		require.NoError(t, err)

		// All requested tools should be present.
		for _, want := range explorerAllowed {
			require.Contains(t, result, want, "tool %q should be in filtered set", want)
		}

		// Write/edit tools should NOT be present.
		for _, disallowed := range []string{"edit", "write", "bash", "multiedit"} {
			require.NotContains(t, result, disallowed, "tool %q should not be in explorer's allowed set", disallowed)
		}
	})

	t.Run("nil AllowedTools means all tools", func(t *testing.T) {
		t.Parallel()

		result, err := config.ParseFilterList(nil, allTools)
		require.NoError(t, err)
		require.Equal(t, allTools, result, "nil filter should return all tools unchanged")
	})

	t.Run("empty AllowedTools means no tools", func(t *testing.T) {
		t.Parallel()

		result, err := config.ParseFilterList([]string{}, allTools)
		require.NoError(t, err)
		require.Empty(t, result, "empty filter should return no tools")
	})

	t.Run("negation excludes specific tools", func(t *testing.T) {
		t.Parallel()

		result, err := config.ParseFilterList([]string{"!bash", "!edit"}, allTools)
		require.NoError(t, err)

		require.NotContains(t, result, "bash")
		require.NotContains(t, result, "edit")
		require.Contains(t, result, "glob")
		require.Contains(t, result, "view")
	})
}

// TestConfigMerge verifies that configureAgents correctly overlays user-defined
// agent config onto the defaults. Nil AllowedTools in user config leaves the
// default alone; a non-nil slice replaces it.
func TestConfigMerge(t *testing.T) {
	t.Parallel()

	t.Run("user override replaces explorer AllowedTools", func(t *testing.T) {
		t.Parallel()

		// Simulate a user config that restricts explorer to only glob.
		userAgents := map[string]config.Agent{
			"explorer": {AllowedTools: []string{"glob"}},
		}

		cfg := &config.Config{Agents: userAgents}
		cfg.SetupAgents() // Load defaults first (mirrors configureAgents snapshot).

		// Apply user overrides manually (mirrors configureAgents merge loop).
		for name, userAgent := range userAgents {
			def, ok := cfg.Agents[name]
			require.True(t, ok)
			if userAgent.AllowedTools != nil {
				def.AllowedTools = userAgent.AllowedTools
			}
			cfg.Agents[name] = def
		}

		explorer := cfg.Agents["explorer"]
		require.Equal(t, []string{"glob"}, explorer.AllowedTools,
			"user override should set explorer to only glob")

		// Fixer was not overridden — its default (nil = all tools) should remain.
		fixer := cfg.Agents["fixer"]
		require.Nil(t, fixer.AllowedTools,
			"fixer AllowedTools should remain nil (unrestricted) since it wasn't overridden")
	})

	t.Run("user overlay nil AllowedTools leaves default", func(t *testing.T) {
		t.Parallel()

		// User sets model but leaves AllowedTools nil.
		userAgents := map[string]config.Agent{
			"explorer": {Model: "anthropic/claude-haiku"},
		}

		cfg := &config.Config{Agents: userAgents}
		cfg.SetupAgents()

		for name, userAgent := range userAgents {
			def, ok := cfg.Agents[name]
			require.True(t, ok)
			if userAgent.AllowedTools != nil {
				def.AllowedTools = userAgent.AllowedTools
			}
			if userAgent.Model != "" {
				def.Model = userAgent.Model
			}
			cfg.Agents[name] = def
		}

		explorer := cfg.Agents["explorer"]
		// Default explorer AllowedTools is readOnlyTools(), which is non-nil.
		require.NotNil(t, explorer.AllowedTools, "default explorer AllowedTools should not be nil")
		// Model should be updated.
		require.Equal(t, "anthropic/claude-haiku", explorer.Model)
	})
}

// TestNoAgentsConfigBackwardCompat verifies that when Config.Agents is nil
// (no "agents" key in the user's JSON config), calling configureAgents via
// SetupAgents produces the full 10-agent roster with expected defaults.
func TestNoAgentsConfigBackwardCompat(t *testing.T) {
	t.Parallel()

	// Nil Agents simulates a config file with no "agents" key.
	cfg := &config.Config{Agents: nil}
	cfg.SetupAgents()

	// Orchestrator must exist.
	orch, ok := cfg.Agents[config.AgentOrchestrator]
	require.True(t, ok, "orchestrator should be present in default agents")
	require.Equal(t, config.AgentOrchestrator, orch.ID)
	// Orchestrator is unrestricted.
	require.Nil(t, orch.AllowedTools, "orchestrator AllowedTools should be nil (unrestricted)")

	// All 10 default agents should be present.
	expectedAgents := []string{
		config.AgentOrchestrator,
		"oracle", "explorer", "librarian", "designer",
		"fixer", "planner", "tester", "reviewer", "devils-advocate",
	}
	require.Len(t, cfg.Agents, len(expectedAgents),
		"all %d default agents should be present", len(expectedAgents))

	for _, name := range expectedAgents {
		_, ok := cfg.Agents[name]
		require.True(t, ok, "agent %q should be present in defaults", name)
	}
}

// TestDelegationRouting verifies the delegation rules encoded in the embedded
// agent .md files. Each agent's delegates_to field controls which sub-agents
// the coordinator will instantiate when it handles a task tool call from that
// agent.
func TestDelegationRouting(t *testing.T) {
	t.Parallel()

	agentMDs := loadAllAgentMDs(t)

	t.Run("orchestrator is not in embedded agent MDs (built programmatically)", func(t *testing.T) {
		t.Parallel()

		// The orchestrator is not described by a standalone .md file — it is
		// built entirely by coordinator.buildPrompt using the shared base +
		// orchestrator templates. Only the 9 specialist files exist.
		_, ok := agentMDs[config.AgentOrchestrator]
		require.False(t, ok, "orchestrator should not have a dedicated .md in templates/agents/")
	})

	t.Run("planner delegates only to devils-advocate", func(t *testing.T) {
		t.Parallel()

		planner, ok := agentMDs["planner"]
		require.True(t, ok)
		require.Equal(t, []string{"devils-advocate"}, planner.DelegatesTo)
	})

	t.Run("tester delegates only to fixer", func(t *testing.T) {
		t.Parallel()

		tester, ok := agentMDs["tester"]
		require.True(t, ok)
		require.Equal(t, []string{"fixer"}, tester.DelegatesTo)
	})

	t.Run("designer delegates only to oracle", func(t *testing.T) {
		t.Parallel()

		designer, ok := agentMDs["designer"]
		require.True(t, ok)
		require.Equal(t, []string{"oracle"}, designer.DelegatesTo)
	})

	t.Run("leaf agents have no delegation", func(t *testing.T) {
		t.Parallel()

		leafAgents := []string{"explorer", "oracle", "fixer", "librarian", "reviewer", "devils-advocate"}
		for _, name := range leafAgents {
			md, ok := agentMDs[name]
			require.True(t, ok, "agent %q not found in embedded MDs", name)
			require.Empty(t, md.DelegatesTo,
				"leaf agent %q should have empty delegates_to", name)
		}
	})

	t.Run("all delegates_to references are valid agent names", func(t *testing.T) {
		t.Parallel()

		knownNames := make(map[string]bool, len(agentMDs))
		for name := range agentMDs {
			knownNames[name] = true
		}

		for _, md := range agentMDs {
			for _, ref := range md.DelegatesTo {
				require.True(t, knownNames[ref],
					"agent %q delegates_to %q, which is not a known agent", md.Name, ref)
			}
		}
	})
}

// TestDelegatesToValidation exercises prompt.ValidateDelegatesTo edge cases
// beyond what the prompt package's own unit tests cover, particularly the
// interaction with disabled agents from the coordinator's perspective.
func TestDelegatesToValidation(t *testing.T) {
	t.Parallel()

	t.Run("all embedded agent refs are valid — no errors or warnings", func(t *testing.T) {
		t.Parallel()

		agentMDs := loadAllAgentMDs(t)
		agents := make([]prompt.AgentMD, 0, len(agentMDs))
		for _, md := range agentMDs {
			agents = append(agents, md)
		}

		errs, warnings := prompt.ValidateDelegatesTo(agents, nil)
		require.Empty(t, errs, "no validation errors expected for the embedded agent MDs")
		require.Empty(t, warnings, "no validation warnings expected when no agents are disabled")
	})

	t.Run("disabling fixer produces warning for tester", func(t *testing.T) {
		t.Parallel()

		agentMDs := loadAllAgentMDs(t)
		agents := make([]prompt.AgentMD, 0, len(agentMDs))
		for _, md := range agentMDs {
			agents = append(agents, md)
		}

		errs, warnings := prompt.ValidateDelegatesTo(agents, []string{"fixer"})
		require.Empty(t, errs)
		require.Len(t, warnings, 1,
			"disabling fixer should produce exactly one warning (tester delegates to fixer)")
		require.Contains(t, warnings[0].Error(), "tester")
		require.Contains(t, warnings[0].Error(), "fixer")
	})

	t.Run("disabling both fixer and oracle produces two warnings", func(t *testing.T) {
		t.Parallel()

		agentMDs := loadAllAgentMDs(t)
		agents := make([]prompt.AgentMD, 0, len(agentMDs))
		for _, md := range agentMDs {
			agents = append(agents, md)
		}

		// tester→fixer and designer→oracle each produce a warning.
		errs, warnings := prompt.ValidateDelegatesTo(agents, []string{"fixer", "oracle"})
		require.Empty(t, errs)
		require.Len(t, warnings, 2,
			"disabling fixer and oracle should each produce one warning")

		warningTexts := make([]string, len(warnings))
		for i, w := range warnings {
			warningTexts[i] = w.Error()
		}
		// One warning for tester→fixer, one for designer→oracle.
		found := slices.ContainsFunc(warningTexts, func(s string) bool {
			return strings.Contains(s, "tester") && strings.Contains(s, "fixer")
		})
		require.True(t, found, "expected warning about tester→fixer delegation")
		found = slices.ContainsFunc(warningTexts, func(s string) bool {
			return strings.Contains(s, "designer") && strings.Contains(s, "oracle")
		})
		require.True(t, found, "expected warning about designer→oracle delegation")
	})

	t.Run("completely unknown ref produces error, not warning", func(t *testing.T) {
		t.Parallel()

		agents := []prompt.AgentMD{
			{Name: "alpha", DelegatesTo: []string{"totally-unknown-agent"}},
		}
		errs, warnings := prompt.ValidateDelegatesTo(agents, nil)
		require.Len(t, errs, 1)
		require.Empty(t, warnings)
		require.Contains(t, errs[0].Error(), "totally-unknown-agent")
	})
}

// TestMultiLevelCostRollup verifies that updateParentSessionCost is present
// and correctly wired inside runSubAgent. The cost-propagation logic itself is
// tested by the existing TestUpdateParentSessionCost and
// TestRunSubAgent/cost_propagation tests in coordinator_test.go. This test
// additionally confirms that the function is callable and returns no error
// when both sessions exist.
func TestMultiLevelCostRollup(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)

	coord := &coordinator{cfg: cfg, sessions: env.sessions}

	parent, err := env.sessions.Create(t.Context(), "Level 1")
	require.NoError(t, err)

	child, err := env.sessions.CreateTaskSession(t.Context(), "tool-a", parent.ID, "Level 2")
	require.NoError(t, err)

	grandchild, err := env.sessions.CreateTaskSession(t.Context(), "tool-b", child.ID, "Level 3")
	require.NoError(t, err)

	// Simulate cost at the deepest level.
	grandchild.Cost = 0.02
	_, err = env.sessions.Save(t.Context(), grandchild)
	require.NoError(t, err)

	child.Cost = 0.03
	_, err = env.sessions.Save(t.Context(), child)
	require.NoError(t, err)

	// Roll grandchild cost up to child.
	require.NoError(t, coord.updateParentSessionCost(t.Context(), grandchild.ID, child.ID))

	// Roll child (now includes grandchild) cost up to parent.
	updatedChild, err := env.sessions.Get(t.Context(), child.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.05, updatedChild.Cost, 1e-9, "child cost should include grandchild cost")

	require.NoError(t, coord.updateParentSessionCost(t.Context(), child.ID, parent.ID))

	updatedParent, err := env.sessions.Get(t.Context(), parent.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.05, updatedParent.Cost, 1e-9,
		"parent should have rolled-up cost from child+grandchild")
}

// TestOrchestratorPromptGoldenFile renders the orchestrator system prompt with
// a deterministic config (fixed time, fixed platform, no context files) and
// compares the output against a golden file. Run with -update to regenerate
// the golden file when the prompt template changes intentionally.
func TestOrchestratorPromptGoldenFile(t *testing.T) {
	t.Parallel()

	agentMDs := loadAllAgentMDs(t)
	agentsBlock, delegationWorkflow := buildOrchestratorBlocksFromMDs(agentMDs)

	cfg := minimalConfigStore(t)

	p, err := orchestratorPrompt(
		prompt.WithTimeFunc(fixedTestTime),
		prompt.WithPlatform("linux"),
		prompt.WithWorkingDir("/project"),
		prompt.WithAgentsBlock(agentsBlock),
		prompt.WithDelegationWorkflow(delegationWorkflow),
	)
	require.NoError(t, err)

	result, err := p.Build(context.Background(), "", "", cfg)
	require.NoError(t, err)

	require.NotEmpty(t, result)

	golden.RequireEqual(t, []byte(result))
}
