package app

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/db"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/Broderick-Westrope/anvil/internal/permission"
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
		setup, ok := buildAssessorOption(validTrustedAssessor(mode), nil, nil)
		require.True(t, ok, "mode %q", mode)
		require.NotNil(t, setup.option, "mode %q", mode)
		require.NotNil(t, setup.assessor, "mode %q", mode)
		want := permission.AssessorMode(mode)
		if mode == "" {
			want = permission.AssessorOff
		}
		require.Equal(t, want, setup.mode, "mode %q", mode)
	}
}

type fakeWarmer struct {
	calls    atomic.Int32
	err      error
	deadline atomic.Bool
}

func (f *fakeWarmer) Warm(ctx context.Context) error {
	f.calls.Add(1)
	_, ok := ctx.Deadline()
	f.deadline.Store(ok)
	return f.err
}

func TestWarmAssessor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode  permission.AssessorMode
		calls int32
	}{
		{permission.AssessorOff, 0},
		{permission.AssessorShadow, 1},
		{permission.AssessorEnforce, 1},
	} {
		w := &fakeWarmer{}
		warmAssessor(t.Context(), w, tc.mode)
		require.Equal(t, tc.calls, w.calls.Load(), "mode %q", tc.mode)
		if tc.calls > 0 {
			require.True(t, w.deadline.Load(), "warm-up must be bounded")
		}
	}
	warmAssessor(t.Context(), nil, permission.AssessorShadow)
}

func TestWarmAssessorSendsOneMinimalRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		data, _ := io.ReadAll(r.Body)
		body.Store(string(data))
		_, _ = io.WriteString(w, `{"model":"von-1.0.0","answers":{"warm":{"type":"noul","noul":0.9}},"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()

	ta := validTrustedAssessor(config.AssessorShadow)
	ta.Config.URL = srv.URL
	setup, ok := buildAssessorOption(ta, nil, nil)
	require.True(t, ok)
	warmAssessor(t.Context(), setup.assessor, setup.mode)
	require.Equal(t, int32(1), hits.Load())
	require.Contains(t, body.Load(), `"warm"`)
	require.Less(t, len(body.Load().(string)), 300)
}

func TestWarmAssessorOffMakesNoRequest(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	ta := validTrustedAssessor(config.AssessorOff)
	ta.Config.URL = srv.URL
	setup, ok := buildAssessorOption(ta, nil, nil)
	require.True(t, ok)
	warmAssessor(t.Context(), setup.assessor, setup.mode)
	require.Zero(t, hits.Load())
}

func TestAssessorWarmsWhenEnabledAtRuntime(t *testing.T) {
	t.Parallel()
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		_, _ = io.WriteString(w, `{"model":"von-1.0.0","answers":{"warm":{"type":"noul","noul":0.9}},"usage":{"input_tokens":10,"output_tokens":1}}`)
	}))
	defer srv.Close()

	ta := validTrustedAssessor(config.AssessorOff)
	ta.Config.URL = srv.URL
	setup, ok := buildAssessorOption(ta, nil, nil)
	require.True(t, ok)
	svc := permission.NewPermissionService(t.TempDir(), config.YoloOff, nil, nil, setup.option)
	require.True(t, svc.AssessorConfigured())
	require.Equal(t, permission.AssessorOff, svc.AssessorMode())

	svc.SetAssessorMode(permission.AssessorShadow)
	select {
	case <-hits:
	case <-time.After(10 * time.Second):
		t.Fatal("assessor was not warmed")
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
