package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPartsRoundTrip pins the on-disk part encoding: every part type must
// survive a trip through marshalParts unchanged.
func TestPartsRoundTrip(t *testing.T) {
	t.Parallel()

	parts := []ContentPart{
		ReasoningContent{Thinking: "hmm", Signature: "sig", ThoughtSignature: "tsig", ToolID: "t1", StartedAt: 1, FinishedAt: 2},
		TextContent{Text: "hello"},
		ImageURLContent{URL: "https://example.test/a.png", Detail: "high"},
		BinaryContent{Path: "/tmp/a.bin", MIMEType: "application/octet-stream", Data: []byte{0, 1, 2}},
		ToolCall{ID: "c1", Name: "bash", Input: `{"cmd":"ls"}`, ProviderExecuted: true, Finished: true},
		ToolResult{ToolCallID: "c1", Name: "bash", Content: "out", Data: "d", MIMEType: "text/plain", Metadata: "{}", IsError: true},
		Finish{Reason: FinishReasonEndTurn, Time: 42, Message: "done", Details: "more"},
		CompactionContent{Summary: "sum", FirstKeptEntryID: "m1", TokensBefore: 1000},
		BranchSummaryContent{Summary: "branch", FromID: "m2"},
		LabelContent{TargetID: "m3", Label: "checkpoint"},
		ModelChangeContent{Provider: "anthropic", ModelID: "claude"},
		ThinkingLevelChangeContent{ThinkingLevel: "high"},
		MCPToggleContent{ServerName: "terminal", Enabled: true},
	}

	encoded, err := marshalParts(parts)
	require.NoError(t, err)

	decoded, err := unmarshalParts(encoded)
	require.NoError(t, err)
	require.Equal(t, parts, decoded)
}

func TestUnmarshalPartsEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty array yields no parts", func(t *testing.T) {
		t.Parallel()

		got, err := unmarshalParts([]byte(`[]`))
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("unknown type is skipped", func(t *testing.T) {
		t.Parallel()

		// A newer Anvil sharing the database may write parts this one
		// doesn't know; the rest of the message still reads.
		got, err := unmarshalParts([]byte(`[{"type":"nope","data":{}},{"type":"text","data":{"text":"hi"}}]`))
		require.NoError(t, err)
		require.Equal(t, []ContentPart{TextContent{Text: "hi"}}, got)
	})

	t.Run("payload may precede its type tag", func(t *testing.T) {
		t.Parallel()

		// JSON does not guarantee key order.
		got, err := unmarshalParts([]byte(`[{"data":{"text":"hi"},"type":"text"}]`))
		require.NoError(t, err)
		require.Equal(t, []ContentPart{TextContent{Text: "hi"}}, got)
	})

	t.Run("malformed json is an error", func(t *testing.T) {
		t.Parallel()

		_, err := unmarshalParts([]byte(`{`))
		require.Error(t, err)
	})
}
