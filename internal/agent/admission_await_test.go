package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func textResult(text string) *fantasy.AgentResult {
	return &fantasy.AgentResult{Response: fantasy.Response{Content: fantasy.ResponseContent{fantasy.TextContent{Text: text}}}}
}

func resultText(r *fantasy.AgentResult) string {
	if r == nil {
		return ""
	}
	return r.Response.Content.Text()
}

// blockingRun returns a run that signals entry and waits for release.
func blockingRun(text string, entered chan<- struct{}, release <-chan struct{}) func(context.Context) (*fantasy.AgentResult, error) {
	return func(ctx context.Context) (*fantasy.AgentResult, error) {
		if entered != nil {
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return textResult(text), nil
	}
}

func instantRun(text string) func(context.Context) (*fantasy.AgentResult, error) {
	return func(context.Context) (*fantasy.AgentResult, error) { return textResult(text), nil }
}

func TestAwaitQueuedBehindBusySessionGetsOwnResult(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan string, 1)
	go func() {
		r, _ := a.submit(t.Context(), "s", submission{prompt: "user", run: blockingRun("A", entered, release)})
		firstDone <- resultText(r)
	}()
	<-entered

	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: instantRun("B")})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
	_, err := a.submit(t.Context(), "s", submission{prompt: "later", run: instantRun("C")})
	require.NoError(t, err)

	close(release)
	require.Equal(t, "B", <-awaited)
	<-firstDone
}

func TestAwaitOnIdleSessionReturnsBeforeLaterQueuedWork(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: blockingRun("mine", entered, release)})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	<-entered
	laterEntered, laterRelease := make(chan struct{}), make(chan struct{})
	defer close(laterRelease)
	_, err := a.submit(t.Context(), "s", submission{prompt: "user", run: blockingRun("theirs", laterEntered, laterRelease)})
	require.NoError(t, err)

	close(release)
	require.Equal(t, "mine", <-awaited)
	<-laterEntered
}

func TestAwaitQueuedIsCancelledWhenQueueIsCleared(t *testing.T) {
	t.Parallel()

	for name, clearQueue := range map[string]func(a *admission){
		"cancel": func(a *admission) { a.cancel("s") },
		"clear":  func(a *admission) { a.clear("s") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newAdmission(t.Context())
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			go func() {
				_, _ = a.submit(t.Context(), "s", submission{run: blockingRun("A", entered, release)})
			}()
			<-entered
			awaited := make(chan error, 1)
			go func() {
				_, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: instantRun("B")})
				awaited <- err
			}()
			require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
			clearQueue(a)
			require.ErrorIs(t, <-awaited, context.Canceled)
		})
	}
}

func TestAwaitWithdrawsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{run: blockingRun("A", entered, release)})
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	awaited := make(chan error, 1)
	go func() {
		_, err := a.submitAwait(ctx, "s", submission{prompt: "script", run: instantRun("B")})
		awaited <- err
	}()
	require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-awaited, context.Canceled)
	require.Empty(t, a.queued("s"))
}

func TestAwaitKeepsOwnershipAcrossContinuation(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var order []string
	userQueued := make(chan struct{})
	r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		order = append(order, "first")
		go func() {
			_, _ = a.submit(t.Context(), "s", submission{prompt: "user", run: func(context.Context) (*fantasy.AgentResult, error) {
				order = append(order, "user")
				return nil, nil
			}})
			close(userQueued)
		}()
		<-userQueued
		a.continueWith(ctx, "s", submission{prompt: "continue", run: func(context.Context) (*fantasy.AgentResult, error) {
			order = append(order, "continuation")
			return textResult("finished"), nil
		}})
		return textResult("compacted"), nil
	}})
	require.NoError(t, err)
	require.Equal(t, "finished", resultText(r))
	require.Eventually(t, func() bool { return !a.busy("s") }, time.Second, time.Millisecond)
	require.Equal(t, []string{"first", "continuation", "user"}, order)
}

func TestAwaitQueuedRunsAfterPreviousOwnerFails(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			return nil, errors.New("user turn failed")
		}})
	}()
	<-entered
	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: instantRun("B")})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
	close(release)
	require.Equal(t, "B", <-awaited)
}

func TestAwaitedRunUsesItsOwnContext(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	release := make(chan struct{})
	defer close(release)
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{run: blockingRun("A", nil, release)})
	}()
	require.Eventually(t, func() bool { return a.busy("s") }, time.Second, time.Millisecond)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type key struct{}
	ctx = context.WithValue(ctx, key{}, "script")
	got := make(chan any, 1)
	go func() {
		_, _ = a.submitAwait(ctx, "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			got <- ctx.Value(key{})
			return nil, nil
		}})
	}()
	require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)
	release <- struct{}{}
	require.Equal(t, "script", <-got)
}

func TestAwaitFromInsideItsOwnRunExecutesDirectly(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	done := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			return a.submitAwait(ctx, "s", submission{run: instantRun("nested")})
		}})
		require.NoError(t, err)
		done <- resultText(r)
	}()
	select {
	case got := <-done:
		require.Equal(t, "nested", got)
	case <-time.After(2 * time.Second):
		t.Fatal("nested awaited submission deadlocked")
	}
}

func TestAwaitBehindQueuedUserWorkRunsAfterUserTurnFails(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "A", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			return nil, errors.New("user turn failed")
		}})
	}()
	<-entered
	_, err := a.submit(t.Context(), "s", submission{prompt: "B", run: instantRun("B")})
	require.NoError(t, err)
	awaited := make(chan string, 1)
	go func() {
		r, err := a.submitAwait(t.Context(), "s", submission{prompt: "C", run: instantRun("C")})
		require.NoError(t, err)
		awaited <- resultText(r)
	}()
	require.Eventually(t, func() bool { return len(a.queued("s")) == 2 }, time.Second, time.Millisecond)
	close(release)
	select {
	case got := <-awaited:
		require.Equal(t, "C", got)
	case <-time.After(2 * time.Second):
		t.Fatal("awaited submission stranded behind queued user work")
	}
}

func TestContinuationRegisteredAfterCancelNeverRuns(t *testing.T) {
	t.Parallel()

	for name, submitAwaited := range map[string]bool{"awaited": true, "plain": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := newAdmission(t.Context())
			var continued atomic.Bool
			run := func(ctx context.Context) (*fantasy.AgentResult, error) {
				// Esc lands between the summary and registering the
				// continuation.
				a.cancel("s")
				a.continueWith(ctx, "s", submission{prompt: "continue", run: func(context.Context) (*fantasy.AgentResult, error) {
					continued.Store(true)
					return nil, nil
				}})
				return nil, nil
			}
			if submitAwaited {
				_, _ = a.submitAwait(t.Context(), "s", submission{run: run})
			} else {
				_, _ = a.submit(t.Context(), "s", submission{run: run})
			}
			// Both submit paths return only after any continuation the run
			// handed off to has finished, so no wait is needed.
			require.False(t, a.busy("s"))
			require.False(t, continued.Load())
			require.Empty(t, a.queued("s"))
		})
	}
}

func TestAwaitOnIdleSessionRunsWorkLeftQueuedFirst(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var order []string
	_, err := a.submit(t.Context(), "s", submission{run: func(context.Context) (*fantasy.AgentResult, error) {
		_, err := a.submit(t.Context(), "s", submission{prompt: "pending", run: func(context.Context) (*fantasy.AgentResult, error) {
			order = append(order, "pending")
			return nil, nil
		}})
		require.NoError(t, err)
		return nil, errors.New("user turn failed")
	}})
	require.Error(t, err)
	require.Equal(t, []string{"pending"}, a.queued("s"))

	r, err := a.submitAwait(t.Context(), "s", submission{prompt: "script", run: func(context.Context) (*fantasy.AgentResult, error) {
		order = append(order, "script")
		return textResult("mine"), nil
	}})
	require.NoError(t, err)
	require.Equal(t, "mine", resultText(r))
	require.Equal(t, []string{"pending", "script"}, order)
}

func TestUnheldContinuationRunsBehindMessagesQueuedBeforeIt(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var order []string
	record := func(name string) func(context.Context) (*fantasy.AgentResult, error) {
		return func(context.Context) (*fantasy.AgentResult, error) {
			order = append(order, name)
			return nil, nil
		}
	}
	_, err := a.submit(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		_, err := a.submit(t.Context(), "s", submission{prompt: "next", run: record("next")})
		require.NoError(t, err)
		a.continueWith(ctx, "s", submission{prompt: "continue", run: record("continue")})
		return nil, nil
	}})
	require.NoError(t, err)
	require.Equal(t, []string{"next", "continue"}, order)
}

func TestAwaitWithAFinishedOwnersContextIsRefused(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	var old context.Context
	_, err := a.submitAwait(t.Context(), "s", submission{run: func(ctx context.Context) (*fantasy.AgentResult, error) {
		old = ctx
		return nil, nil
	}})
	require.NoError(t, err)
	var ran atomic.Bool
	_, err = a.submitAwait(context.WithoutCancel(old), "s", submission{run: func(context.Context) (*fantasy.AgentResult, error) {
		ran.Store(true)
		return nil, nil
	}})
	require.ErrorIs(t, err, ErrSessionBusy)
	require.False(t, ran.Load())
}

// Esc cancels the turn and the prompts queued behind it. Silent work (a
// workflow's marker, a metadata entry) isn't a prompt the user can see, so
// it survives and runs once the session is free.
func TestCancellingASessionKeepsItsSilentWork(t *testing.T) {
	t.Parallel()

	for name, stop := range map[string]func(a *admission){
		"cancel": func(a *admission) { a.cancel("s") },
		"clear":  func(a *admission) { a.clear("s") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			a := newAdmission(t.Context())
			entered, release := make(chan struct{}), make(chan struct{})
			go func() {
				_, _ = a.submit(t.Context(), "s", submission{prompt: "user", run: blockingRun("A", entered, release)})
			}()
			<-entered

			silent := make(chan error, 1)
			go func() {
				_, err := a.submitAwait(t.Context(), "s", submission{silent: true, run: instantRun("marker")})
				silent <- err
			}()
			queuedPrompt := make(chan error, 1)
			go func() {
				_, err := a.submitAwait(t.Context(), "s", submission{prompt: "queued", run: instantRun("Q")})
				queuedPrompt <- err
			}()
			require.Eventually(t, func() bool { return len(a.queued("s")) == 1 }, time.Second, time.Millisecond)

			stop(a)
			require.ErrorIs(t, <-queuedPrompt, context.Canceled, "the user's queued prompt is dropped")
			close(release)
			require.NoError(t, <-silent, "the silent work still runs")
		})
	}
}

// A failed turn holds back the prompts queued behind it. Silent work queued
// among them (a model change written while the turn ran) still runs, and
// mustn't release the prompts when it does.
func TestSilentWorkAfterAFailedTurnKeepsPromptsHeld(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "A", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			return nil, errors.New("user turn failed")
		}})
	}()
	<-entered
	ran := make(chan struct{}, 1)
	_, err := a.submit(t.Context(), "s", submission{prompt: "B", run: func(context.Context) (*fantasy.AgentResult, error) {
		ran <- struct{}{}
		return textResult("B"), nil
	}})
	require.NoError(t, err)
	silent := make(chan error, 1)
	go func() {
		_, err := a.submitAwait(t.Context(), "s", submission{silent: true, run: instantRun("meta")})
		silent <- err
	}()
	require.Eventually(t, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.queues["s"]) == 2
	}, time.Second, time.Millisecond)

	close(release)
	require.NoError(t, <-silent, "the silent work runs")
	require.Eventually(t, func() bool { return !a.busy("s") }, time.Second, time.Millisecond)
	select {
	case <-ran:
		t.Fatal("the held prompt ran")
	default:
	}
	require.Equal(t, []string{"B"}, a.queued("s"), "the prompt is still queued")
}

// The same, when the silent work arrives after the failed turn has ended
// and the session is idle with the prompt still held.
func TestSilentWorkOnAnIdleSessionKeepsHeldPromptsHeld(t *testing.T) {
	t.Parallel()

	a := newAdmission(t.Context())
	entered, release := make(chan struct{}), make(chan struct{})
	failed := make(chan struct{})
	go func() {
		_, _ = a.submit(t.Context(), "s", submission{prompt: "A", run: func(ctx context.Context) (*fantasy.AgentResult, error) {
			close(entered)
			<-release
			return nil, errors.New("user turn failed")
		}})
		close(failed)
	}()
	<-entered
	ran := make(chan struct{}, 1)
	_, err := a.submit(t.Context(), "s", submission{prompt: "B", run: func(context.Context) (*fantasy.AgentResult, error) {
		ran <- struct{}{}
		return textResult("B"), nil
	}})
	require.NoError(t, err)
	close(release)
	<-failed
	require.Eventually(t, func() bool { return !a.busy("s") }, time.Second, time.Millisecond)

	_, err = a.submitAwait(t.Context(), "s", submission{silent: true, run: instantRun("meta")})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !a.busy("s") }, time.Second, time.Millisecond)
	select {
	case <-ran:
		t.Fatal("the held prompt ran")
	default:
	}
	require.Equal(t, []string{"B"}, a.queued("s"))
}
