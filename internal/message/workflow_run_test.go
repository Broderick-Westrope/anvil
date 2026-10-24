package message_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/message"
)

func TestWorkflowRunMarkerIsStoredAndKeptOutOfModelContext(t *testing.T) {
	t.Parallel()

	svc, _, sessionID := newTestService(t)
	marker := message.WorkflowRunContent{RunID: "run-1", Command: "build", Input: "track HIP downloads"}
	created, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{
		Role: message.System, MessageType: message.MessageTypeWorkflowRun, Parts: []message.ContentPart{marker},
	})
	require.NoError(t, err)

	got, err := svc.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, []message.ContentPart{marker}, got.Parts, "a marker gets no Finish part and round-trips as stored")
	require.Nil(t, message.FilterMetadataMessage(got))
}
