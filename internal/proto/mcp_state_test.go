package proto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state MCPState
		want  string
	}{
		{MCPStateDisabled, "disabled"},
		{MCPStateStarting, "starting"},
		{MCPStateConnected, "connected"},
		{MCPStateError, "error"},
		{MCPStateLazy, "lazy"},
		{MCPState(99), "unknown"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, tt.state.String())
	}
}

func TestMCPState_UnmarshalText_Lazy(t *testing.T) {
	t.Parallel()

	var s MCPState
	err := s.UnmarshalText([]byte("lazy"))
	require.NoError(t, err)
	require.Equal(t, MCPStateLazy, s)
}
