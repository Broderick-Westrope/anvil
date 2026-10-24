package tools_test

import (
	"sync"
	"testing"

	"charm.land/fantasy/schema"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools"
	"github.com/Broderick-Westrope/anvil/internal/agent/tools/mcp"
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// prepare does what fantasy's prepareTools does with each tool's Info on
// every request: wrap the parameters and normalise them in place.
func prepare(tool *tools.Tool) {
	info := tool.Info()
	schema.Normalize(map[string]any{
		"type":       "object",
		"properties": info.Parameters,
		"required":   info.Required,
	})
}

func TestMCPToolParametersAreSafeToNormaliseConcurrently(t *testing.T) {
	const server = "race-normalise"
	mcp.SetToolsForTest(server, &mcp.Tool{
		Name: "lookup",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": []any{"string", "null"}},
				"tags":  map[string]any{"type": "array"},
			},
		},
	})
	t.Cleanup(func() { mcp.DeleteToolsForTest(server) })
	cfg := config.NewTestStore(&config.Config{})

	find := func() *tools.Tool {
		for _, tool := range tools.GetMCPTools(nil, cfg, t.TempDir()) {
			if tool.MCP() == server {
				return tool
			}
		}
		t.Fatal("no wrapper for the registered tool")
		return nil
	}
	shared := find()
	// The orchestrator's and a subagent's wrappers come from separate
	// GetMCPTools calls but share one registry entry.
	wrappers := []*tools.Tool{shared, shared, find(), find()}

	var wg sync.WaitGroup
	for _, wrapper := range wrappers {
		wg.Go(func() { prepare(wrapper) })
	}
	wg.Wait()

	require.Equal(t, shared.Info().Parameters, find().Info().Parameters)
	require.NotContains(t, shared.InputSchema().(map[string]any)["properties"].(map[string]any)["query"], "anyOf")
}
