package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AgentIDs(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
	}
	cfg.SetupAgents()

	// Verify each agent in the 10-agent roster exists with the correct ID.
	agentNames := []string{
		AgentOrchestrator,
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
	for _, name := range agentNames {
		t.Run(name+" agent should have correct ID", func(t *testing.T) {
			t.Parallel()
			agent, ok := cfg.Agents[name]
			require.Truef(t, ok, "agent %q should be present in default roster", name)
			assert.Equal(t, name, agent.ID, "agent %q: ID field should match map key", name)
		})
	}
}
