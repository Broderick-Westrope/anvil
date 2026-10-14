package message

import (
	"context"
	"testing"
	"time"
)

// waitTimeout bounds every wait on the service under test. It is long
// enough not to flake under -race on slow CI runners, and short enough
// that a broken implementation fails in seconds instead of hanging until
// the test binary or mutation tester times out.
const waitTimeout = 5 * time.Second

// testCtx returns a context that expires after waitTimeout. Synchronous
// flushes loop until the buffer is clean, so a broken flush only returns
// once its context ends.
func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	t.Cleanup(cancel)
	return ctx
}
