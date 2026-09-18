package recovery

import (
	"encoding/json"
	"os"
	"testing"

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
