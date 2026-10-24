package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
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

// maxTransientResumes is how many times a run, a top-level turn or one
// task call, is resumed after transient failures. Fantasy has already
// retried each step by the time a failure ends a run, so a couple of
// widely spaced resumes is enough.
const maxTransientResumes = 2

// defaultRetryDelays are the waits before each automatic resume.
var defaultRetryDelays = []time.Duration{firstRetryDelay, laterRetryDelay}

// The default waits, as constants so the mutation check treats them like
// any other pinned constant.
const (
	firstRetryDelay = 5 * time.Second
	laterRetryDelay = 30 * time.Second
)

// transientContinueText is the harness's message to a run resumed after a
// transient failure.
const transientContinueText = "Your previous attempt stopped because of a transient error. Continue where you left off."

// permanentProviderStatuses are HTTP statuses a provider returns for a
// request that will fail the same way however often it's sent.
var permanentProviderStatuses = map[int]bool{
	http.StatusBadRequest:            true,
	http.StatusForbidden:             true,
	http.StatusNotFound:              true,
	http.StatusRequestEntityTooLarge: true,
	http.StatusUnprocessableEntity:   true,
}

// classifyRunFailure decides whether a run that ended with err should be
// resumed automatically, and if so names the failure for the harness
// message. The rules are checked in order and the first match wins; callCtx
// is the context of the task call or turn. The order is load-bearing: a tool or
// a 400 can wrap a network error, and neither is worth retrying. A run that
// finished without text isn't a failure here at all, so it's never
// retried either.
func classifyRunFailure(callCtx context.Context, err error) (retry bool, reason string) {
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

// maxRetryDelay caps a provider's retry hint, so a quota reset
// hours away can't leave a run waiting silently.
const maxRetryDelay = 2 * time.Minute

// withProviderHint returns how long to wait before resuming after err:
// base, or the provider's retry hint when it asks for longer, up to
// maxRetryDelay.
func withProviderHint(err error, base time.Duration) time.Duration {
	var providerErr *fantasy.ProviderError
	if !errors.As(err, &providerErr) {
		return base
	}
	return min(max(base, retryHint(providerErr.ResponseHeaders)), maxRetryDelay)
}

// retryHint returns the wait a provider asks for, no longer than
// maxRetryDelay. Values are clamped before they become a
// Duration, since converting a huge float is implementation-defined.
func retryHint(headers map[string]string) time.Duration {
	limit := maxRetryDelay.Seconds()
	if ms, err := strconv.ParseFloat(headers["retry-after-ms"], 64); err == nil {
		return clampedSeconds(ms/1000, limit)
	}
	retryAfter := headers["retry-after"]
	if secs, err := strconv.ParseFloat(retryAfter, 64); err == nil {
		return clampedSeconds(secs, limit)
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return clampedSeconds(time.Until(at).Seconds(), limit)
	}
	return 0
}

// clampedSeconds converts secs to a Duration between 0 and limit seconds.
// A NaN hint is no hint.
func clampedSeconds(secs, limit float64) time.Duration {
	if math.IsNaN(secs) {
		return 0
	}
	return time.Duration(min(max(secs, 0), limit) * float64(time.Second))
}

// backoffFor returns the wait before the automatic resume that follows
// resumes earlier ones.
func (c *coordinator) backoffFor(resumes int) time.Duration {
	delays := c.retryDelays
	if delays == nil {
		delays = defaultRetryDelays
	}
	return delays[min(resumes, len(delays)-1)]
}
