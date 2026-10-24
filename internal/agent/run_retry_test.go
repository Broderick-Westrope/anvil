package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/message"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

func TestClassifySubagentFailure(t *testing.T) {
	t.Parallel()

	wrap := func(err error) error { return fmt.Errorf("stream step: %w", fmt.Errorf("agent: %w", err)) }
	provider := func(status int) error {
		return wrap(&fantasy.ProviderError{StatusCode: status, Title: fantasy.ErrorTitleForStatusCode(status)})
	}

	tests := map[string]struct {
		cancelCall bool
		err        error
		retry      bool
	}{
		"1 call cancelled":                    {cancelCall: true, err: provider(503), retry: false},
		"2 tool error wrapping network":       {err: wrap(&fantasy.ToolExecutionError{ToolName: "fetch", Err: io.ErrUnexpectedEOF}), retry: false},
		"2 local persistence":                 {err: wrap(fmt.Errorf("save message: %w", errLocalPersistence)), retry: false},
		"4 bad request":                       {err: provider(400), retry: false},
		"4 forbidden":                         {err: provider(403), retry: false},
		"4 not found":                         {err: provider(404), retry: false},
		"4 too large":                         {err: provider(413), retry: false},
		"4 unprocessable":                     {err: provider(422), retry: false},
		"4 context length":                    {err: wrap(&fantasy.ProviderError{StatusCode: 503, ContextTooLargeErr: true}), retry: false},
		"4 non-401 auth error":                {err: wrap(&fantasy.ProviderError{StatusCode: 403, AuthError: true}), retry: false},
		"4 bad request caused by EOF":         {err: wrap(&fantasy.ProviderError{StatusCode: 400, Cause: io.ErrUnexpectedEOF}), retry: false},
		"5 persistent 401":                    {err: provider(401), retry: true},
		"6 overloaded":                        {err: provider(529), retry: true},
		"6 service unavailable":               {err: provider(503), retry: true},
		"6 rate limited":                      {err: provider(429), retry: true},
		"6 request timeout status":            {err: provider(408), retry: true},
		"6 conflict":                          {err: provider(409), retry: true},
		"6 transient stream error":            {err: wrap(&fantasy.ProviderError{TransientError: true, Title: "overloaded_error"}), retry: true},
		"7 unexpected EOF":                    {err: wrap(io.ErrUnexpectedEOF), retry: true},
		"7 connection reset":                  {err: wrap(syscall.ECONNRESET), retry: true},
		"7 http2 stream reset":                {err: wrap(http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal}), retry: true},
		"7 request timeout, call still alive": {err: wrap(context.DeadlineExceeded), retry: true},
		"7 provider error caused by a reset":  {err: wrap(&fantasy.ProviderError{Cause: syscall.ECONNRESET}), retry: true},
		"8 unknown provider status":           {err: provider(418), retry: false},
		"8 anything else":                     {err: wrap(errors.New("boom")), retry: false},
		"8 cancelled attempt, call alive":     {err: wrap(context.Canceled), retry: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancelCall {
				cancel()
			}
			retry, reason := classifyRunFailure(ctx, tc.err)
			require.Equal(t, tc.retry, retry)
			if retry {
				require.NotEmpty(t, reason)
			}
		})
	}
}

func TestNetworkFailuresAreResumedWithTheirReason(t *testing.T) {
	t.Parallel()

	dial := func(err error) error {
		return fmt.Errorf("retry error: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: err}})
	}
	tests := map[string]struct {
		err    error
		reason string
	}{
		"refused":          {err: dial(syscall.ECONNREFUSED), reason: "connection refused"},
		"network down":     {err: dial(syscall.ENETDOWN), reason: "network unreachable"},
		"net unreachable":  {err: dial(syscall.ENETUNREACH), reason: "network unreachable"},
		"host unreachable": {err: dial(syscall.EHOSTUNREACH), reason: "network unreachable"},
		"no such host": {
			err:    fmt.Errorf("retry error: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "api.anthropic.com", IsNotFound: true}}),
			reason: "DNS lookup failed",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			retry, reason := classifyRunFailure(t.Context(), tc.err)
			require.True(t, retry)
			require.Equal(t, tc.reason, reason)
		})
	}
}

func TestSubagentFailureReasonNamesTheStatus(t *testing.T) {
	t.Parallel()
	_, reason := classifyRunFailure(t.Context(), fmt.Errorf("x: %w", &fantasy.ProviderError{StatusCode: 503, Title: "overloaded"}))
	require.Equal(t, "503 overloaded", reason)
}

func TestSubagentRetryDelayHonoursALongerProviderHint(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		base time.Duration
		want time.Duration
	}{
		"no hint":          {err: io.ErrUnexpectedEOF, base: 5 * time.Second, want: 5 * time.Second},
		"shorter hint":     {err: &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after": "2"}}, base: 5 * time.Second, want: 5 * time.Second},
		"longer hint":      {err: &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after": "12"}}, base: 5 * time.Second, want: 12 * time.Second},
		"longer ms hint":   {err: &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after-ms": "7500"}}, base: 5 * time.Second, want: 7500 * time.Millisecond},
		"unparseable hint": {err: &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after": "soon"}}, base: 5 * time.Second, want: 5 * time.Second},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, withProviderHint(fmt.Errorf("w: %w", tc.err), tc.base))
		})
	}
}

func TestSubagentRetryDelayReadsAnHTTPDateHint(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)
	err := &fantasy.ProviderError{StatusCode: 503, ResponseHeaders: map[string]string{"retry-after": at}}
	got := withProviderHint(err, 5*time.Second)
	require.Greater(t, got, 15*time.Second)
	require.LessOrEqual(t, got, 20*time.Second)
}

func TestLocalPersistenceMarksOnlyRealFailures(t *testing.T) {
	t.Parallel()
	live := t.Context()
	cancelled, cancel := context.WithCancelCause(t.Context())
	cancel(errSubagentInterrupted)

	require.ErrorIs(t, localPersistence(live, errors.New("disk full")), errLocalPersistence)
	require.NotErrorIs(t, localPersistence(cancelled, context.Canceled), errLocalPersistence,
		"a write cut off by an interrupt is a cancellation, not a persistence failure")
	require.NoError(t, localPersistence(live, nil))
	require.Equal(t, "disk full", localPersistence(live, errors.New("disk full")).Error())
}

func TestLocalPersistenceStillMarksAStorageFailureThatRacedACancel(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, localPersistence(cancelled, errors.New("disk full")), errLocalPersistence)
}

func TestSubagentRetryDelayCapsTheProviderHint(t *testing.T) {
	t.Parallel()
	err := &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after": "3600"}}
	require.Equal(t, maxRetryDelay, withProviderHint(err, 5*time.Second))
}

func TestSubagentRetryDelaySurvivesAbsurdHints(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"huge seconds": "1e300",
		"infinite":     "Inf",
	}
	for name, hint := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: map[string]string{"retry-after": hint, "retry-after-ms": hint}}
			require.Equal(t, maxRetryDelay, withProviderHint(err, 5*time.Second))
		})
	}
}

func TestSubagentRetryHintFallsBackAndIgnoresNaN(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		headers map[string]string
		want    time.Duration
	}{
		"bad ms falls back to seconds": {headers: map[string]string{"retry-after-ms": "soon", "retry-after": "12"}, want: 12 * time.Second},
		"NaN is no hint":               {headers: map[string]string{"retry-after": "NaN"}, want: 5 * time.Second},
		"negative is no hint":          {headers: map[string]string{"retry-after": "-30"}, want: 5 * time.Second},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := &fantasy.ProviderError{StatusCode: 429, ResponseHeaders: tc.headers}
			require.Equal(t, tc.want, withProviderHint(err, 5*time.Second))
		})
	}
}

func TestResumeAfterTransientFailures(t *testing.T) {
	t.Parallel()

	unauthorized := &fantasy.ProviderError{StatusCode: http.StatusUnauthorized, Title: "unauthorized"}
	badRequest := &fantasy.ProviderError{StatusCode: http.StatusBadRequest, Title: "bad request"}
	retryInjection := func(attempt int) []message.InjectionContent {
		return []message.InjectionContent{{
			Source: message.InjectionSourceHarness, Kind: message.InjectionKindRetry, Text: transientContinueText,
			Attempt: attempt, MaxAttempts: maxTransientResumes, Reason: "503 overloaded",
		}}
	}
	type outcome struct {
		accept bool
		err    error
	}
	tests := map[string]struct {
		accepted      bool
		summaryFailed bool
		err           error
		outcomes      []outcome
		wantCalls     []SessionAgentCall
		wantErr       error
	}{
		"a saved turn continues with a harness message": {
			accepted:  true,
			err:       overloaded(),
			outcomes:  []outcome{{accept: true}},
			wantCalls: []SessionAgentCall{{Injections: retryInjection(1)}},
		},
		"a prompt that was never saved is sent again": {
			err:       overloaded(),
			outcomes:  []outcome{{accept: true}},
			wantCalls: []SessionAgentCall{{Prompt: "hi", Attachments: []message.Attachment{{FileName: "a.txt"}}}},
		},
		"a harness message that was never saved is sent again as the next attempt": {
			accepted:  true,
			err:       overloaded(),
			outcomes:  []outcome{{err: overloaded()}, {accept: true}},
			wantCalls: []SessionAgentCall{{Injections: retryInjection(1)}, {Injections: retryInjection(2)}},
		},
		"it gives up after the last resume": {
			accepted:  true,
			err:       overloaded(),
			outcomes:  []outcome{{accept: true, err: overloaded()}, {accept: true, err: overloaded()}},
			wantCalls: []SessionAgentCall{{Injections: retryInjection(1)}, {Injections: retryInjection(2)}},
			wantErr:   overloaded(),
		},
		"a permanent failure isn't resumed": {
			accepted: true,
			err:      badRequest,
			wantErr:  badRequest,
		},
		"an unauthorized failure is left to the token refresh": {
			accepted: true,
			err:      unauthorized,
			wantErr:  unauthorized,
		},
		"a failed summary isn't resumed": {
			accepted:      true,
			summaryFailed: true,
			err:           overloaded(),
			wantErr:       overloaded(),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &coordinator{retryDelays: []time.Duration{time.Millisecond}}
			first := SessionAgentCall{
				Prompt: "hi", Attachments: []message.Attachment{{FileName: "a.txt"}},
				state: &runState{summaryFailed: tc.summaryFailed},
			}
			if tc.accepted {
				first.state.acceptedUserID = "u1"
			}
			var calls []SessionAgentCall
			run := func(call SessionAgentCall) (*fantasy.AgentResult, error) {
				if tc.accepted {
					require.NotSame(t, first.state, call.state, "a resumed turn starts from the session's leaf")
				} else {
					require.Same(t, first.state, call.state, "an unsaved prompt keeps its run state")
				}
				got := tc.outcomes[len(calls)]
				calls = append(calls, SessionAgentCall{Prompt: call.Prompt, Attachments: call.Attachments, Injections: call.Injections})
				if got.accept {
					call.state.acceptedUserID = "u2"
				}
				if got.err != nil {
					return nil, got.err
				}
				return agentResultWithText("done"), nil
			}

			result, err := c.resumeAfterTransientFailures(t.Context(), first, nil, tc.err, run)

			require.Equal(t, tc.wantCalls, calls)
			if tc.wantErr != nil {
				require.Equal(t, tc.wantErr, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "done", result.Response.Content.Text())
		})
	}
}

func TestResumeAfterTransientFailuresStopsWaitingWhenCancelled(t *testing.T) {
	t.Parallel()
	c := &coordinator{retryDelays: []time.Duration{time.Hour}}
	ctx, cancel := context.WithCancel(t.Context())
	first := SessionAgentCall{Prompt: "hi", state: &runState{acceptedUserID: "u1"}}
	done := make(chan error, 1)
	go func() {
		_, err := c.resumeAfterTransientFailures(ctx, first, nil, overloaded(), func(SessionAgentCall) (*fantasy.AgentResult, error) {
			t.Error("a cancelled turn must not be resumed")
			return nil, nil
		})
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		require.Equal(t, overloaded(), err)
	case <-time.After(10 * time.Second):
		t.Fatal("the backoff wait didn't end on cancel")
	}
}

func TestRunResumesTheTurnAfterATransientFailure(t *testing.T) {
	t.Parallel()
	env := testEnv(t)
	c, _ := newReloadTestCoordinator(t, env)
	c.retryDelays = []time.Duration{time.Millisecond}
	var calls []SessionAgentCall
	c.setOrchestratorForTest(newMockAgent(reloadTestProvider, 1000, func(_ context.Context, call SessionAgentCall) (*fantasy.AgentResult, error) {
		calls = append(calls, call)
		if len(calls) == 1 {
			call.state.acceptedUserID = "u1"
			return nil, overloaded()
		}
		return agentResultWithText("done"), nil
	}))

	result, err := c.Run(t.Context(), "s", "hi")

	require.NoError(t, err)
	require.Equal(t, "done", result.Response.Content.Text())
	require.Len(t, calls, 2)
	require.Equal(t, "hi", calls[0].Prompt)
	require.Empty(t, calls[1].Prompt)
	require.Equal(t, "s", calls[1].SessionID)
	require.Equal(t, calls[0].MaxOutputTokens, calls[1].MaxOutputTokens)
	require.Equal(t, message.InjectionKindRetry, calls[1].Injections[0].Kind)
}
