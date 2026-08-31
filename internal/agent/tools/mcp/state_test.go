package mcp

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state State
		want  string
	}{
		{StateDisabled, "disabled"},
		{StateStarting, "starting"},
		{StateConnected, "connected"},
		{StateError, "error"},
		{StateLazy, "lazy"},
		{StateDeferred, "deferred"},
		{State(99), "unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.state.String())
	}
}

func TestUpdateState_NeedsAuth(t *testing.T) {
	t.Parallel()

	const name = "auth-flag-test"
	t.Cleanup(func() { states.Del(name) })

	t.Run("StateError with auth error sets NeedsAuth true", func(t *testing.T) {
		authErr := fmt.Errorf("wrapped: %w", ErrNeedsAuth)
		updateState(name, StateError, authErr, nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.True(t, info.NeedsAuth)
	})

	t.Run("StateError with plain error sets NeedsAuth false", func(t *testing.T) {
		updateState(name, StateError, fmt.Errorf("plain"), nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.False(t, info.NeedsAuth)
	})

	t.Run("StateConnected with stale auth error sets NeedsAuth false", func(t *testing.T) {
		authErr := fmt.Errorf("stale: %w", ErrNeedsAuth)
		updateState(name, StateConnected, authErr, nil, Counts{})
		info, ok := GetState(name)
		require.True(t, ok)
		require.False(t, info.NeedsAuth,
			"NeedsAuth must be false when state is not StateError")
	})
}
