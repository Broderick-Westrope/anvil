//go:build !windows

package tools

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/skills"
	"github.com/stretchr/testify/require"
)

func TestOpenRegularFileSocketAndDevice(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "s")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)
	defer listener.Close()
	for _, path := range []string{path, "/dev/null"} {
		_, err := openRegularFile(path)
		require.ErrorIs(t, err, errNotRegularSource)
	}
}

func TestOpenRegularFileFIFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "fifo")
	require.NoError(t, syscall.Mkfifo(fifoPath, 0o600))

	_, err := openRegularFile(fifoPath)
	require.Error(t, err)
	require.True(t, errors.Is(err, errNotRegularSource))
}

func TestViewToolSkillNameFIFOSourceIsPromptlyRejected(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	skillDir := filepath.Join(workingDir, "fifo-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	fifoPath := filepath.Join(skillDir, "SKILL.md")
	require.NoError(t, syscall.Mkfifo(fifoPath, 0o600))

	registry := []*skills.Skill{{Name: "fifo-skill", Description: "d", SkillFilePath: fifoPath}}
	tool := newViewToolWithRegistryForTest(registry, workingDir)

	done := make(chan struct{})
	var respIsError bool
	var respContent string
	go func() {
		resp := runViewTool(t, tool, sessionCtx(), ViewParams{SkillName: "fifo-skill"})
		respIsError = resp.IsError
		respContent = resp.Content
		close(done)
	}()

	select {
	case <-done:
		require.True(t, respIsError)
		require.Contains(t, respContent, "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("view tool did not return promptly for a FIFO source; a blocking open(2) is not being avoided")
	}
}
