package message_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newTestService creates an in-memory SQLite database, runs all
// migrations, and returns a ready-to-use message.Service and a session
// ID that can be used to attach messages.
func newTestService(t *testing.T) (message.Service, string) {
	t.Helper()

	ctx := context.Background()
	dataDir := t.TempDir()

	// Connect runs goose migrations automatically.
	sqlDB, err := db.Connect(ctx, dataDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Release(dataDir)
	})

	q := db.New(sqlDB)

	// Create a session so that foreign-key constraints are satisfied.
	sess, err := q.CreateSession(ctx, db.CreateSessionParams{
		ID:               uuid.New().String(),
		ParentSessionID:  sql.NullString{},
		Title:            "test session",
		MessageCount:     0,
		PromptTokens:     0,
		CompletionTokens: 0,
		Cost:             0,
	})
	require.NoError(t, err)

	svc := message.NewService(q, message.WithConn(sqlDB))
	return svc, sess.ID
}

// createMsg is a helper that creates a message with the given text and
// optional parent, returning the created Message.
func createMsg(t *testing.T, svc message.Service, sessionID, text, parentID string) message.Message {
	t.Helper()

	msg, err := svc.Create(context.Background(), sessionID, message.CreateMessageParams{
		Role:            message.User,
		Parts:           []message.ContentPart{message.TextContent{Text: text}},
		ParentMessageID: parentID,
	})
	require.NoError(t, err)
	return msg
}

// extractIDs returns only the IDs from a slice of messages, preserving
// order.
func extractIDs(msgs []message.Message) []string {
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func TestGetBranchPath_SingleMessage(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	root := createMsg(t, svc, sessionID, "root", "")

	path, err := svc.GetBranchPath(ctx, root.ID)
	require.NoError(t, err)
	require.Equal(t, []string{root.ID}, extractIDs(path))
}

func TestGetBranchPath_LinearChain(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	// Build a chain: m1 → m2 → m3 → m4 → m5.
	m1 := createMsg(t, svc, sessionID, "msg 1", "")
	m2 := createMsg(t, svc, sessionID, "msg 2", m1.ID)
	m3 := createMsg(t, svc, sessionID, "msg 3", m2.ID)
	m4 := createMsg(t, svc, sessionID, "msg 4", m3.ID)
	m5 := createMsg(t, svc, sessionID, "msg 5", m4.ID)

	path, err := svc.GetBranchPath(ctx, m5.ID)
	require.NoError(t, err)

	// Expect root-to-leaf order (sorted by created_at ASC).
	require.Equal(t, []string{m1.ID, m2.ID, m3.ID, m4.ID, m5.ID}, extractIDs(path))
}

func TestGetBranchPath_BranchedTree(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	// Tree structure:
	//   A (root)
	//   ├── B
	//   │   └── C
	//   └── D
	//       └── E
	a := createMsg(t, svc, sessionID, "A", "")
	b := createMsg(t, svc, sessionID, "B", a.ID)
	c := createMsg(t, svc, sessionID, "C", b.ID)
	d := createMsg(t, svc, sessionID, "D", a.ID)
	e := createMsg(t, svc, sessionID, "E", d.ID)

	// Walking from C should yield [A, B, C].
	pathC, err := svc.GetBranchPath(ctx, c.ID)
	require.NoError(t, err)
	require.Equal(t, []string{a.ID, b.ID, c.ID}, extractIDs(pathC))

	// Walking from E should yield [A, D, E].
	pathE, err := svc.GetBranchPath(ctx, e.ID)
	require.NoError(t, err)
	require.Equal(t, []string{a.ID, d.ID, e.ID}, extractIDs(pathE))
}

func TestGetBranchPath_NonExistentID(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t)
	ctx := context.Background()

	// A non-existent ID should produce an empty slice, not an error, because
	// the CTE simply finds no matching rows.
	path, err := svc.GetBranchPath(ctx, "does-not-exist")
	require.NoError(t, err)
	require.Empty(t, path)
}

func TestGetBranchPath_EmptyID(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t)
	ctx := context.Background()

	// An empty leaf ID matches nothing in the CTE.
	path, err := svc.GetBranchPath(ctx, "")
	require.NoError(t, err)
	require.Empty(t, path)
}

func TestGetBranchPathTail_Limit(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	// Branch: root → a → b → c, plus a sibling branch off a.
	root := createMsg(t, svc, sessionID, "root", "")
	a := createMsg(t, svc, sessionID, "A", root.ID)
	b := createMsg(t, svc, sessionID, "B", a.ID)
	c := createMsg(t, svc, sessionID, "C", b.ID)
	sibling := createMsg(t, svc, sessionID, "sibling", a.ID)
	_ = createMsg(t, svc, sessionID, "sibling child", sibling.ID)

	// limit=2 from leaf C returns exactly [B, C], oldest-first.
	tail, err := svc.GetBranchPathTail(ctx, c.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []string{b.ID, c.ID}, extractIDs(tail))
}

func TestGetBranchPathTail_Paging(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	root := createMsg(t, svc, sessionID, "root", "")
	a := createMsg(t, svc, sessionID, "A", root.ID)
	b := createMsg(t, svc, sessionID, "B", a.ID)
	c := createMsg(t, svc, sessionID, "C", b.ID)

	// First page from leaf C.
	page1, err := svc.GetBranchPathTail(ctx, c.ID, 2)
	require.NoError(t, err)
	require.Equal(t, []string{b.ID, c.ID}, extractIDs(page1))

	// Page back: the oldest returned message's parent is the next leaf.
	page2, err := svc.GetBranchPathTail(ctx, page1[0].ParentMessageID, 2)
	require.NoError(t, err)
	require.Equal(t, []string{root.ID, a.ID}, extractIDs(page2))
}

func TestGetBranchPathTail_ExcludesSiblingBranches(t *testing.T) {
	t.Parallel()

	svc, sessionID := newTestService(t)
	ctx := context.Background()

	root := createMsg(t, svc, sessionID, "root", "")
	a := createMsg(t, svc, sessionID, "A", root.ID)
	b := createMsg(t, svc, sessionID, "B", a.ID)
	c := createMsg(t, svc, sessionID, "C", b.ID)
	sibling := createMsg(t, svc, sessionID, "sibling", a.ID)
	siblingChild := createMsg(t, svc, sessionID, "sibling child", sibling.ID)

	// A generous limit walks the full branch; sibling messages must
	// never appear.
	tail, err := svc.GetBranchPathTail(ctx, c.ID, 100)
	require.NoError(t, err)
	require.Equal(t, []string{root.ID, a.ID, b.ID, c.ID}, extractIDs(tail))
	require.NotContains(t, extractIDs(tail), sibling.ID)
	require.NotContains(t, extractIDs(tail), siblingChild.ID)
}

func TestGetBranchPathTail_UnknownLeaf(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t)
	ctx := context.Background()

	// A non-existent leaf ID matches nothing in the CTE.
	tail, err := svc.GetBranchPathTail(ctx, "does-not-exist", 5)
	require.NoError(t, err)
	require.Empty(t, tail)
}

func TestGetBranchPathTail_EmptyLeaf(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t)
	ctx := context.Background()

	// An empty leaf ID matches nothing in the CTE.
	tail, err := svc.GetBranchPathTail(ctx, "", 5)
	require.NoError(t, err)
	require.Empty(t, tail)
}
