package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_AgentIDs(t *testing.T) {
	cfg := &Config{
		Options: &Options{
			DisabledTools: []string{},
		},
	}
	cfg.SetupAgents()

	t.Run("Orchestrator agent should have correct ID", func(t *testing.T) {
		coderAgent, ok := cfg.Agents[AgentOrchestrator]
		require.True(t, ok)
		assert.Equal(t, AgentOrchestrator, coderAgent.ID, "Orchestrator agent ID should be '%s'", AgentOrchestrator)
	})

	t.Run("Task agent should have correct ID", func(t *testing.T) {
		taskAgent, ok := cfg.Agents[AgentTask]
		require.True(t, ok)
		assert.Equal(t, AgentTask, taskAgent.ID, "Task agent ID should be '%s'", AgentTask)
	})
}
