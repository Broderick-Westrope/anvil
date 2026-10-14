package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/pubsub"
	"github.com/stretchr/testify/require"
)

func TestViewToolFileNotFound(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		existing []string
		request  string
		want     func(dir string) string
	}{
		"no similar files": {
			existing: []string{"other.txt"},
			request:  "missing.go",
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "missing.go")
			},
		},
		"missing directory": {
			request: filepath.Join("nodir", "main.go"),
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "nodir", "main.go")
			},
		},
		"entry contains the requested name": {
			existing: []string{"main.go", "other.txt"},
			request:  "main",
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "main") +
					"\n\nDid you mean one of these?\n" + filepath.Join(dir, "main.go")
			},
		},
		"requested name contains the entry": {
			existing: []string{"main.go", "other.txt"},
			request:  "main.go.bak",
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "main.go.bak") +
					"\n\nDid you mean one of these?\n" + filepath.Join(dir, "main.go")
			},
		},
		"matching ignores case": {
			existing: []string{"README.md"},
			request:  "readme",
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "readme") +
					"\n\nDid you mean one of these?\n" + filepath.Join(dir, "README.md")
			},
		},
		"at most three suggestions": {
			existing: []string{"a_util.go", "b_util.go", "c_util.go", "d_util.go"},
			request:  "util",
			want: func(dir string) string {
				return "File not found: " + filepath.Join(dir, "util") +
					"\n\nDid you mean one of these?\n" +
					filepath.Join(dir, "a_util.go") + "\n" +
					filepath.Join(dir, "b_util.go") + "\n" +
					filepath.Join(dir, "c_util.go")
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, f := range tc.existing {
				require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644))
			}
			tool := newViewToolForTest(dir)

			resp := runViewTool(t, tool, sessionCtx(), ViewParams{FilePath: tc.request})

			require.True(t, resp.IsError)
			require.Equal(t, tc.want(dir), resp.Content)
		})
	}
}

func TestViewToolStatErrorIsReturned(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a path through a file as not existing, not as a stat error")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644))
	tool := newViewToolForTest(dir)

	_, err := tool.Run(sessionCtx(), fantasy.ToolCall{
		ID:    "test-call",
		Name:  ViewToolName,
		Input: `{"file_path":"file.txt/child"}`,
	})

	require.ErrorContains(t, err, "error accessing file")
}

func TestViewToolDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := newViewToolForTest(dir)

	resp := runViewTool(t, tool, sessionCtx(), ViewParams{FilePath: "."})

	require.True(t, resp.IsError)
	require.Equal(t, "Path is a directory, not a file: "+dir, resp.Content)
}

func TestViewToolRequiresSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644))
	tool := newViewToolForTest(dir)

	_, err := tool.Run(context.Background(), fantasy.ToolCall{
		ID:    "test-call",
		Name:  ViewToolName,
		Input: `{"file_path":"file.txt"}`,
	})

	require.EqualError(t, err, "session ID is required for accessing files outside working directory")
}

func TestViewToolOutsideWorkingDirPermission(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		path        func(workingDir, outsideDir string) string
		wantRequest bool
	}{
		"inside working directory": {
			path: func(workingDir, _ string) string { return filepath.Join(workingDir, "file.txt") },
		},
		"nested inside working directory": {
			path: func(workingDir, _ string) string { return filepath.Join(workingDir, "sub", "file.txt") },
		},
		"name starting with two dots inside working directory": {
			path: func(workingDir, _ string) string { return filepath.Join(workingDir, "..file.txt") },
		},
		"outside working directory": {
			path:        func(_, outsideDir string) string { return filepath.Join(outsideDir, "file.txt") },
			wantRequest: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			outsideDir := t.TempDir()
			path := tc.path(workingDir, outsideDir)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
			perms := &mockViewPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
			tool := newViewToolWithPermissionsForTest(nil, nil, perms, workingDir)
			params := ViewParams{FilePath: path}

			resp := runViewTool(t, tool, sessionCtx(), params)

			require.False(t, resp.IsError, resp.Content)
			if !tc.wantRequest {
				require.Zero(t, perms.requestCount())
				return
			}
			require.Equal(t, []permission.CreatePermissionRequest{{
				SessionID:   "test-session",
				Path:        path,
				ToolCallID:  "test-call",
				ToolName:    ViewToolName,
				Action:      "read",
				Description: "Read file outside working directory: " + path,
				Params:      ViewPermissionsParams(params),
				Input:       path,
			}}, perms.requests)
		})
	}
}

func TestViewToolOutsideWorkingDirDenied(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	perms := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
			return permission.RequestResult{Reason: "not today"}, nil
		},
	}
	tool := newViewToolWithPermissionsForTest(nil, nil, perms, t.TempDir())

	resp := runViewTool(t, tool, sessionCtx(), ViewParams{FilePath: path})

	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn)
	require.Equal(t, "Permission denied: not today", resp.Content)
}

func TestViewToolOutsideWorkingDirRequestErrorIsReturned(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	requestErr := errors.New("permission service down")
	perms := &mockViewPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		requestFunc: func(context.Context, permission.CreatePermissionRequest) (permission.RequestResult, error) {
			return permission.RequestResult{}, requestErr
		},
	}
	tool := newViewToolWithPermissionsForTest(nil, nil, perms, t.TempDir())

	input, err := json.Marshal(ViewParams{FilePath: path})
	require.NoError(t, err)

	_, err = tool.Run(sessionCtx(), fantasy.ToolCall{
		ID:    "test-call",
		Name:  ViewToolName,
		Input: string(input),
	})

	require.ErrorIs(t, err, requestErr)
}
