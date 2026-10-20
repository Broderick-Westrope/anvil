package message_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
)

func TestCreateAtLeaf(t *testing.T) {
	t.Parallel()

	svc, _, sessionID := newTestService(t)
	a, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)

	b, err := svc.CreateAtLeaf(t.Context(), sessionID, a.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
	require.NoError(t, err)
	require.Equal(t, a.ID, b.ParentMessageID)

	_, err = svc.CreateAtLeaf(t.Context(), sessionID, a.ID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
	require.ErrorIs(t, err, message.ErrLeafMoved)
	children, err := svc.GetChildren(t.Context(), a.ID)
	require.NoError(t, err)
	require.Len(t, children, 1, "nothing is stored when the leaf moved")
}
