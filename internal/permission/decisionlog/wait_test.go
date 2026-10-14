package decisionlog

import (
	"context"
	"sync"
	"testing"
	"time"
)

// waitTimeout bounds every wait on the recorder under test. It is long
// enough not to flake under -race on slow CI runners, and short enough
// that a broken implementation fails in seconds instead of hanging until
// the test binary or mutation tester times out.
const waitTimeout = 5 * time.Second

// testCtx returns a context that expires after waitTimeout. It doesn't
// derive from t.Context, which is already cancelled when cleanups run.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	t.Cleanup(cancel)
	return ctx
}

// recv returns the next value from ch, failing the test if none arrives
// within waitTimeout.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitTimeout):
		var zero T
		t.Fatalf("timed out after %s waiting to receive %T", waitTimeout, zero)
		return zero
	}
}

// wait waits for wg, failing the test if it doesn't finish within
// waitTimeout.
func wait(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout):
		t.Fatalf("timed out after %s waiting for goroutines to finish", waitTimeout)
	}
}
