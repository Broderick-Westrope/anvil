package message

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/session"
)

// TestUserMessageQueriesAreBounded pins the prompt-history depth at 200.
func TestUserMessageQueriesAreBounded(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	q := db.New(conn)
	workingDir := t.TempDir()
	sess, err := session.NewService(q, conn).Create(t.Context(), "test", workingDir)
	require.NoError(t, err)

	svc := NewService(q, WithConn(conn))
	for range promptHistoryProbeCount {
		_, err := svc.Create(t.Context(), sess.ID, CreateMessageParams{
			Role:  User,
			Parts: []ContentPart{TextContent{Text: "prompt"}},
		})
		require.NoError(t, err)
	}

	t.Run("per session", func(t *testing.T) {
		t.Parallel()

		got, err := svc.ListUserMessages(t.Context(), sess.ID)
		require.NoError(t, err)
		require.Len(t, got, 200)
		for _, m := range got {
			require.Equal(t, User, m.Role)
		}
	})

	t.Run("by working dir", func(t *testing.T) {
		t.Parallel()

		got, err := svc.ListUserMessagesByWorkingDir(t.Context(), workingDir)
		require.NoError(t, err)
		require.Len(t, got, 200)
		for _, m := range got {
			require.Equal(t, User, m.Role)
		}
	})
}

// Past the 200 the queries return, so a missing LIMIT shows up.
const promptHistoryProbeCount = 250
