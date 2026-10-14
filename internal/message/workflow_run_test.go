package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowRunMarkerStaysOutOfModelContext(t *testing.T) {
	t.Parallel()

	msg := Message{
		MessageType: MessageTypeWorkflowRun,
		Parts:       []ContentPart{WorkflowRunContent{RunID: "run-1", Command: "build"}},
	}
	require.Nil(t, FilterMetadataMessage(msg))
}

func TestWorkflowRunContentRoundTrips(t *testing.T) {
	t.Parallel()

	original := WorkflowRunContent{RunID: "run-1", Command: "build", Input: "track HIP downloads"}
	data, err := marshalParts([]ContentPart{original})
	require.NoError(t, err)
	require.JSONEq(t, `[{"type":"workflow_run","data":{"run_id":"run-1","command":"build","input":"track HIP downloads"}}]`, string(data))

	parts, err := unmarshalParts(data)
	require.NoError(t, err)
	require.Equal(t, []ContentPart{original}, parts)
}
