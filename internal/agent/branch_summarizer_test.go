package agent

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent/prompt"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOrchestratorBlocksOmitBranchSummarizers(t *testing.T) {
	t.Parallel()

	mds := map[string]prompt.AgentMD{
		"fixer":      {Name: "fixer", Role: "Fixes things", RoutingHint: "Route fixes to @fixer."},
		"summarizer": {Name: "summarizer", Role: "Summarizes branches", RoutingHint: "Route to @summarizer.", BranchSummarizer: true},
	}
	cfgs := map[string]config.Agent{"fixer": {ID: "fixer"}, "summarizer": {ID: "summarizer"}}

	agents, workflow := buildOrchestratorBlocksFromState(mds, cfgs)

	require.Contains(t, agents, "@fixer")
	require.NotContains(t, agents, "summarizer")
	require.NotContains(t, workflow, "summarizer")
}

func TestBuiltinBranchSummarizer(t *testing.T) {
	t.Parallel()

	mds, err := discoverAgentMDs(agentMDFS, nil, nil)
	require.NoError(t, err)

	md, ok := mds[BuiltinBranchSummarizer]
	require.True(t, ok)
	require.True(t, md.BranchSummarizer)
}
