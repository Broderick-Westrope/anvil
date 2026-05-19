package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AgentIDs(t *testing.T) {
	t.Parallel()

	t.Run("setupDefaultAgents produces only orchestrator", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Options: &Options{
				DisabledTools: []string{},
			},
		}
		cfg.setupDefaultAgents()

		require.Len(t, cfg.Agents, 1, "setupDefaultAgents should produce only the orchestrator")
		agent, ok := cfg.Agents[AgentOrchestrator]
		require.True(t, ok, "orchestrator should be present")
		assert.Equal(t, AgentOrchestrator, agent.ID)
	})

	t.Run("SetupAgentsWithDefaults produces full roster with correct IDs", func(t *testing.T) {
		t.Parallel()

		nonOrchestratorNames := []string{
			"oracle",
			"explorer",
			"librarian",
			"designer",
			"fixer",
			"planner",
			"tester",
			"reviewer",
			"devils-advocate",
		}

		mdDefaults := make(map[string]Agent, len(nonOrchestratorNames))
		for _, name := range nonOrchestratorNames {
			mdDefaults[name] = Agent{ID: name, Name: name}
		}

		cfg := &Config{
			Options: &Options{
				DisabledTools: []string{},
			},
		}
		cfg.SetupAgentsWithDefaults(mdDefaults)

		allNames := append([]string{AgentOrchestrator}, nonOrchestratorNames...)
		for _, name := range allNames {
			t.Run(name+" agent should have correct ID", func(t *testing.T) {
				t.Parallel()
				agent, ok := cfg.Agents[name]
				require.Truef(t, ok, "agent %q should be present", name)
				assert.Equal(t, name, agent.ID, "agent %q: ID field should match map key", name)
			})
		}
	})
}
