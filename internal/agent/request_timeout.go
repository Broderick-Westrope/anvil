package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"charm.land/fantasy"
)

// requestTimeoutError reports that an LLM request exhausted its
// options.request_timeout budget. For streams it is an idle timeout: it
// fires only when the provider sends nothing for the whole window.
//
// It unwraps only to context.DeadlineExceeded, never to the provider SDK's
// error. Cancelling an HTTP/2 stream can surface as "stream error ...
// CANCEL", which fantasy classifies as a retryable transport error;
// exposing it would make fantasy retry the step, so a hung provider would
// take up to MaxRetries+1 full timeouts to fail.
type requestTimeoutError struct {
	timeout time.Duration
	idle    bool
	detail  string
}

func (e *requestTimeoutError) Error() string {
	msg := fmt.Sprintf("LLM request timed out after %s", e.timeout)
	if e.idle {
		msg = fmt.Sprintf("LLM stream received no data for %s", e.timeout)
	}
	if e.detail != "" {
		return msg + ": " + e.detail
	}
	return msg
}

func (e *requestTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// userMessage explains the timeout in the UI and how to change the limit.
func (e *requestTimeoutError) userMessage() string {
	hint := `Raise "options.request_timeout" (seconds) in anvil.json, or set it to 0 to disable the timeout.`
	if e.idle {
		return fmt.Sprintf("The model stopped sending data for %s. %s", e.timeout, hint)
	}
	return fmt.Sprintf("The model did not respond within %s. %s", e.timeout, hint)
}

// requestTimeoutModel bounds each request to the wrapped model. Generate
// gets a hard deadline per call; Stream gets an idle timeout that resets
// whenever a part arrives (including Anthropic keepalives), so a slow but
// active stream is never aborted.
type requestTimeoutModel struct {
	fantasy.LanguageModel
	timeout time.Duration
}

// newRequestTimeoutModel wraps m. A non-positive timeout or nil model
// returns m unchanged.
func newRequestTimeoutModel(m fantasy.LanguageModel, timeout time.Duration) fantasy.LanguageModel {
	if m == nil || timeout <= 0 {
		return m
	}
	return requestTimeoutModel{LanguageModel: m, timeout: timeout}
}

// wrapTimedOut replaces err with timeoutErr when this model's own timer
// fired. User cancellation, outer deadlines and provider failures pass
// through unchanged.
func wrapTimedOut(ctx context.Context, timeoutErr *requestTimeoutError, err error) error {
	if err == nil || !errors.Is(context.Cause(ctx), timeoutErr) {
		return err
	}
	// The SDK's report of our own cancellation adds nothing and reads like
	// a user cancel, so keep only errors that say something else.
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		timeoutErr.detail = err.Error()
	}
	return timeoutErr
}

// Generate implements fantasy.LanguageModel.
func (m requestTimeoutModel) Generate(ctx context.Context, call fantasy.Call) (*fantasy.Response, error) {
	timeoutErr := &requestTimeoutError{timeout: m.timeout}
	ctx, cancel := context.WithTimeoutCause(ctx, m.timeout, timeoutErr)
	defer cancel()
	resp, err := m.LanguageModel.Generate(ctx, call)
	return resp, wrapTimedOut(ctx, timeoutErr, err)
}

// Stream implements fantasy.LanguageModel. The stream is consumed after
// Stream returns, so the timer outlives this call and is released when
// iteration ends. Connecting and the gaps between parts share one budget.
func (m requestTimeoutModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	timeoutErr := &requestTimeoutError{timeout: m.timeout, idle: true}
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(m.timeout, func() { cancel(timeoutErr) })

	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		timer.Stop()
		cancel(nil)
		return nil, wrapTimedOut(ctx, timeoutErr, err)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer timer.Stop()
		defer cancel(nil)
		inner(func(part fantasy.StreamPart) bool {
			timer.Reset(m.timeout)
			part.Error = wrapTimedOut(ctx, timeoutErr, part.Error)
			return yield(part)
		})
	}, nil
}
