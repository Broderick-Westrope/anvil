package message_test

import (
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/google/uuid"
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

func TestDeleteChildless(t *testing.T) {
	t.Parallel()

	t.Run("moves the leaf back when it was the leaf", func(t *testing.T) {
		t.Parallel()
		svc, q, sessionID := newTestService(t)
		a, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User})
		require.NoError(t, err)
		b, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
		require.NoError(t, err)

		require.NoError(t, svc.DeleteChildless(t.Context(), sessionID, b.ID))

		_, err = svc.Get(t.Context(), b.ID)
		require.Error(t, err)
		sess, err := q.GetSessionByID(t.Context(), sessionID)
		require.NoError(t, err)
		require.Equal(t, a.ID, sess.LeafMessageID.String)
	})
	t.Run("leaves the leaf alone when it is elsewhere", func(t *testing.T) {
		t.Parallel()
		svc, q, sessionID := newTestService(t)
		a, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User})
		require.NoError(t, err)
		b, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
		require.NoError(t, err)
		c, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
		require.NoError(t, err)

		require.NoError(t, svc.DeleteChildless(t.Context(), sessionID, b.ID))

		sess, err := q.GetSessionByID(t.Context(), sessionID)
		require.NoError(t, err)
		require.Equal(t, c.ID, sess.LeafMessageID.String)
	})
	t.Run("refuses a message with children", func(t *testing.T) {
		t.Parallel()
		svc, _, sessionID := newTestService(t)
		a, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User})
		require.NoError(t, err)
		_, err = svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User, ParentMessageID: a.ID})
		require.NoError(t, err)

		err = svc.DeleteChildless(t.Context(), sessionID, a.ID)

		require.ErrorIs(t, err, message.ErrHasChildren)
		_, err = svc.Get(t.Context(), a.ID)
		require.NoError(t, err)
	})
	t.Run("refuses a message from another session", func(t *testing.T) {
		t.Parallel()
		svc, q, sessionID := newTestService(t)
		other, err := q.CreateSession(t.Context(), db.CreateSessionParams{ID: uuid.New().String(), Title: "other"})
		require.NoError(t, err)
		a, err := svc.Create(t.Context(), other.ID, message.CreateMessageParams{Role: message.User})
		require.NoError(t, err)

		err = svc.DeleteChildless(t.Context(), sessionID, a.ID)

		require.ErrorContains(t, err, "belongs to session "+other.ID)
		_, err = svc.Get(t.Context(), a.ID)
		require.NoError(t, err)
	})
}

func TestAppendToLeaf(t *testing.T) {
	t.Parallel()

	svc, _, sessionID := newTestService(t)
	a, err := svc.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.User})
	require.NoError(t, err)

	b, err := svc.AppendToLeaf(t.Context(), sessionID, message.CreateMessageParams{
		Role: message.User, MessageType: message.MessageTypeMCPToggle, ParentMessageID: "stale-parent",
	})

	require.NoError(t, err)
	require.Equal(t, a.ID, b.ParentMessageID, "parented on the leaf read in the same transaction")
}
