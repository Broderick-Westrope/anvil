package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"charm.land/fantasy"
)

// minToolInputTimeout is the shortest idle window while a tool call's input
// is streaming. Providers may hold back a parameter until the model has
// generated all of it (Anthropic does without fine-grained tool streaming),
// so a large file write can be silent for as long as the model takes to
// write the file.
const minToolInputTimeout = 10 * time.Minute

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
	// tool names the tool call whose input was streaming when the window
	// ran out, if any.
	tool   string
	detail string
}

func (e *requestTimeoutError) Error() string {
	msg := fmt.Sprintf("LLM request timed out after %s", e.timeout)
	if e.idle {
		msg = fmt.Sprintf("LLM stream received no data for %s", e.timeout)
	}
	if e.tool != "" {
		msg += fmt.Sprintf(" while streaming %s tool input", e.tool)
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
	switch {
	case e.idle && e.tool != "":
		return fmt.Sprintf("The model stopped sending data for %s while writing the input for its %s tool call. %s", e.timeout, e.tool, hint)
	case e.idle:
		return fmt.Sprintf("The model stopped sending data for %s. %s", e.timeout, hint)
	}
	return fmt.Sprintf("The model did not respond within %s. %s", e.timeout, hint)
}

// requestTimeoutModel bounds each request to the wrapped model. Generate
// gets a hard deadline per call; Stream gets an idle timeout that resets
// whenever a part arrives (including Anthropic keepalives), so a slow but
// active stream is never aborted. While a tool call's input is streaming
// the idle window is toolInputTimeout instead.
type requestTimeoutModel struct {
	fantasy.LanguageModel
	timeout          time.Duration
	toolInputTimeout time.Duration
}

// newRequestTimeoutModel wraps m. A non-positive timeout or nil model
// returns m unchanged.
func newRequestTimeoutModel(m fantasy.LanguageModel, timeout time.Duration) fantasy.LanguageModel {
	if m == nil || timeout <= 0 {
		return m
	}
	return requestTimeoutModel{
		LanguageModel:    m,
		timeout:          timeout,
		toolInputTimeout: max(timeout, minToolInputTimeout),
	}
}

// wrapTimedOut replaces err with the request's timeout error when its own
// timer fired. User cancellation, outer deadlines and provider failures pass
// through unchanged.
func wrapTimedOut(ctx context.Context, err error) error {
	var timeoutErr *requestTimeoutError
	if err == nil || !errors.As(context.Cause(ctx), &timeoutErr) {
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
	return resp, wrapTimedOut(ctx, err)
}

// Stream implements fantasy.LanguageModel. The stream is consumed after
// Stream returns, so the timer outlives this call and is released when
// iteration ends. Connecting and the gaps between parts share one budget.
func (m requestTimeoutModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	// The timer cancels with whichever window is current, so the error
	// names the window that ran out.
	var window atomic.Pointer[requestTimeoutError]
	window.Store(&requestTimeoutError{timeout: m.timeout, idle: true})
	ctx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(m.timeout, func() { cancel(window.Load()) })

	inner, err := m.LanguageModel.Stream(ctx, call)
	if err != nil {
		timer.Stop()
		cancel(nil)
		return nil, wrapTimedOut(ctx, err)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		defer timer.Stop()
		defer cancel(nil)
		var inputs openToolInputs
		inner(func(part fantasy.StreamPart) bool {
			timer.Stop()
			inputs.observe(part)
			next := &requestTimeoutError{timeout: m.timeout, idle: true}
			if tool, ok := inputs.streaming(); ok {
				next = &requestTimeoutError{timeout: m.toolInputTimeout, idle: true, tool: tool}
			}
			window.Store(next)
			timer.Reset(next.timeout)
			part.Error = wrapTimedOut(ctx, part.Error)
			return yield(part)
		})
	}, nil
}

// openToolInputs tracks the tool calls whose input has started streaming
// but not finished.
type openToolInputs struct {
	ids   []string
	names []string
}

func (o *openToolInputs) observe(part fantasy.StreamPart) {
	switch part.Type {
	case fantasy.StreamPartTypeToolInputStart:
		o.ids = append(o.ids, part.ID)
		o.names = append(o.names, part.ToolCallName)
	case fantasy.StreamPartTypeToolInputEnd, fantasy.StreamPartTypeToolCall:
		if i := slices.Index(o.ids, part.ID); i >= 0 {
			o.ids = slices.Delete(o.ids, i, i+1)
			o.names = slices.Delete(o.names, i, i+1)
		}
	}
}

// streaming reports the most recently started tool call still streaming
// its input.
func (o *openToolInputs) streaming() (string, bool) {
	if len(o.names) == 0 {
		return "", false
	}
	return o.names[len(o.names)-1], true
}
