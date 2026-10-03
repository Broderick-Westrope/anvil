package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

// capturingPermissionService grants every request and records the
// requests it saw.
type capturingPermissionService struct {
	mockPermissionService
	requests []permission.CreatePermissionRequest
}

func (c *capturingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
	c.requests = append(c.requests, req)
	return permission.RequestResult{Granted: true}, nil
}

func (c *capturingPermissionService) last(t *testing.T) permission.CreatePermissionRequest {
	t.Helper()
	require.NotEmpty(t, c.requests)
	return c.requests[len(c.requests)-1]
}

func newCapturingEditContext(t *testing.T, dir string) (editContext, *capturingPermissionService) {
	t.Helper()
	perms := &capturingPermissionService{}
	return editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: perms,
		filetracker: &mockEditFileTracker{},
		workingDir:  dir,
	}, perms
}

func TestEditPermissionRequestCarriesDiff(t *testing.T) {
	t.Parallel()

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		edit, perms := newCapturingEditContext(t, dir)
		_, err := createNewFile(edit, filepath.Join(dir, "new.txt"), "hello\n", fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		req := perms.last(t)
		require.Contains(t, req.Diff, "+hello")
		require.Equal(t, "hello\n", req.Content)
	})

	t.Run("replace", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "a.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("keep\nfunc important() {}\n"), 0o644))
		edit, perms := newCapturingEditContext(t, dir)
		_, err := replaceContent(edit, filePath, "func important() {}", "// gone", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		req := perms.last(t)
		require.Contains(t, req.Diff, "-func important() {}")
		require.Contains(t, req.Diff, "+// gone")
		require.Equal(t, "// gone", req.Content)
	})

	t.Run("delete", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "a.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("keep\nremove me\n"), 0o644))
		edit, perms := newCapturingEditContext(t, dir)
		_, err := deleteContent(edit, filePath, "remove me\n", false, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		req := perms.last(t)
		require.Contains(t, req.Diff, "-remove me")
		require.Equal(t, "keep\n", req.Content)
	})
}

func TestMultiEditPermissionRequestCarriesDiff(t *testing.T) {
	t.Parallel()

	t.Run("existing", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "a.txt")
		require.NoError(t, os.WriteFile(filePath, []byte("one\ntwo\n"), 0o644))
		edit, perms := newCapturingEditContext(t, dir)
		params := MultiEditParams{FilePath: filePath, Edits: []MultiEditOperation{{OldString: "two", NewString: "TWO"}}}
		_, err := processMultiEditExistingFile(edit, params, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.Contains(t, perms.last(t).Diff, "-two")
	})

	t.Run("create", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		edit, perms := newCapturingEditContext(t, dir)
		params := MultiEditParams{FilePath: filepath.Join(dir, "b.txt"), Edits: []MultiEditOperation{{NewString: "fresh\n"}}}
		_, err := processMultiEditWithCreation(edit, params, fantasy.ToolCall{ID: "call"})
		require.NoError(t, err)
		require.Contains(t, perms.last(t).Diff, "+fresh")
	})
}

func TestWritePermissionRequestCarriesDiff(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)
	perms := &capturingPermissionService{}
	tool := NewWriteTool(nil, perms, tracker, workingDir)

	resp := runWrite(t, tool, "existing.txt", "alpha\n")
	require.False(t, resp.IsError)
	req := perms.last(t)
	require.Contains(t, req.Diff, "-beta")
	require.Equal(t, "alpha\n", req.Content)
}
