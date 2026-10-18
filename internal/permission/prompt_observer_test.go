package permission

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/stretchr/testify/require"
)

// promptRecorder collects the times a prompt observer was called with.
type promptRecorder struct {
	mu    sync.Mutex
	times []time.Time
}

func (g *promptRecorder) observe(at time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.times = append(g.times, at)
}

func (g *promptRecorder) calls() []time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]time.Time(nil), g.times...)
}

func TestPromptObserver_HumanGrantNotifiesBeforeReturning(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
	rec := &promptRecorder{}
	ctx := WithPromptObserver(testCtx(t), "a", rec.observe)

	done := requestAsync(ctx, h.svc, h.req("a", "rm -rf build"))
	prompt := waitPrompt(t, h.events)
	before := time.Now()
	h.svc.Grant(prompt)
	res := waitResult(t, done)
	after := time.Now()

	require.NoError(t, res.err)
	require.True(t, res.result.Granted)
	calls := rec.calls()
	require.Len(t, calls, 1)
	require.False(t, calls[0].Before(before), "grant time is when the human answered")
	require.False(t, calls[0].After(after))
}

func TestPromptObserver_HumanDenyNotifies(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
	rec := &promptRecorder{}
	ctx := WithPromptObserver(testCtx(t), "a", rec.observe)

	done := requestAsync(ctx, h.svc, h.req("a", "rm -rf build"))
	h.svc.Deny(waitPrompt(t, h.events), "")
	res := waitResult(t, done)

	require.NoError(t, res.err)
	require.False(t, res.result.Granted)
	require.Len(t, rec.calls(), 1)
}

func TestPromptObserver_CancelDuringPromptNotifies(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
	rec := &promptRecorder{}
	ctx, cancel := context.WithCancel(testCtx(t))
	ctx = WithPromptObserver(ctx, "a", rec.observe)

	done := requestAsync(ctx, h.svc, h.req("a", "rm -rf build"))
	waitPrompt(t, h.events)
	cancel()
	res := waitResult(t, done)

	require.ErrorIs(t, res.err, context.Canceled)
	require.Len(t, rec.calls(), 1)
}

func TestPromptObserver_CancelBeforePromptDoesNotNotify(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
	rec := &promptRecorder{}
	ctx, cancel := context.WithCancel(testCtx(t))
	cancel()

	_, err := h.svc.Request(WithPromptObserver(ctx, "a", rec.observe), h.req("a", "rm -rf build"))
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, rec.calls())
}

func TestPromptObserver_OtherToolCallIsNotNotified(t *testing.T) {
	t.Parallel()
	h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
	rec := &promptRecorder{}
	ctx := WithPromptObserver(testCtx(t), "other", rec.observe)

	done := requestAsync(ctx, h.svc, h.req("a", "rm -rf build"))
	h.svc.Grant(waitPrompt(t, h.events))
	res := waitResult(t, done)

	require.True(t, res.result.Granted)
	require.Empty(t, rec.calls())
}

func TestPromptObserver_AutomaticApprovalsDoNotNotify(t *testing.T) {
	t.Parallel()

	allowBash := []config.PermissionRule{{ToolPattern: "bash", Action: config.PermissionAllow}}
	tests := map[string]func(t *testing.T, ctx context.Context) RequestResult{
		"allow-list rule": func(t *testing.T, ctx context.Context) RequestResult {
			h := newBouncerHarness(t, nil, BouncerOff, allowBash, nil)
			r, err := h.svc.Request(ctx, h.req("a", "ls"))
			require.NoError(t, err)
			return r
		},
		"hook approval": func(t *testing.T, ctx context.Context) RequestResult {
			h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
			r, err := h.svc.Request(WithHookApproval(ctx, "a"), h.req("a", "ls"))
			require.NoError(t, err)
			return r
		},
		"yolo standard": func(t *testing.T, ctx context.Context) RequestResult {
			h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
			h.svc.SetYoloLevel(config.YoloStandard)
			r, err := h.svc.Request(ctx, h.req("a", "ls"))
			require.NoError(t, err)
			return r
		},
		"yolo full": func(t *testing.T, ctx context.Context) RequestResult {
			h := newBouncerHarness(t, nil, BouncerOff, nil, nil)
			h.svc.SetYoloLevel(config.YoloFull)
			r, err := h.svc.Request(ctx, h.req("a", "ls"))
			require.NoError(t, err)
			return r
		},
		"bouncer allow": func(t *testing.T, ctx context.Context) RequestResult {
			h := newBouncerHarness(t, &fakeBouncer{outcome: AssessAllow}, BouncerEnforce, nil, nil)
			r, err := h.svc.Request(ctx, h.req("a", "go build ./..."))
			require.NoError(t, err)
			return r
		},
		"bouncer allow cache": func(t *testing.T, ctx context.Context) RequestResult {
			fake := &fakeBouncer{outcome: AssessAllow}
			h := newBouncerHarness(t, fake, BouncerEnforce, nil, nil)
			_, err := h.svc.Request(testCtx(t), h.req("warm", "go build ./..."))
			require.NoError(t, err)
			r, err := h.svc.Request(ctx, h.req("a", "go build ./..."))
			require.NoError(t, err)
			require.Equal(t, int32(1), fake.calls.Load(), "the second request is served from the allow cache")
			return r
		},
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := &promptRecorder{}
			result := run(t, WithPromptObserver(testCtx(t), "a", rec.observe))
			require.True(t, result.Granted)
			require.Empty(t, rec.calls())
		})
	}
}
