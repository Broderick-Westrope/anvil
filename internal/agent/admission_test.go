package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestAdmissionOwnsPreparationAndFIFO(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	var order []string
	go func() {
		_, err := a.submit(t.Context(), "s", submission{prompt: "first", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			return nil, nil
		}})
		done <- err
	}()
	<-entered
	require.True(t, a.busy("s"))
	_, err := a.submit(t.Context(), "s", submission{exclusive: true})
	require.ErrorIs(t, err, ErrSessionBusy)
	for _, prompt := range []string{"second", "third"} {
		_, err = a.submit(t.Context(), "s", submission{prompt: prompt, run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			require.True(t, a.busy("s"))
			order = append(order, prompt)
			return nil, nil
		}})
		require.NoError(t, err)
	}
	require.Equal(t, []string{"second", "third"}, a.queued("s"))
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, []string{"second", "third"}, order)
	require.False(t, a.busy("s"))
}

func TestAdmissionCancellationAndOwnerIdentity(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var old context.Context
	_, err := a.submit(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		old = ctx
		_, err := a.submit(ctx, "other", submission{})
		require.ErrorIs(t, err, ErrSessionBusy)
		_, err = a.submit(ctx, "s", submission{run: func(context.Context) (*fantasy.AgentResult, error) { return nil, nil }})
		require.NoError(t, err)
		_, err = a.submit(t.Context(), "s", submission{prompt: "queued"})
		require.NoError(t, err)
		a.cancel("s")
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		require.True(t, a.busy("s"))
		require.Empty(t, a.queued("s"))
		return nil, ctx.Err()
	}})
	require.ErrorIs(t, err, context.Canceled)
	_, err = a.submit(context.WithoutCancel(old), "s", submission{})
	require.ErrorIs(t, err, ErrSessionBusy)
}

func TestAdmissionErrorPreservesQueue(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	failure := errors.New("preparation failed")
	_, err := a.submit(t.Context(), "s", submission{run: func(context.Context) (*fantasy.AgentResult, error) {
		_, err := a.submit(t.Context(), "s", submission{prompt: "pending"})
		require.NoError(t, err)
		return nil, failure
	}})
	require.ErrorIs(t, err, failure)
	require.False(t, a.busy("s"))
	require.Equal(t, []string{"pending"}, a.queued("s"))
	_, err = a.submit(t.Context(), "s", submission{exclusive: true})
	require.ErrorIs(t, err, ErrSessionBusy)
	a.clear("s")
	require.Empty(t, a.queued("s"))
}

func TestAdmissionHandoffUsesNewOwner(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var first *submissionOwner
	_, err := a.submit(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		first = ctx.Value(ownerKey{}).(*submissionOwner)
		return a.submit(t.Context(), "s", submission{prompt: "next", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			require.NotSame(t, first, ctx.Value(ownerKey{}))
			require.ErrorIs(t, first.ctx.Err(), context.Canceled)
			require.True(t, a.busy("s"))
			a.cancel("s")
			return nil, ctx.Err()
		}})
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, a.busy("s"))
}

func TestAdmissionCompletionObservesReleasedOwner(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	completed := false
	_, err := a.submit(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		ctx.Value(ownerKey{}).(*submissionOwner).onFinish = func() {
			completed = true
			require.False(t, a.busy("s"))
		}
		return nil, nil
	}})
	require.NoError(t, err)
	require.True(t, completed)
}

func TestOwnsWithNothingWaiting(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup func(a *admission, owner *submissionOwner) context.Context
		want  bool
	}{
		"the owner with nothing behind it": {
			setup: func(_ *admission, owner *submissionOwner) context.Context { return owner.ctx },
			want:  true,
		},
		"a context with no owner": {
			setup: func(*admission, *submissionOwner) context.Context { return context.Background() },
		},
		"another session's owner": {
			setup: func(a *admission, _ *submissionOwner) context.Context {
				return a.claim(context.Background(), "other").ctx
			},
		},
		"an owner that has been replaced": {
			setup: func(a *admission, owner *submissionOwner) context.Context {
				a.claim(context.Background(), owner.sessionID)
				return owner.ctx
			},
		},
		"an awaited owner": {
			setup: func(_ *admission, owner *submissionOwner) context.Context {
				owner.done = make(chan submissionResult, 1)
				return owner.ctx
			},
		},
		"queued work": {
			setup: func(a *admission, owner *submissionOwner) context.Context {
				a.queues[owner.sessionID] = []submission{{silent: true}}
				return owner.ctx
			},
		},
		"a hold": {
			setup: func(a *admission, owner *submissionOwner) context.Context {
				a.held[owner.sessionID] = 1
				return owner.ctx
			},
		},
		"a cancelled owner": {
			setup: func(_ *admission, owner *submissionOwner) context.Context {
				owner.cancel()
				return owner.ctx
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newAdmission(t.Context())
			owner := a.claim(t.Context(), "s")
			ctx := tc.setup(a, owner)
			require.Equal(t, tc.want, a.ownsWithNothingWaiting(ctx, "s"))
		})
	}
}
