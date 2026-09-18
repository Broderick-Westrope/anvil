package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/stretchr/testify/require"
)

func TestSessionRecoverListsOnlyInterruptedSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := recovery.NewTracker(root)
	require.NoError(t, err)
	tracker.Track(recovery.Entry{SessionID: "full-session-id", WorkingDir: "/project with spaces", Title: "Fix\nthings\x1b[31m"})
	require.NoError(t, tracker.Close(false))
	command := newSessionRecoverCommand(root)
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "full-session-id")
	require.Contains(t, output.String(), "/project with spaces")
	require.Contains(t, output.String(), "Fix things")
	require.NotContains(t, output.String(), "\x1b")
	entries, err := recovery.List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	command = newSessionRecoverCommand(root)
	command.SetOut(&output)
	command.SetArgs([]string{"--json"})
	output.Reset()
	require.NoError(t, command.Execute())
	var result []recovery.Entry
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Len(t, result, 1)
	require.Equal(t, "full-session-id", result[0].SessionID)
	require.Equal(t, "/project with spaces", result[0].WorkingDir)

	command = newSessionRecoverCommand(root)
	command.SetOut(&output)
	command.SetArgs([]string{"--clear"})
	require.NoError(t, command.Execute())
	entries, err = recovery.List(root)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestSessionRecoverWarnsAndPrintsPartialResults(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "valid.json"), []byte(`{"session_id":"valid","title":"Work"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "bad.json"), []byte("{"), 0o600))
	command := newSessionRecoverCommand(root)
	var output, warnings bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&warnings)
	command.SetArgs([]string{"--json"})
	require.NoError(t, command.Execute())
	var entries []recovery.Entry
	require.NoError(t, json.Unmarshal(output.Bytes(), &entries))
	require.Len(t, entries, 1)
	require.Equal(t, "valid", entries[0].SessionID)
	require.Contains(t, warnings.String(), "bad.json")
	require.Contains(t, warnings.String(), "Warning")
}

func TestSessionRecoverEmpty(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "missing")
	command := newSessionRecoverCommand(root)
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "No interrupted sessions")
	command = newSessionRecoverCommand(root)
	command.SetOut(&output)
	command.SetArgs([]string{"--json"})
	output.Reset()
	require.NoError(t, command.Execute())
	require.JSONEq(t, "[]", output.String())
}
