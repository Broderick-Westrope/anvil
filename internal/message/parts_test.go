package message_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestStoredParts_RoundTripEveryPartType(t *testing.T) {
	t.Parallel()

	svc, _, sessionID := newTestService(t)
	parts := []message.ContentPart{
		message.ReasoningContent{
			Thinking:         "thinking",
			Signature:        "sig",
			ThoughtSignature: "thought",
			ToolID:           "tool",
			StartedAt:        1,
			FinishedAt:       2,
		},
		message.TextContent{Text: "text"},
		message.ImageURLContent{URL: "https://example.com/a.png", Detail: "high"},
		message.BinaryContent{Path: "a.png", MIMEType: "image/png", Data: []byte{1, 2, 3}},
		message.ToolCall{ID: "call", Name: "bash", Input: "{}", ProviderExecuted: true, Finished: true, StartedAt: 1_000},
		message.ToolResult{ToolCallID: "call", Name: "bash", Content: "out", Data: "ZGF0YQ==", MIMEType: "text/plain", Metadata: "{}", IsError: true, FinishedAt: 2_000},
		message.CompactionContent{Summary: "sum", FirstKeptEntryID: "kept", TokensBefore: 10},
		message.BranchSummaryContent{
			Summary: "branch", FromID: "from", ChildSessionID: "child", Agent: "branch-summarizer",
			Ledger: message.BranchLedger{
				Modified: []string{"a.go"}, Read: []string{"b.go"},
				Events: []message.LedgerEvent{{Tool: "bash", Command: "git commit -m x", Dir: "/repo", Status: message.LedgerSucceeded, Refs: []string{"abc1234"}}},
			},
		},
		message.LabelContent{TargetID: "target", Label: "label"},
		message.ModelChangeContent{Provider: "anthropic", ModelID: "model"},
		message.ThinkingLevelChangeContent{ThinkingLevel: "high"},
		message.MCPToggleContent{ServerName: "server", Enabled: true},
		message.Finish{Reason: message.FinishReasonEndTurn, Time: 5, Message: "msg", Details: "details"},
	}

	created, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: parts,
	})
	require.NoError(t, err)
	require.Equal(t, parts, created.Parts)

	got, err := svc.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, parts, got.Parts)
}

func TestStoredParts_EmptyArrayDecodesToEmptyParts(t *testing.T) {
	t.Parallel()

	svc, q, sessionID := newTestService(t)
	created, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Assistant})
	require.NoError(t, err)
	require.NoError(t, q.UpdateMessage(t.Context(), db.UpdateMessageParams{ID: created.ID, Parts: "[]"}))

	got, err := svc.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Parts)
	require.Empty(t, got.Parts)
}

func TestStoredParts_RejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		raw     string
		wantErr string
	}{
		"not an array":         {raw: `{}`, wantErr: "cannot unmarshal object"},
		"part not an object":   {raw: `[1]`, wantErr: "cannot unmarshal number"},
		"data of wrong shape":  {raw: `[{"type":"text","data":5}]`, wantErr: "cannot unmarshal number"},
		"unknown part type":    {raw: `[{"type":"bogus","data":{}}]`, wantErr: "unknown part type: bogus"},
		"error after good one": {raw: `[{"type":"text","data":{"text":"ok"}},{"type":"finish","data":"x"}]`, wantErr: "cannot unmarshal string"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, q, sessionID := newTestService(t)
			created, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Assistant})
			require.NoError(t, err)
			require.NoError(t, q.UpdateMessage(t.Context(), db.UpdateMessageParams{
				ID:    created.ID,
				Parts: tc.raw,
			}))

			_, err = svc.Get(t.Context(), created.ID)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}
