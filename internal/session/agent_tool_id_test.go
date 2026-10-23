package session_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/session"
)

func TestAgentToolSessionID_RoundTrips(t *testing.T) {
	t.Parallel()

	id := session.AgentToolSessionID("msg-1", "call-1")

	require.Equal(t, "msg-1$$call-1", id)
	messageID, toolCallID, ok := session.ParseAgentToolSessionID(id)
	require.True(t, ok)
	require.Equal(t, "msg-1", messageID)
	require.Equal(t, "call-1", toolCallID)
}

func TestParseAgentToolSessionID_RejectsOtherIDs(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"plain uuid":     "4b7f7c2e-8a1d-4c55-9f0e-1f2a3b4c5d6e",
		"two separators": "a$$b$$c",
	}
	for name, id := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			messageID, toolCallID, ok := session.ParseAgentToolSessionID(id)
			require.False(t, ok)
			require.Empty(t, messageID)
			require.Empty(t, toolCallID)
		})
	}
}
