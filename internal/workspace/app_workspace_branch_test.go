package workspace_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/agent"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/stretchr/testify/require"
)

func TestAppWorkspaceBranchPreservesPayload(t *testing.T) {
	f := branchfixture.New(t)
	s, err := f.Workspace.CreateSession(f.Context, "source")
	require.NoError(t, err)
	source, err := f.Messages.Create(f.Context, s.ID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)
	raw := "  raw prompt\n\n"
	binary := []byte{0, 1, 2, 255}
	count := 0
	var accepted message.Message
	err = f.Workspace.AgentRunFromMessage(f.Context, s.ID, raw, agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}, OnUserMessageCreated: func(m message.Message) { count++; accepted = m }}, message.Attachment{FilePath: "not-read.png", FileName: "not-read.png", MimeType: "image/png", Content: binary})
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
	require.Equal(t, 1, count)
	require.Equal(t, raw, accepted.Content().Text)
	require.Equal(t, binary, accepted.Parts[1].(message.BinaryContent).Data)
	persisted, err := f.Messages.Get(f.Context, accepted.ID)
	require.NoError(t, err)
	require.Equal(t, binary, persisted.Parts[1].(message.BinaryContent).Data)
	require.NotEmpty(t, persisted.FinishPart())
	require.NoError(t, f.Workspace.MoveLeaf(f.Context, s.ID, source.ID))
	err = f.Workspace.AgentRunFromMessage(f.Context, s.ID, "", agent.BranchRunOptions{Origin: agent.BranchOrigin{TargetMessageID: source.ID, ExpectedSourceLeafID: source.ID}}, message.Attachment{FilePath: "not-read.txt", MimeType: "text/plain", Content: []byte("attachment-only prompt")})
	require.NoError(t, err)
	f.Coordinator.WaitBackgroundJobs()
}
