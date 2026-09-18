package recovery

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetryRenameTransientPermissionFailure(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := retryRename(func() error {
		attempts++
		if attempts < 3 {
			return &os.LinkError{Op: "rename", Err: os.ErrPermission}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts)
}

func TestRetryRenamePermanentFailure(t *testing.T) {
	t.Parallel()
	attempts := 0
	failure := errors.New("disk failure")
	err := retryRename(func() error {
		attempts++
		return failure
	})
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, attempts)
}

func TestRetryRenameBounded(t *testing.T) {
	t.Parallel()
	attempts := 0
	err := retryRename(func() error {
		attempts++
		return os.ErrPermission
	})
	require.ErrorIs(t, err, os.ErrPermission)
	require.Greater(t, attempts, 1)
	require.Less(t, attempts, 100)
}
