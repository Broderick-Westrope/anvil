package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestRetryRenameWindowsSharingViolation(t *testing.T) {
	t.Parallel()
	tracker, err := NewTracker(t.TempDir())
	require.NoError(t, err)
	defer tracker.Close(true)
	require.NoError(t, tracker.write(Entry{SessionID: "previous"}))
	path, err := windows.UTF16PtrFromString(tracker.path)
	require.NoError(t, err)
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	source := tracker.path + ".tmp"
	require.NoError(t, os.WriteFile(source, []byte(`{"session_id":"replacement"}`), 0o600))
	attempts := 0
	err = retryRename(func() error {
		attempts++
		err := os.Rename(source, tracker.path)
		if attempts == 1 {
			require.Error(t, err)
			require.NoError(t, windows.CloseHandle(handle))
		}
		return err
	})
	require.NoError(t, err)
	require.Greater(t, attempts, 1)
	require.NoError(t, tracker.write(Entry{SessionID: "latest"}))
	data, err := os.ReadFile(tracker.path)
	require.NoError(t, err)
	var entry Entry
	require.NoError(t, json.Unmarshal(data, &entry))
	require.Equal(t, "latest", entry.SessionID)
}

// holdExclusive opens path with no sharing, the way a concurrent rename or
// delete in another process briefly holds a recovery record.
func holdExclusive(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	return handle
}

func TestReadRecordWaitsOutSharingViolation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "record.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"session_id":"s"}`), 0o600))
	handle := holdExclusive(t, path)

	_, err := os.ReadFile(path)
	require.ErrorIs(t, err, errSharingViolation, "precondition: the held handle blocks reads")

	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = windows.CloseHandle(handle)
		close(released)
	}()
	data, err := readRecord(path)
	<-released
	require.NoError(t, err)
	require.JSONEq(t, `{"session_id":"s"}`, string(data))
}

func TestRemoveRecordWaitsOutSharingViolation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "record.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))
	handle := holdExclusive(t, path)

	require.ErrorIs(t, os.Remove(path), errSharingViolation, "precondition: the held handle blocks deletes")

	released := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = windows.CloseHandle(handle)
		close(released)
	}()
	err := removeRecord(path)
	<-released
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}
