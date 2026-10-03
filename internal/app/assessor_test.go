package app

import (
	"bytes"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/stretchr/testify/require"
)

type intentFixture struct {
	conn     *sql.DB
	sessions session.Service
	messages message.Service
	nextTime int64
}

func newIntentFixture(t *testing.T) *intentFixture {
	t.Helper()
	dataDir := t.TempDir()
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Release(dataDir) })
	q := db.New(conn)
	return &intentFixture{
		conn:     conn,
		sessions: session.NewService(q, conn),
		messages: message.NewService(q, message.WithConn(conn)),
		nextTime: 1_700_000_000,
	}
}

// add creates a message under parentID with a created_at one second after
// the previous message, so ordering by time and by branch can diverge.
func (f *intentFixture) add(t *testing.T, sessionID string, role message.MessageRole, text, parentID string) message.Message {
	t.Helper()
	msg, err := f.messages.Create(t.Context(), sessionID, message.CreateMessageParams{
		Role:            role,
		Parts:           []message.ContentPart{message.TextContent{Text: text}},
		ParentMessageID: parentID,
	})
	require.NoError(t, err)
	f.nextTime++
	_, err = f.conn.ExecContext(t.Context(), "UPDATE messages SET created_at = ? WHERE id = ?", f.nextTime, msg.ID)
	require.NoError(t, err)
	return msg
}

func (f *intentFixture) source() *intentSource {
	return &intentSource{sessions: f.sessions, messages: f.messages}
}

func TestIntentSource_ChildResolvesToParentActiveBranch(t *testing.T) {
	t.Parallel()
	f := newIntentFixture(t)
	ctx := t.Context()

	parent, err := f.sessions.Create(ctx, "parent", t.TempDir())
	require.NoError(t, err)

	u1 := f.add(t, parent.ID, message.User, "u1", "")
	a1 := f.add(t, parent.ID, message.Assistant, "a1", u1.ID)
	u2 := f.add(t, parent.ID, message.User, "u2", a1.ID)
	a2 := f.add(t, parent.ID, message.Assistant, "a2", u2.ID)
	u3 := f.add(t, parent.ID, message.User, "u3", a2.ID)
	a3 := f.add(t, parent.ID, message.Assistant, "a3", u3.ID)
	u4 := f.add(t, parent.ID, message.User, "u4", a3.ID)
	a4 := f.add(t, parent.ID, message.Assistant, "a4", u4.ID)
	// A sibling branch created after u4: the newest user message by time,
	// but not on the active leaf path.
	u5 := f.add(t, parent.ID, message.User, "u5", a3.ID)
	f.add(t, parent.ID, message.Assistant, "a5", u5.ID)
	require.NoError(t, f.sessions.MoveLeaf(ctx, parent.ID, a4.ID))

	child, err := f.sessions.CreateTaskSession(ctx, "tool-call-1", parent.ID, "task")
	require.NoError(t, err)

	got, err := f.source().RecentUserMessages(ctx, child.ID, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"u2", "u3", "u4"}, got)

	got, err = f.source().RecentUserMessages(ctx, parent.ID, 10)
	require.NoError(t, err)
	require.Equal(t, []string{"u1", "u2", "u3", "u4"}, got)
}

func TestIntentSource_NoLeafReturnsNil(t *testing.T) {
	t.Parallel()
	f := newIntentFixture(t)

	sess, err := f.sessions.Create(t.Context(), "empty", t.TempDir())
	require.NoError(t, err)

	got, err := f.source().RecentUserMessages(t.Context(), sess.ID, 3)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestIntentSource_UnknownSessionReturnsError(t *testing.T) {
	t.Parallel()
	f := newIntentFixture(t)

	_, err := f.source().RecentUserMessages(t.Context(), "missing", 3)
	require.Error(t, err)
}

func validTrustedAssessor(mode config.AssessorMode) *config.TrustedAssessor {
	return &config.TrustedAssessor{
		Config: &config.PermissionAssessor{
			Mode:  mode,
			URL:   "https://assessor.example.com/v1/systemone",
			Model: "von-1.0.0",
		},
		APIKey: "secret-key-value",
	}
}

func TestBuildAssessorOption_ModeOffStillBuilt(t *testing.T) {
	t.Parallel()
	for _, mode := range []config.AssessorMode{"", config.AssessorOff, config.AssessorShadow, config.AssessorEnforce} {
		opt, ok := buildAssessorOption(validTrustedAssessor(mode), nil, nil)
		require.True(t, ok, "mode %q", mode)
		require.NotNil(t, opt, "mode %q", mode)
	}
}

func TestBuildAssessorOption_SendUserMessagesFalseBuilt(t *testing.T) {
	t.Parallel()
	ta := validTrustedAssessor(config.AssessorShadow)
	off := false
	ta.Config.SendUserMessages = &off
	_, ok := buildAssessorOption(ta, nil, nil)
	require.True(t, ok)
}

func TestBuildAssessorOption_NilNotBuilt(t *testing.T) {
	t.Parallel()
	_, ok := buildAssessorOption(nil, nil, nil)
	require.False(t, ok)
}

// The tests below swap the default slog handler, so they don't run in
// parallel.

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestBuildAssessorOption_EmptyKeyNotBuilt(t *testing.T) {
	buf := captureSlog(t)
	ta := validTrustedAssessor(config.AssessorShadow)
	ta.APIKey = ""

	_, ok := buildAssessorOption(ta, nil, nil)
	require.False(t, ok)
	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "Permission assessor is configured but unusable")
}

func TestBuildAssessorOption_MissingURLOrModelNotBuilt(t *testing.T) {
	buf := captureSlog(t)

	ta := validTrustedAssessor(config.AssessorShadow)
	ta.Config.URL = ""
	_, ok := buildAssessorOption(ta, nil, nil)
	require.False(t, ok)

	ta = validTrustedAssessor(config.AssessorShadow)
	ta.Config.Model = ""
	_, ok = buildAssessorOption(ta, nil, nil)
	require.False(t, ok)

	require.NotContains(t, buf.String(), "secret-key-value")
}

func TestBuildAssessorOption_InvalidMergedThresholdsNotBuilt(t *testing.T) {
	buf := captureSlog(t)
	ta := validTrustedAssessor(config.AssessorEnforce)
	// Valid alone, but not below the default deny_at of 0.9.
	escalate := 0.95
	ta.Config.EscalateAt = &escalate

	_, ok := buildAssessorOption(ta, nil, nil)
	require.False(t, ok)
	require.Contains(t, buf.String(), "Permission assessor thresholds are invalid")
}
