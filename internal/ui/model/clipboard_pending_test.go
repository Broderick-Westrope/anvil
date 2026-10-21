package model

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteWithinPassesOnTheWritesResult(t *testing.T) {
	t.Parallel()

	var pending pendingWrite
	require.NoError(t, pending.writeWithin(time.Second, func() error { return nil }))
	require.EqualError(t, pending.writeWithin(time.Second, func() error { return errors.New("denied") }), "denied")
}

func TestWriteWithinWontQueueBehindAStuckWrite(t *testing.T) {
	t.Parallel()

	var pending pendingWrite
	release := make(chan struct{})
	finished := make(chan struct{})
	freed := func() bool { return !pending.busy.Load() }
	start := time.Now()
	err := pending.writeWithin(50*time.Millisecond, func() error {
		<-release
		close(finished)
		return nil
	})
	require.ErrorIs(t, err, errClipboardBusy)
	require.EqualError(t, err, "the clipboard didn't take the image in time; try again")
	require.Less(t, time.Since(start), time.Second)

	wrote := false
	err = pending.writeWithin(time.Second, func() error { wrote = true; return nil })
	require.EqualError(t, err, "an earlier copy is still waiting for the clipboard; try again", "the stuck write still holds the clipboard")
	require.False(t, wrote, "a second write would wait behind it and land out of order")

	close(release)
	<-finished
	require.Eventually(t, freed, time.Second, time.Millisecond)
	require.NoError(t, pending.writeWithin(time.Second, func() error { return nil }), "the clipboard is free again once it finishes")
}

func TestWriteWithinIsFreeAsSoonAsAWriteReturns(t *testing.T) {
	t.Parallel()

	var pending pendingWrite
	for range 1000 {
		require.NoError(t, pending.writeWithin(time.Second, func() error { return nil }))
	}
}
