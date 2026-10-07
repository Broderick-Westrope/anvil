//go:build !windows

package cmd

import (
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTerminationWatchReportsSIGTERM(t *testing.T) {
	watch := watchTermination()
	defer watch.Stop()
	require.False(t, watch.Received())
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	require.Eventually(t, watch.Received, 5*time.Second, 10*time.Millisecond)
	require.True(t, watch.Received(), "stays true once a signal has arrived")
}
