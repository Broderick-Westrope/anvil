package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"testing"
	"time"

	"charm.land/fantasy"
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
		"3 no text output":                    {err: wrap(errNoTextOutput), retry: false},
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
			retry, reason := classifySubagentFailure(ctx, tc.err)
			require.Equal(t, tc.retry, retry)
			if retry {
				require.NotEmpty(t, reason)
			}
		})
	}
}

func TestSubagentFailureReasonNamesTheStatus(t *testing.T) {
	t.Parallel()
	_, reason := classifySubagentFailure(t.Context(), fmt.Errorf("x: %w", &fantasy.ProviderError{StatusCode: 503, Title: "overloaded"}))
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
			require.Equal(t, tc.want, subagentRetryDelay(fmt.Errorf("w: %w", tc.err), tc.base))
		})
	}
}

func TestSubagentRetryDelayReadsAnHTTPDateHint(t *testing.T) {
	t.Parallel()
	at := time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)
	err := &fantasy.ProviderError{StatusCode: 503, ResponseHeaders: map[string]string{"retry-after": at}}
	got := subagentRetryDelay(err, 5*time.Second)
	require.Greater(t, got, 15*time.Second)
	require.LessOrEqual(t, got, 20*time.Second)
}
