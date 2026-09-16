package pubsub

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPublish_DoesNotSetMustDeliver verifies the lossy path leaves the
// flag unset so forwarders keep using the lossy path downstream.
func TestPublish_DoesNotSetMustDeliver(t *testing.T) {
	t.Parallel()

	b := NewBroker[string]()
	defer b.Shutdown()

	ch := b.Subscribe(t.Context())
	b.Publish(UpdatedEvent, "delta")

	select {
	case ev := <-ch:
		require.False(t, ev.MustDeliver, "lossy Publish must not set MustDeliver")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

// TestPublishMustDeliver_SetsMustDeliver verifies terminal events carry
// the flag so fan-in forwarders can preserve delivery semantics.
func TestPublishMustDeliver_SetsMustDeliver(t *testing.T) {
	t.Parallel()

	b := NewBroker[string]()
	defer b.Shutdown()

	ch := b.Subscribe(t.Context())
	b.PublishMustDeliver(t.Context(), UpdatedEvent, "finish")

	select {
	case ev := <-ch:
		require.True(t, ev.MustDeliver, "PublishMustDeliver must set MustDeliver")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}
