package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/Broderick-Westrope/anvil/internal/filetracker"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/stretchr/testify/require"
)

// mockFileTrackerService is a stateful filetracker mock keyed by path.
type mockFileTrackerService struct {
	hashes map[string]string
}

func newMockFileTracker() *mockFileTrackerService {
	return &mockFileTrackerService{hashes: map[string]string{}}
}

func (m *mockFileTrackerService) RecordRead(ctx context.Context, sessionID, path string) {
	hash := ""
	if content, err := os.ReadFile(path); err == nil {
		hash = filetracker.HashContent(content)
	}
	m.hashes[path] = hash
}

func (m *mockFileTrackerService) RecordReadWithHash(ctx context.Context, sessionID, path, hash string) {
	m.hashes[path] = hash
}

func (m *mockFileTrackerService) LastContentHash(ctx context.Context, sessionID, path string) string {
	return m.hashes[path]
}

func (m *mockFileTrackerService) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return time.Time{}
}

func (m *mockFileTrackerService) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

func runWrite(t *testing.T, tool fantasy.AgentTool, filePath, content string) fantasy.ToolResponse {
	t.Helper()

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	input, err := json.Marshal(WriteParams{FilePath: filePath, Content: content})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  WriteToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func TestWriteToolWritesEmptyNewFile(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, newMockFileTracker(), workingDir)

	resp := runWrite(t, tool, "empty.txt", "")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filepath.Join(workingDir, "empty.txt"))
	require.NoError(t, err)
	require.Equal(t, "", string(b))
}

func TestWriteToolNewFileIsUngated(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, newMockFileTracker(), workingDir)

	resp := runWrite(t, tool, "new.txt", "hello\n")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filepath.Join(workingDir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(b))
}

func TestWriteToolGateBlocksNeverSeenFileThenRetrySucceeds(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("original content\n"), 0o644))

	tool := NewWriteTool(nil, &mockPermissionService{}, newMockFileTracker(), workingDir)

	// First write fails: file never seen this session. The error contains
	// the current content and counts as a read.
	resp := runWrite(t, tool, "existing.txt", "new content\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "original content")
	require.Contains(t, resp.Content, "This counts as a read")

	// Immediate retry succeeds without an intervening view.
	resp = runWrite(t, tool, "existing.txt", "new content\n")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "new content\n", string(b))
}

func TestWriteToolGateBlocksExternalChangeThenRetrySucceeds(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("seen content\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)

	tool := NewWriteTool(nil, &mockPermissionService{}, tracker, workingDir)

	// External change after the file was seen.
	require.NoError(t, os.WriteFile(filePath, []byte("changed externally\n"), 0o644))

	resp := runWrite(t, tool, "existing.txt", "new content\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "changed externally")

	// Retry passes: the failed call recorded the current hash.
	resp = runWrite(t, tool, "existing.txt", "new content\n")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "new content\n", string(b))
}

func TestWriteToolMtimeOnlyTouchDoesNotTripGate(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("stable content\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)

	tool := NewWriteTool(nil, &mockPermissionService{}, tracker, workingDir)

	// Formatter-style touch: identical bytes, future mtime.
	require.NoError(t, os.WriteFile(filePath, []byte("stable content\n"), 0o644))
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filePath, future, future))

	resp := runWrite(t, tool, "existing.txt", "new content\n")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "new content\n", string(b))
}

func TestWriteToolPreservesCRLF(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "crlf.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)

	tool := NewWriteTool(nil, &mockPermissionService{}, tracker, workingDir)

	resp := runWrite(t, tool, "crlf.txt", "alpha\ngamma\n")
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\ngamma\r\n", string(b))

	// The recorded hash matches the written bytes, so an immediate
	// follow-up write is not spuriously gated.
	resp = runWrite(t, tool, "crlf.txt", "alpha\ndelta\n")
	require.False(t, resp.IsError)

	b, err = os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\ndelta\r\n", string(b))
}

func TestWriteToolIdenticalContentShortCircuitsPostConversion(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "crlf.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)

	tool := NewWriteTool(nil, &mockPermissionService{}, tracker, workingDir)

	// LF content that matches the CRLF file after conversion.
	resp := runWrite(t, tool, "crlf.txt", "alpha\nbeta\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "already contains the exact content")
}

func TestWriteToolSuccessResponseIncludesDiff(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	tracker := newMockFileTracker()
	tracker.RecordRead(t.Context(), "test-session", filePath)

	tool := NewWriteTool(nil, &mockPermissionService{}, tracker, workingDir)

	resp := runWrite(t, tool, "existing.txt", "alpha\ngamma\n")
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "-beta")
	require.Contains(t, resp.Content, "+gamma")
}

// denyingPermissionService denies every permission request.
type denyingPermissionService struct {
	mockPermissionService
}

func (d *denyingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (permission.RequestResult, error) {
	return permission.RequestResult{Granted: false, Reason: "denied"}, nil
}

func TestWriteToolGateOutsideWorkingDirDeniedOmitsContent(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	filePath := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("top secret\n"), 0o644))

	tracker := newMockFileTracker()
	tool := NewWriteTool(nil, &denyingPermissionService{}, tracker, workingDir)

	resp := runWrite(t, tool, filePath, "new content\n")
	require.True(t, resp.IsError)
	require.NotContains(t, resp.Content, "top secret")
	require.Contains(t, resp.Content, "Permission to read its content was denied")

	// The denied call must not count as a read.
	require.Empty(t, tracker.hashes[filePath])

	// The file must be untouched.
	b, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "top secret\n", string(b))
}

func TestWriteToolGateErrorBinaryContentNotDisplayed(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "bin.dat")
	require.NoError(t, os.WriteFile(filePath, []byte{0xff, 0xfe, 0x00, 0x81}, 0o644))

	tool := NewWriteTool(nil, &mockPermissionService{}, newMockFileTracker(), workingDir)

	resp := runWrite(t, tool, "bin.dat", "new content\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "binary and cannot be displayed")
	require.NotContains(t, resp.Content, "Current file content")
}

func TestWriteToolGateErrorTruncatesLongContent(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "long.txt")
	var sb strings.Builder
	for i := range DefaultReadLimit + 50 {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	require.NoError(t, os.WriteFile(filePath, []byte(sb.String()), 0o644))

	tool := NewWriteTool(nil, &mockPermissionService{}, newMockFileTracker(), workingDir)

	resp := runWrite(t, tool, "long.txt", "new content\n")
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "line 0\n")
	require.Contains(t, resp.Content, fmt.Sprintf("(Content truncated at %d lines.)", DefaultReadLimit))
	require.NotContains(t, resp.Content, fmt.Sprintf("line %d\n", DefaultReadLimit))
}
