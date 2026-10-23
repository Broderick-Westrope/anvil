package mcp

import (
	"encoding/json"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

func nullableTool(name string) *Tool {
	return &Tool{
		Name:        name,
		Description: "Looks things up.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": []any{"string", "null"}},
				"tags":  map[string]any{"type": "array"},
			},
			"required": []any{"query"},
		},
	}
}

func TestUpdateToolsNormalisesACopyOfTheParameters(t *testing.T) {
	const server = "registry-normalise"
	t.Cleanup(func() { allTools.Del(server) })
	raw := nullableTool("lookup")
	before, err := json.Marshal(raw.InputSchema)
	require.NoError(t, err)

	updateTools(config.NewTestStore(&config.Config{}), server, []*Tool{raw})

	registered := ServerTools(server)
	require.Len(t, registered, 1)
	require.Equal(t, []string{"query"}, registered[0].Required)
	require.Equal(t, map[string]any{
		"query": map[string]any{"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "null"},
		}},
		"tags": map[string]any{"type": "array", "items": map[string]any{}},
	}, registered[0].Parameters)

	after, err := json.Marshal(registered[0].InputSchema)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "the raw schema must stay as the server sent it")
}

func TestSentToolBytesMeasuresWhatReachesTheModel(t *testing.T) {
	const server = "registry-size"
	t.Cleanup(func() { allTools.Del(server) })
	tool := &Tool{
		Name:        "ping",
		Description: "Replies pong.",
		InputSchema: map[string]any{"type": "object"},
		Meta:        map[string]any{"ignored": "never sent"},
		Title:       "never sent",
	}
	updateTools(config.NewTestStore(&config.Config{}), server, []*Tool{tool})

	want := `{"name":"mcp_registry-size_ping","description":"Replies pong.","parameters":{},"required":[]}`
	require.Equal(t, len(want), SentToolBytes(server))
}

func TestSentToolBytesIsZeroForAnUnknownServer(t *testing.T) {
	require.Zero(t, SentToolBytes("registry-nothing"))
}
