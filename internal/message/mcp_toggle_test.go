package message

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPToggle_FilterMetadataMessage(t *testing.T) {
	t.Parallel()

	msg := Message{
		MessageType: MessageTypeMCPToggle,
		Parts:       []ContentPart{MCPToggleContent{ServerName: "test-server", Enabled: true}},
	}
	result := FilterMetadataMessage(msg)
	require.Nil(t, result)
}

func TestMCPToggle_ContentJSON(t *testing.T) {
	t.Parallel()

	original := MCPToggleContent{ServerName: "my-server", Enabled: true}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded MCPToggleContent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	require.Equal(t, original, decoded)

	// Verify snake_case JSON field names.
	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	require.Contains(t, raw, "server_name")
	require.Contains(t, raw, "enabled")
}

func TestMCPToggle_PartsRoundTrip(t *testing.T) {
	t.Parallel()

	original := []ContentPart{
		MCPToggleContent{ServerName: "mcp-github", Enabled: false},
	}

	data, err := marshalParts(original)
	require.NoError(t, err)

	parts, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Len(t, parts, 1)

	toggle, ok := parts[0].(MCPToggleContent)
	require.True(t, ok)
	require.Equal(t, "mcp-github", toggle.ServerName)
	require.False(t, toggle.Enabled)
}
