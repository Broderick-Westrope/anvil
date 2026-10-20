package db

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUserMessagesUseSessionRoleIndex pins why
// idx_messages_session_role_created_at exists: prompt history seeks to a
// session's user rows and reads them back already ordered.
func TestUserMessagesUseSessionRoleIndex(t *testing.T) {
	t.Parallel()

	conn, err := Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	queryPlan := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		rows, err := conn.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
		require.NoError(t, err)
		defer rows.Close()
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
			plan.WriteString(detail)
			plan.WriteString("\n")
		}
		require.NoError(t, rows.Err())
		return plan.String()
	}

	t.Run("per session", func(t *testing.T) {
		t.Parallel()

		got := queryPlan(t, listUserMessagesBySession, "session")
		require.Contains(t, got, "idx_messages_session_role_created_at")
		require.NotContains(t, got, "TEMP B-TREE",
			"the index covers the ordering, so no sort should be needed")
	})

	t.Run("by working dir", func(t *testing.T) {
		t.Parallel()

		got := queryPlan(t, listUserMessagesByWorkingDir, "/dir")
		require.Contains(t, got, "COVERING INDEX idx_messages_session_role_created_at",
			"the newest-first sort must run on the index, never on message parts")
	})
}
