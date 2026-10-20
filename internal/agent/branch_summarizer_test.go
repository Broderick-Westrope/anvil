package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOrchestratorPromptOmitsBranchSummarizers(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, pluginDir := newReloadTestCoordinator(t, env)
	summarizer := []byte("---\nrole: summarizes branches\nbranch_summarizer: true\n---\nSummarize.\n")
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "agents", "plugsum.md"), summarizer, 0o644))
	_, err := c.ReloadPlugins(t.Context())
	require.NoError(t, err)
	require.Contains(t, c.agentConfigs, "plugsum")

	p, err := c.buildPrompt(config.AgentOrchestrator, c.agentConfigs[config.AgentOrchestrator])
	require.NoError(t, err)
	built, err := p.Build(t.Context(), reloadTestProvider, reloadTestModel, c.cfg)
	require.NoError(t, err)

	require.Contains(t, built, "@helper")
	require.NotContains(t, built, "plugsum")
	require.NotContains(t, built, BuiltinBranchSummarizer)
}

func TestBuiltinBranchSummarizer(t *testing.T) {
	t.Parallel()

	mds, err := discoverAgentMDs(agentMDFS, nil, nil)
	require.NoError(t, err)

	md, ok := mds[BuiltinBranchSummarizer]
	require.True(t, ok)
	require.True(t, md.BranchSummarizer)
}
