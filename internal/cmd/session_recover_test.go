package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Broderick-Westrope/anvil/internal/recovery"
	"github.com/Broderick-Westrope/anvil/internal/session"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSessionRecoverListsOnlyInterruptedSessions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tracker, err := recovery.NewTracker(root)
	require.NoError(t, err)
	tracker.Track(recovery.Entry{SessionID: "full-session-id", WorkingDir: "/project with spaces", Title: "Fix\nthings\x1b[31m"})
	require.NoError(t, tracker.Close(false))
	command := newSessionRecoverCommand(root, nil)
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "Session: full-session-id\n")
	require.Contains(t, output.String(), "Directory: /project with spaces\n")
	require.Contains(t, output.String(), "Title: Fix things\n")
	require.Contains(t, output.String(), "/project with spaces")
	require.Contains(t, output.String(), "Fix things")
	require.NotContains(t, output.String(), "\x1b")
	entries, err := recovery.List(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	command = newSessionRecoverCommand(root, nil)
	command.SetOut(&output)
	command.SetArgs([]string{"--json"})
	output.Reset()
	require.NoError(t, command.Execute())
	var result []recovery.Entry
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.Len(t, result, 1)
	require.Equal(t, "full-session-id", result[0].SessionID)
	require.Equal(t, "/project with spaces", result[0].WorkingDir)

	command = newSessionRecoverCommand(root, nil)
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
	command := newSessionRecoverCommand(root, nil)
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
	command := newSessionRecoverCommand(root, nil)
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "No interrupted sessions")
	command = newSessionRecoverCommand(root, nil)
	command.SetOut(&output)
	command.SetArgs([]string{"--json"})
	output.Reset()
	require.NoError(t, command.Execute())
	require.JSONEq(t, "[]", output.String())
}

func writeRecoveryRecord(t *testing.T, root, name string, entry recovery.Entry) {
	t.Helper()
	data, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, name+".json"), data, 0o600))
}

func TestSessionRecoverShowsOnlyLatestInterruption(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	restart := time.Now().Add(-time.Hour).UTC()
	writeRecoveryRecord(t, root, "a", recovery.Entry{SessionID: "latest-a", Title: "A", SeenAt: restart})
	writeRecoveryRecord(t, root, "b", recovery.Entry{SessionID: "latest-b", Title: "B", SeenAt: restart.Add(-30 * time.Second)})
	writeRecoveryRecord(t, root, "c", recovery.Entry{SessionID: "stale", Title: "C", SeenAt: restart.Add(-72 * time.Hour)})

	run := func(args ...string) string {
		command := newSessionRecoverCommand(root, nil)
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(args)
		require.NoError(t, command.Execute())
		return output.String()
	}

	latest := run()
	require.Contains(t, latest, "(2 sessions)")
	require.Contains(t, latest, "Session: latest-a\n")
	require.Contains(t, latest, "Session: latest-b\n")
	require.NotContains(t, latest, "stale")
	require.Contains(t, latest, "1 session from earlier interruptions not shown. Use --all")

	everything := run("--all")
	require.Contains(t, everything, "Session: stale\n")
	require.Equal(t, 2, strings.Count(everything, "Interrupted around"))
	require.NotContains(t, everything, "not shown")

	var entries []recovery.Entry
	require.NoError(t, json.Unmarshal([]byte(run("--json")), &entries))
	require.Len(t, entries, 2)
	require.NoError(t, json.Unmarshal([]byte(run("--json", "--all")), &entries))
	require.Len(t, entries, 3)
}

func TestSessionRecoverHidesSessionsWithNothingToResume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seen := time.Now().UTC()
	writeRecoveryRecord(t, root, "a", recovery.Entry{SessionID: "worked", Title: "New Session", SeenAt: seen})
	writeRecoveryRecord(t, root, "b", recovery.Entry{SessionID: "empty", Title: "Empty", SeenAt: seen})
	writeRecoveryRecord(t, root, "c", recovery.Entry{SessionID: "deleted", Title: "Deleted", SeenAt: seen})
	writeRecoveryRecord(t, root, "d", recovery.Entry{SessionID: "unreadable", Title: "Recorded title", SeenAt: seen})
	stored := map[string]session.Session{
		"worked": {ID: "worked", Title: "Generated title", MessageCount: 4},
		"empty":  {ID: "empty", Title: "Empty"},
	}
	closed := false
	open := func(*cobra.Command) (sessionLookup, func(), error) {
		return func(_ context.Context, id string) (session.Session, error) {
			if id == "unreadable" {
				return session.Session{}, errors.New("database is locked")
			}
			found, ok := stored[id]
			if !ok {
				return session.Session{}, sql.ErrNoRows
			}
			return found, nil
		}, func() { closed = true }, nil
	}
	command := newSessionRecoverCommand(root, open)
	var output, warnings bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&warnings)
	require.NoError(t, command.Execute())
	require.True(t, closed)
	require.Contains(t, output.String(), "(2 sessions)")
	require.Contains(t, output.String(), "Title: Generated title\n")
	require.Contains(t, output.String(), "Title: Recorded title\n")
	require.NotContains(t, output.String(), "Empty")
	require.NotContains(t, output.String(), "Deleted")
	require.Contains(t, warnings.String(), "database is locked")
}

func TestSessionRecoverListsRecordsWhenSessionStoreUnavailable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRecoveryRecord(t, root, "a", recovery.Entry{SessionID: "kept", SeenAt: time.Now().UTC()})
	open := func(*cobra.Command) (sessionLookup, func(), error) {
		return nil, nil, errors.New("no database")
	}
	command := newSessionRecoverCommand(root, open)
	var output, warnings bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&warnings)
	require.NoError(t, command.Execute())
	require.Contains(t, output.String(), "Session: kept\n")
	require.Contains(t, warnings.String(), "no database")
}

func TestSessionRecoverPrintsResumeCommands(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	restart := time.Now().UTC()
	writeRecoveryRecord(t, root, "a", recovery.Entry{SessionID: "first", WorkingDir: "/a", Title: "First", SeenAt: restart})
	writeRecoveryRecord(t, root, "b", recovery.Entry{SessionID: "it's-second", WorkingDir: "/b with space", Title: "Multi\nline title", SeenAt: restart.Add(-time.Second)})
	writeRecoveryRecord(t, root, "c", recovery.Entry{SessionID: "stale", WorkingDir: "/c", SeenAt: restart.Add(-24 * time.Hour)})
	command := newSessionRecoverCommand(root, nil)
	var output, notes bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&notes)
	command.SetArgs([]string{"--print-commands"})
	require.NoError(t, command.Execute())
	require.Equal(t, "# First\ncd /a && anvil --session first\n\n# Multi line title\ncd '/b with space' && anvil --session 'it'\\''s-second'\n\n", output.String())
	require.Contains(t, notes.String(), "1 session from earlier interruptions not shown")

	command = newSessionRecoverCommand(root, nil)
	output.Reset()
	command.SetOut(&output)
	command.SetErr(&notes)
	command.SetArgs([]string{"--print-commands", "--all"})
	require.NoError(t, command.Execute())
	require.Equal(t, 3, strings.Count(output.String(), "&& anvil --session"))

	command = newSessionRecoverCommand(root, nil)
	command.SetOut(&output)
	command.SetErr(&notes)
	command.SetArgs([]string{"--print-commands", "--json"})
	require.Error(t, command.Execute())
}
