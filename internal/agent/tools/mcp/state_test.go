package mcp

import (
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
