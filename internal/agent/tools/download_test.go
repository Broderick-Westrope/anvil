package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func runDownloadTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params DownloadParams) (fantasy.ToolResponse, error) {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	return tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: DownloadToolName, Input: string(input)})
}

func newDownloadToolForTest(t *testing.T, workingDir string, client *http.Client) (fantasy.AgentTool, *recordingPermissionService) {
	t.Helper()
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest](), allow: true}
	return NewDownloadTool(perms, workingDir, client), perms
}

// deadlineRecorder records the deadline of each request it sends.
type deadlineRecorder struct {
	next     http.RoundTripper
	deadline time.Time
	hasLimit bool
}

func (d *deadlineRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	d.deadline, d.hasLimit = req.Context().Deadline()
	return d.next.RoundTrip(req)
}

func TestDownloadTool_RejectsInvalidParams(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		params DownloadParams
		want   string
	}{
		"missing url":        {params: DownloadParams{FilePath: "out.txt"}, want: "URL parameter is required"},
		"missing file path":  {params: DownloadParams{URL: "https://example.com"}, want: "file_path parameter is required"},
		"unsupported scheme": {params: DownloadParams{URL: "ftp://example.com", FilePath: "out.txt"}, want: "URL must start with http:// or https://"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tool, perms := newDownloadToolForTest(t, t.TempDir(), nil)

			resp, err := runDownloadTool(t, tool, sessionCtx(), tc.params)

			require.NoError(t, err)
			require.True(t, resp.IsError)
			require.Equal(t, tc.want, resp.Content)
			require.Zero(t, perms.requestCount)
		})
	}
}

func TestDownloadTool_RequiresSession(t *testing.T) {
	t.Parallel()
	tool, _ := newDownloadToolForTest(t, t.TempDir(), nil)

	_, err := runDownloadTool(t, tool, context.Background(), DownloadParams{URL: "https://example.com", FilePath: "out.txt"})

	require.EqualError(t, err, "session ID is required for downloading files")
}

func TestDownloadTool_DownloadsFile(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(r.Method + " " + r.UserAgent()))
	})
	workingDir := t.TempDir()
	tool, perms := newDownloadToolForTest(t, workingDir, server.Client())
	params := DownloadParams{URL: server.URL, FilePath: "nested/dir/out.txt", Timeout: 30}

	resp, err := runDownloadTool(t, tool, sessionCtx(), params)

	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Successfully downloaded 13 bytes to nested/dir/out.txt (Content-Type: text/plain)", resp.Content)
	filePath := filepath.Join(workingDir, "nested", "dir", "out.txt")
	got, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "GET anvil/1.0", string(got))
	require.Equal(t, permission.CreatePermissionRequest{
		SessionID:   "test-session",
		Path:        filePath,
		ToolName:    DownloadToolName,
		Action:      "download",
		Description: "Download file from URL: " + server.URL + " to " + filePath,
		Params:      DownloadPermissionsParams(params),
		Input:       server.URL,
	}, perms.lastRequest)
}

func TestDownloadTool_OmitsMissingContentType(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header()["Content-Type"] = nil
		_, _ = w.Write([]byte("abc"))
	})
	tool, _ := newDownloadToolForTest(t, t.TempDir(), server.Client())

	resp, err := runDownloadTool(t, tool, sessionCtx(), DownloadParams{URL: server.URL, FilePath: "out.bin"})

	require.NoError(t, err)
	require.Equal(t, "Successfully downloaded 3 bytes to out.bin", resp.Content)
}

func TestDownloadTool_ReportsPathRelativeToWorkingDir(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, serveBody("", "abc"))
	workingDir := t.TempDir()
	tool, _ := newDownloadToolForTest(t, workingDir, server.Client())
	outside := filepath.Join(t.TempDir(), "out.bin")
	rel, err := filepath.Rel(workingDir, outside)
	require.NoError(t, err)

	resp, err := runDownloadTool(t, tool, sessionCtx(), DownloadParams{URL: server.URL, FilePath: outside})

	require.NoError(t, err)
	require.Equal(t, "Successfully downloaded 3 bytes to "+filepath.ToSlash(rel), resp.Content)
	require.FileExists(t, outside)
}

func TestDownloadTool_PermissionDeniedStopsTurn(t *testing.T) {
	t.Parallel()
	perms := &recordingPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	workingDir := t.TempDir()
	tool := NewDownloadTool(perms, workingDir, nil)

	resp, err := runDownloadTool(t, tool, sessionCtx(), DownloadParams{URL: "https://example.com", FilePath: "out.txt"})

	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn)
	require.Equal(t, "Permission denied", resp.Content)
	require.NoFileExists(t, filepath.Join(workingDir, "out.txt"))
}

func TestDownloadTool_PermissionRequestErrorIsReturned(t *testing.T) {
	t.Parallel()
	requestErr := errors.New("permission service down")
	perms := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
			return permission.RequestResult{}, requestErr
		},
	}
	tool := NewDownloadTool(perms, t.TempDir(), nil)

	_, err := runDownloadTool(t, tool, sessionCtx(), DownloadParams{URL: "https://example.com", FilePath: "out.txt"})

	require.ErrorIs(t, err, requestErr)
}

func TestDownloadTool_NonOKStatus(t *testing.T) {
	t.Parallel()
	server := newFetchServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	workingDir := t.TempDir()
	tool, _ := newDownloadToolForTest(t, workingDir, server.Client())

	resp, err := runDownloadTool(t, tool, sessionCtx(), DownloadParams{URL: server.URL, FilePath: "out.txt"})

	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Equal(t, "Request failed with status code: 404", resp.Content)
	require.NoFileExists(t, filepath.Join(workingDir, "out.txt"))
}

func TestDownloadTool_Timeout(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		timeout   int
		wantLimit time.Duration
	}{
		"no timeout":      {timeout: 0},
		"within the max":  {timeout: 5, wantLimit: 5 * time.Second},
		"at the max":      {timeout: 600, wantLimit: 600 * time.Second},
		"clamped to max":  {timeout: 1000, wantLimit: 600 * time.Second},
		"negative is off": {timeout: -1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := newFetchServer(t, serveBody("text/plain", "ok"))
			recorder := &deadlineRecorder{next: server.Client().Transport}
			tool, perms := newDownloadToolForTest(t, t.TempDir(), &http.Client{Transport: recorder})
			params := DownloadParams{URL: server.URL, FilePath: "out.txt", Timeout: tc.timeout}

			start := time.Now()
			resp, err := runDownloadTool(t, tool, sessionCtx(), params)

			require.NoError(t, err)
			require.False(t, resp.IsError, resp.Content)
			require.Equal(t, DownloadPermissionsParams(params), perms.lastRequest.Params)
			require.Equal(t, tc.wantLimit != 0, recorder.hasLimit)
			if tc.wantLimit != 0 {
				require.WithinDuration(t, start.Add(tc.wantLimit), recorder.deadline, 5*time.Second)
			}
		})
	}
}
