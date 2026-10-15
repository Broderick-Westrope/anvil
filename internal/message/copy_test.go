package message_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
)

func newSession(t *testing.T, q db.Querier) string {
	t.Helper()
	s, err := q.CreateSession(t.Context(), db.CreateSessionParams{ID: uuid.NewString(), Title: "fork"})
	require.NoError(t, err)
	return s.ID
}

func add(t *testing.T, svc message.Service, sessionID, parent string, role message.MessageRole, parts ...message.ContentPart) message.Message {
	t.Helper()
	m, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: role, ParentMessageID: parent, Parts: parts})
	require.NoError(t, err)
	return m
}

func TestCopyBranchCopiesTheBranchIntoAnotherSession(t *testing.T) {
	t.Parallel()

	svc, q, source := newTestService(t)
	u1 := add(t, svc, source, "", message.User, message.TextContent{Text: "hello"})
	a1 := add(t, svc, source, u1.ID, message.Assistant, message.TextContent{Text: "hi"}, message.Finish{Reason: message.FinishReasonEndTurn})
	// A branch that isn't on the copied path.
	add(t, svc, source, u1.ID, message.Assistant, message.TextContent{Text: "other branch"}, message.Finish{Reason: message.FinishReasonEndTurn})
	compaction, err := svc.Create(t.Context(), source, message.CreateMessageParams{
		Role: message.User, ParentMessageID: a1.ID, MessageType: message.MessageTypeCompaction,
		Parts: []message.ContentPart{message.CompactionContent{Summary: "so far", FirstKeptEntryID: a1.ID}},
	})
	require.NoError(t, err)
	u2 := add(t, svc, source, compaction.ID, message.User, message.TextContent{Text: "build it"})

	fork := newSession(t, q)
	leaf, err := svc.CopyBranch(t.Context(), u2.ID, fork)
	require.NoError(t, err)

	copied, err := svc.GetBranchPath(t.Context(), leaf)
	require.NoError(t, err)
	original, err := svc.GetBranchPath(t.Context(), u2.ID)
	require.NoError(t, err)
	require.Len(t, copied, len(original))
	for i := range original {
		require.NotEqual(t, original[i].ID, copied[i].ID)
		require.Equal(t, fork, copied[i].SessionID)
		require.Equal(t, original[i].Role, copied[i].Role)
		require.Equal(t, original[i].MessageType, copied[i].MessageType)
		require.Equal(t, original[i].CreatedAt, copied[i].CreatedAt)
		if original[i].MessageType != message.MessageTypeCompaction {
			require.Equal(t, original[i].Parts, copied[i].Parts, "parts are copied as they are")
		}
	}
	kept := copied[2].Parts[0].(message.CompactionContent).FirstKeptEntryID
	require.Equal(t, copied[1].ID, kept, "the compaction points at the copy of its first kept message")

	session, err := q.GetSessionByID(t.Context(), fork)
	require.NoError(t, err)
	require.Equal(t, leaf, session.LeafMessageID.String)

	sourceSession, err := q.GetSessionByID(t.Context(), source)
	require.NoError(t, err)
	require.Equal(t, u2.ID, sourceSession.LeafMessageID.String, "the source is left as it was")
}

func TestCopyBranchLeavesOutAnExchangeItEndsInTheMiddleOf(t *testing.T) {
	t.Parallel()

	tests := map[string]func(svc message.Service, sessionID, after string) message.Message{
		"unfinished reply": func(svc message.Service, sessionID, after string) message.Message {
			return add(t, svc, sessionID, after, message.Assistant, message.TextContent{Text: "half a th"})
		},
		"tool call without its result": func(svc message.Service, sessionID, after string) message.Message {
			return add(t, svc, sessionID, after, message.Assistant,
				message.ToolCall{ID: "c1", Name: "view", Input: "{}", Finished: true}, message.Finish{Reason: message.FinishReasonToolUse})
		},
		"tool result with no reply yet": func(svc message.Service, sessionID, after string) message.Message {
			call := add(t, svc, sessionID, after, message.Assistant,
				message.ToolCall{ID: "c1", Name: "view", Input: "{}", Finished: true}, message.Finish{Reason: message.FinishReasonToolUse})
			return add(t, svc, sessionID, call.ID, message.Tool, message.ToolResult{ToolCallID: "c1", Content: "ok"})
		},
	}
	for name, unfinished := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			svc, q, source := newTestService(t)
			u := add(t, svc, source, "", message.User, message.TextContent{Text: "hello"})
			tip := unfinished(svc, source, u.ID)

			fork := newSession(t, q)
			leaf, err := svc.CopyBranch(t.Context(), tip.ID, fork)
			require.NoError(t, err)
			copied, err := svc.GetBranchPath(t.Context(), leaf)
			require.NoError(t, err)
			require.Len(t, copied, 1)
			require.Equal(t, message.User, copied[0].Role)
		})
	}
}

func TestCopyBranchOfNothingCopiesNothing(t *testing.T) {
	t.Parallel()

	svc, q, _ := newTestService(t)
	leaf, err := svc.CopyBranch(t.Context(), "", newSession(t, q))
	require.NoError(t, err)
	require.Empty(t, leaf)
}

func TestCopyBranchKeepsPartsItCantReadWhileRemappingReferences(t *testing.T) {
	t.Parallel()

	svc, q, source := newTestService(t)
	kept := add(t, svc, source, "", message.User, message.TextContent{Text: "kept"})
	compaction, err := svc.Create(t.Context(), source, message.CreateMessageParams{
		Role: message.User, ParentMessageID: kept.ID, MessageType: message.MessageTypeCompaction,
		Parts: []message.ContentPart{message.CompactionContent{Summary: "s", FirstKeptEntryID: kept.ID}},
	})
	require.NoError(t, err)
	// A newer Anvil wrote a part this version doesn't know beside the
	// compaction.
	raw := `[{"type":"from_the_future","data":{"x":1}},{"type":"compaction","data":{"summary":"s","first_kept_entry_id":"` + kept.ID + `","tokens_before":0}}]`
	require.NoError(t, q.UpdateMessage(t.Context(), db.UpdateMessageParams{ID: compaction.ID, Parts: raw}))

	fork := newSession(t, q)
	leaf, err := svc.CopyBranch(t.Context(), compaction.ID, fork)
	require.NoError(t, err)
	stored, err := q.GetMessage(t.Context(), leaf)
	require.NoError(t, err)
	require.Contains(t, stored.Parts, `{"type":"from_the_future","data":{"x":1}}`, "the unknown part survives the copy")
	copied, err := svc.GetBranchPath(t.Context(), leaf)
	require.NoError(t, err)
	require.Equal(t, copied[0].ID, copied[1].Parts[0].(message.CompactionContent).FirstKeptEntryID)
}
