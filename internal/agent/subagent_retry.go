package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"charm.land/fantasy"
)

// errLocalPersistence marks a subagent failure that came from saving to the
// local database rather than from the provider. Retrying can't fix it.
var errLocalPersistence = errors.New("local persistence")

// permanentProviderStatuses are HTTP statuses a provider returns for a
// request that will fail the same way however often it's sent.
var permanentProviderStatuses = map[int]bool{
	http.StatusBadRequest:            true,
	http.StatusForbidden:             true,
	http.StatusNotFound:              true,
	http.StatusRequestEntityTooLarge: true,
	http.StatusUnprocessableEntity:   true,
}

// classifySubagentFailure decides whether a subagent run that ended with
// err should be resumed automatically, and if so names the failure for the
// task result. The rules are checked in order and the first match wins;
// callCtx is the task call's context. The order is load-bearing: a tool or
// a 400 can wrap a network error, and neither is worth retrying. A run that
// finished without text isn't a failure here at all, so it's never
// retried either.
func classifySubagentFailure(callCtx context.Context, err error) (retry bool, reason string) {
	if callCtx.Err() != nil {
		return false, ""
	}
	var toolErr *fantasy.ToolExecutionError
	if errors.As(err, &toolErr) || errors.Is(err, errLocalPersistence) {
		return false, ""
	}
	var providerErr *fantasy.ProviderError
	if errors.As(err, &providerErr) {
		return classifyProviderError(providerErr)
	}
	if reason := networkFailure(err); reason != "" {
		return true, reason
	}
	return false, ""
}

func classifyProviderError(err *fantasy.ProviderError) (retry bool, reason string) {
	unauthorized := err.StatusCode == http.StatusUnauthorized
	if permanentProviderStatuses[err.StatusCode] || err.IsContextTooLarge() || (err.AuthError && !unauthorized) {
		return false, ""
	}
	if unauthorized || err.IsRetryable() {
		return true, providerFailureReason(err)
	}
	if reason := networkFailure(err.Cause); reason != "" {
		return true, reason
	}
	return false, ""
}

func providerFailureReason(err *fantasy.ProviderError) string {
	title := err.Title
	if title == "" {
		title = err.Message
	}
	if err.StatusCode == 0 {
		return title
	}
	return fmt.Sprintf("%d %s", err.StatusCode, title)
}

// networkFailure names a transient network or transport failure in err, or
// returns "" when err isn't one. The call's own deadline is ruled out
// before this runs, so a deadline here is a request timeout.
func networkFailure(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected EOF"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case fantasy.IsTransportError(err):
		return "broken stream"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "request timeout"
	}
	return ""
}

// maxSubagentRetryDelay caps a provider's retry hint, so a quota reset
// hours away can't leave the orchestrator's task call waiting silently.
const maxSubagentRetryDelay = 2 * time.Minute

// withProviderHint returns how long to wait before resuming after err:
// base, or the provider's retry hint when it asks for longer, up to
// maxSubagentRetryDelay.
func withProviderHint(err error, base time.Duration) time.Duration {
	var providerErr *fantasy.ProviderError
	if !errors.As(err, &providerErr) {
		return base
	}
	return min(max(base, retryHint(providerErr.ResponseHeaders)), maxSubagentRetryDelay)
}

// retryHint returns the wait a provider asks for, no longer than
// maxSubagentRetryDelay. Values are clamped before they become a
// Duration, since converting a huge float is implementation-defined.
func retryHint(headers map[string]string) time.Duration {
	limit := maxSubagentRetryDelay.Seconds()
	if ms, err := strconv.ParseFloat(headers["retry-after-ms"], 64); err == nil && ms > 0 {
		return time.Duration(min(ms/1000, limit) * float64(time.Second))
	}
	retryAfter := headers["retry-after"]
	if secs, err := strconv.ParseFloat(retryAfter, 64); err == nil && secs > 0 {
		return time.Duration(min(secs, limit) * float64(time.Second))
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}
