package workspace_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/app"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/testutil/branchfixture"
	"github.com/Broderick-Westrope/anvil/internal/workspace"
)

// Without a coordinator there's no queue to wait behind, so the entry is
// written straight onto the leaf.
func TestWriteMetadataEntryWithoutACoordinatorWritesOnTheLeaf(t *testing.T) {
	f := branchfixture.New(t)
	ctx := f.Context
	ws := workspace.NewAppWorkspace(&app.App{Sessions: f.Sessions, Messages: f.Messages}, f.Config)
	sess, err := f.Workspace.CreateSession(ctx, "s")
	require.NoError(t, err)
	params := func(parent string) message.CreateMessageParams {
		return message.CreateMessageParams{
			Role: message.System, MessageType: message.MessageTypeModelChange, ParentMessageID: parent,
			Parts: []message.ContentPart{message.ModelChangeContent{ModelID: "m"}},
		}
	}

	require.NoError(t, ws.WriteMetadataEntry(ctx, sess.ID, params("")))
	written, err := f.Messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, written, 1)
	require.Equal(t, message.MessageTypeModelChange, written[0].MessageType)
	got, err := f.Sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, written[0].ID, got.LeafMessageID)

	require.Error(t, ws.WriteMetadataEntry(ctx, sess.ID, params(written[0].ID)),
		"the leaf decides the parent, not the caller")
	after, err := f.Messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, after, 1, "nothing was written")
}
