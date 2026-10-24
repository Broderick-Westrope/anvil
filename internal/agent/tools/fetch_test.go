package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

const fetchTestPage = `<html><head><title>Ignored</title></head><body><h1>Title</h1>
<p>Some   <b>bold</b>
text</p></body></html>`

func newFetchServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func serveBody(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}
}

func runFetchTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params FetchParams) (fantasy.ToolResponse, error) {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	return tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: FetchToolName, Input: string(input)})
}

func newFetchToolForTest(t *testing.T, client *http.Client) (fantasy.AgentTool, *recordingPermissionService) {
	t.Helper()
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}
	return NewFetchTool(perms, "/work", client), perms
}

func TestFetchTool_RejectsInvalidParams(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		params FetchParams
		want   string
	}{
		"missing url":        {params: FetchParams{Format: "text"}, want: "URL parameter is required"},
		"unknown format":     {params: FetchParams{URL: "https://example.com", Format: "pdf"}, want: "Format must be one of: text, markdown, html"},
		"empty format":       {params: FetchParams{URL: "https://example.com"}, want: "Format must be one of: text, markdown, html"},
		"unsupported scheme": {params: FetchParams{URL: "ftp://example.com", Format: "text"}, want: "URL must start with http:// or https://"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, perms := newFetchToolForTest(t, nil)

			resp, err := runFetchTool(t, tool, sessionCtx(), tc.params)

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
			require.Zero(t, perms.requestCount)
		})
	}
}

func TestFetchTool_RequiresSession(t *testing.T) {
	t.Parallel()
	tool, _ := newFetchToolForTest(t, nil)

	_, err := runFetchTool(t, tool, context.Background(), FetchParams{URL: "https://example.com", Format: "text"})

	require.EqualError(t, err, "session ID is required for creating a new file")
}

func TestFetchTool_PermissionRequest(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, serveBody("text/plain", "ok"))
	tool, perms := newFetchToolForTest(t, server.Client())
	params := FetchParams{URL: server.URL, Format: "TEXT"}

	resp, err := runFetchTool(t, tool, sessionCtx(), params)

	require.NoError(t, err)
	require.Equal(t, "ok", resp.Content)
	require.Equal(t, permission.CreatePermissionRequest{
		SessionID:   "test-session",
		Path:        "/work",
		ToolCallID:  "test-call",
		ToolName:    FetchToolName,
		Action:      "fetch",
		Description: "Fetch content from URL: " + server.URL,
		Params:      FetchPermissionsParams(params),
		Input:       server.URL,
	}, perms.lastRequest)
}

func TestFetchTool_PermissionDeniedStopsTurn(t *testing.T) {
	t.Parallel()
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	tool := NewFetchTool(perms, "/work", nil)

	resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: "https://example.com", Format: "text"})

	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn)
	require.Equal(t, "Permission denied", resp.Content)
}

func TestFetchTool_PermissionRequestErrorIsReturned(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("permission service down")
	perms := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
			return permission.RequestResult{}, requestErr
		},
	}
	tool := NewFetchTool(perms, "/work", nil)

	_, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: "https://example.com", Format: "text"})

	require.ErrorIs(t, err, requestErr)
}

func TestFetchTool_ConvertsBodyToFormat(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		contentType string
		body        string
		format      string
		want        string
		wantError   bool
	}{
		"text from html": {
			contentType: "text/html; charset=utf-8", body: fetchTestPage, format: "text",
			want: "Title Some bold text",
		},
		"text from plain": {
			contentType: "text/plain", body: "<b>raw</b>", format: "text",
			want: "<b>raw</b>",
		},
		"markdown from html": {
			contentType: "text/html", body: fetchTestPage, format: "markdown",
			want: "```\nIgnored\n\n# Title\n\nSome **bold**\ntext\n```",
		},
		"markdown from plain": {
			contentType: "text/plain", body: "<b>raw</b>", format: "markdown",
			want: "```\n<b>raw</b>\n```",
		},
		"html from html": {
			contentType: "text/html", body: fetchTestPage, format: "html",
			want: "<html>\n<body>\n<h1>Title</h1>\n<p>Some   <b>bold</b>\ntext</p>\n</body>\n</html>",
		},
		"html from html without body content": {
			contentType: "text/html", body: "<html><head><title>x</title></head><body></body></html>", format: "html",
			want: "No body content found in HTML", wantError: true,
		},
		"html from plain": {
			contentType: "text/plain", body: "<b>raw</b>", format: "html",
			want: "<b>raw</b>",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newFetchServer(t, serveBody(tc.contentType, tc.body))
			tool, _ := newFetchToolForTest(t, server.Client())

			resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: server.URL, Format: tc.format})

			require.NoError(t, err)
			require.Equal(t, tc.wantError, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestFetchTool_ResponseFailures(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"non-200 status": {
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    "Request failed with status code: 404",
		},
		"non-200 status with a body": {
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte("rate limit exceeded"))
			},
			want: "Request failed with status code: 429 (Retry-After: 60): rate limit exceeded",
		},
		"invalid utf-8": {
			handler: serveBody("text/plain", "bad \xff byte"),
			want:    "Response content is not valid UTF-8",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newFetchServer(t, tc.handler)
			tool, _ := newFetchToolForTest(t, server.Client())

			resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: server.URL, Format: "text"})

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestFetchTool_SendsUserAgent(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method + " " + r.UserAgent()))
	})
	tool, _ := newFetchToolForTest(t, nil)

	resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: server.URL, Format: "text"})

	require.NoError(t, err)
	require.Equal(t, "GET anvil/1.0", resp.Content)
}

func TestFetchTool_Truncation(t *testing.T) {
	t.Parallel()
	const note = "\n\n[Content truncated to 102400 bytes]"
	tests := map[string]struct {
		body   string
		format string
		want   string
	}{
		"just under the limit": {
			body: strings.Repeat("a", MaxFetchSize-1), format: "text",
			want: strings.Repeat("a", MaxFetchSize-1),
		},
		"at the limit": {
			body: strings.Repeat("a", MaxFetchSize), format: "text",
			want: strings.Repeat("a", MaxFetchSize) + note,
		},
		"over the limit": {
			body: strings.Repeat("a", MaxFetchSize+10), format: "text",
			want: strings.Repeat("a", MaxFetchSize) + note,
		},
		"pushed over the limit by markdown fences": {
			body: strings.Repeat("a", MaxFetchSize-5), format: "markdown",
			want: "```\n" + strings.Repeat("a", MaxFetchSize-5) + "\n" + note,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newFetchServer(t, serveBody("text/plain", tc.body))
			tool, _ := newFetchToolForTest(t, server.Client())

			resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: server.URL, Format: tc.format})

			require.NoError(t, err)
			require.False(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
		})
	}
}

func TestFetchTool_Timeout(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		timeout int
		delay   time.Duration
		wantErr bool
	}{
		"responds within timeout":         {timeout: 5, delay: 0},
		"responds within clamped timeout": {timeout: 1000, delay: 0},
		"exceeds timeout":                 {timeout: 1, delay: 3 * time.Second, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			release := make(chan struct{})
			server := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(tc.delay):
				case <-release:
				}
				_, _ = w.Write([]byte("ok"))
			})
			t.Cleanup(func() { close(release) })
			tool, _ := newFetchToolForTest(t, server.Client())

			resp, err := runFetchTool(t, tool, sessionCtx(), FetchParams{URL: server.URL, Format: "text", Timeout: tc.timeout})

			if tc.wantErr {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.ErrorContains(t, err, "failed to fetch URL")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "ok", resp.Content)
		})
	}
}
