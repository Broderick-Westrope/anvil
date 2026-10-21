//go:build (darwin || windows) && !ios && !arm && !386

package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteWithinPassesOnTheWritesResult(t *testing.T) {
	t.Parallel()

	require.NoError(t, writeWithin(time.Second, func() error { return nil }))
	require.EqualError(t, writeWithin(time.Second, func() error { return errors.New("denied") }), "denied")
}

func TestWriteWithinGivesUpOnAClipboardThatStaysBusy(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	start := time.Now()
	err := writeWithin(50*time.Millisecond, func() error {
		<-release
		return nil
	})
	require.ErrorIs(t, err, errClipboardBusy)
	require.EqualError(t, err, "another app is holding the clipboard; try again")
	require.Less(t, time.Since(start), time.Second)
}
